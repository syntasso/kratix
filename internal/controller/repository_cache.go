package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/delivery"
	"github.com/syntasso/kratix/lib/writers"
	gitutil "github.com/syntasso/kratix/util/git"
	corev1 "k8s.io/api/core/v1"
)

type Repository struct {
	sync.Mutex
	Path          string
	Branch        string
	SecretVersion string
	Writer        delivery.Writer
}

//counterfeiter:generate . RepositoryCache
type RepositoryCache interface {
	Cleanup(stateStore StateStore) error
	InitRepository(logger logr.Logger, stateStore StateStore, secret corev1.Secret) (*Repository, *StateStoreError)
	GetRepositoryByTypeAndName(stateStoreType string, name string) (*Repository, error)
}

type repositoryCache struct {
	sync.Mutex
	gitRepositoryCache      map[string]*Repository
	s3RepositoryCache       map[string]*Repository
	gitMinimumFetchInterval time.Duration
}

type RepositoryCacheOption func(*repositoryCache)

func WithGitMinimumFetchInterval(interval time.Duration) RepositoryCacheOption {
	return func(c *repositoryCache) {
		c.gitMinimumFetchInterval = interval
	}
}

func NewRepositoryCache(opts ...RepositoryCacheOption) RepositoryCache {
	cache := &repositoryCache{
		gitRepositoryCache:      map[string]*Repository{},
		s3RepositoryCache:       map[string]*Repository{},
		gitMinimumFetchInterval: gitutil.DefaultMinimumFetchInterval,
	}
	for _, opt := range opts {
		opt(cache)
	}
	return cache
}

func (c *repositoryCache) InitRepository(logger logr.Logger, stateStore StateStore, secret corev1.Secret) (*Repository, *StateStoreError) {
	c.Lock()
	defer c.Unlock()

	var repo *Repository

	kind := stateStore.GetObjectKind().GroupVersionKind().Kind
	name := stateStore.GetName()
	secretVersion := secret.ResourceVersion
	var err *StateStoreError
	switch kind {

	case v1alpha1.GitStateStoreKind:
		if repository, ok := c.gitRepositoryCache[name]; ok {
			if repository.SecretVersion == secretVersion {
				return repository, nil
			}
			delete(c.gitRepositoryCache, name)
			_ = os.RemoveAll(repository.Path)
		}

		repo, err = c.initGitRepository(logger, stateStore, secret)
		if err != nil {
			return nil, err
		}
		c.gitRepositoryCache[name] = repo

	case v1alpha1.BucketStateStoreKind:
		if repository, ok := c.s3RepositoryCache[name]; ok {
			if repository.SecretVersion == secretVersion {
				return repository, nil
			}
			delete(c.s3RepositoryCache, name)
		}

		repo, err = c.initBucketRepository(logger, stateStore, secret)
		if err != nil {
			return nil, err
		}
		c.s3RepositoryCache[name] = repo

	default:
		return nil, NewInitialiseWriterError(fmt.Errorf("unknown state store type: %s", kind))
	}
	return repo, nil
}

var ErrCacheMiss = fmt.Errorf("not ready")

func (c *repositoryCache) GetRepositoryByTypeAndName(stateStoreType string, name string) (*Repository, error) {
	c.Lock()
	defer c.Unlock()
	switch stateStoreType {
	case v1alpha1.GitStateStoreKind:
		if repository, ok := c.gitRepositoryCache[name]; ok {
			return repository, nil
		}

		return nil, ErrCacheMiss
	case v1alpha1.BucketStateStoreKind:
		if repository, ok := c.s3RepositoryCache[name]; ok {
			return repository, nil
		}

		return nil, ErrCacheMiss
	default:
		return nil, NewInitialiseWriterError(fmt.Errorf("unknown state store type: %s", stateStoreType))
	}

}

func (c *repositoryCache) initGitRepository(logger logr.Logger, store StateStore, secret corev1.Secret) (*Repository, *StateStoreError) {
	stateStore := store.(*v1alpha1.GitStateStore)
	gitWriter, err := newBaseWriter(logger, store, secret.Data, writers.WithMinimumFetchInterval(c.gitMinimumFetchInterval))
	if err != nil {
		return nil, NewInitialiseWriterError(err)
	}

	repoDir, err := gitWriter.Init(stateStore.Spec.Branch)
	if err != nil {
		return nil, NewInitialiseWriterError(fmt.Errorf("unable to clone repository: %w", err))
	}

	writer, deliveryErr := writerForDelivery(store, gitWriter, secret.Data)
	if deliveryErr != nil {
		return nil, deliveryErr
	}

	repo := &Repository{
		Path:          repoDir,
		Branch:        stateStore.Spec.Branch,
		SecretVersion: secret.ResourceVersion,
		Writer:        writer,
	}
	return repo, nil
}

// newBaseWriter builds the writer the state store uses on its own, without calling Init.
func newBaseWriter(logger logr.Logger, store StateStore, creds map[string][]byte,
	gitOpts ...writers.GitWriterOption) (writers.StateStoreWriter, error) {
	switch stateStore := store.(type) {
	case *v1alpha1.GitStateStore:
		writer, err := newGitWriter(logger.WithName("writers").WithName("GitStateStoreWriter"),
			stateStore.Spec, "", creds, gitOpts...)
		if err != nil {
			return nil, fmt.Errorf("unable to create git writer: %w", err)
		}
		return writer, nil
	case *v1alpha1.BucketStateStore:
		writer, err := newS3Writer(logger.WithName("writers").WithName("S3StateStoreWriter"),
			stateStore.Spec, "", creds)
		if err != nil {
			return nil, fmt.Errorf("unable to create bucket writer: %w", err)
		}
		return writer, nil
	default:
		return nil, fmt.Errorf("unknown state store type: %T", store)
	}
}

// writerForDelivery wraps the writer the state store built for itself, which every write path uses.
func writerForDelivery(store StateStore, base writers.StateStoreWriter,
	creds map[string][]byte) (delivery.Writer, *StateStoreError) {
	mode, config := delivery.BuiltIn, map[string]string(nil)
	if deliverySpec := store.GetDelivery(); deliverySpec != nil {
		mode, config = deliverySpec.Mode, deliverySpec.Config
	}

	factory, err := delivery.For(mode)
	if err != nil {
		return nil, NewInitialiseWriterError(err)
	}

	writer, err := factory(base, config, creds)
	if err != nil {
		return nil, NewInitialiseWriterError(fmt.Errorf("delivery mode %q: %w", mode, err))
	}
	return writer, nil
}

func (c *repositoryCache) initBucketRepository(logger logr.Logger, store StateStore, secret corev1.Secret) (*Repository, *StateStoreError) {
	s3Writer, err := newBaseWriter(logger, store, secret.Data)
	if err != nil {
		return nil, NewInitialiseWriterError(err)
	}

	writer, deliveryErr := writerForDelivery(store, s3Writer, secret.Data)
	if deliveryErr != nil {
		return nil, deliveryErr
	}

	repo := &Repository{
		Writer:        writer,
		SecretVersion: secret.ResourceVersion,
	}
	return repo, nil
}

// Cleanup waits for any write in progress, then makes the repository refuse every later write.
func (c *repositoryCache) Cleanup(stateStore StateStore) error {
	repo, err := c.remove(stateStore)
	if err != nil || repo == nil {
		return err
	}

	repo.Lock()
	defer repo.Unlock()
	repo.Writer = cleanedUpWriter{}
	if repo.Path == "" {
		return nil
	}
	return os.RemoveAll(repo.Path)
}

func (c *repositoryCache) remove(stateStore StateStore) (*Repository, error) {
	c.Lock()
	defer c.Unlock()

	var entries map[string]*Repository
	switch kind := stateStore.GetObjectKind().GroupVersionKind().Kind; kind {
	case v1alpha1.GitStateStoreKind:
		entries = c.gitRepositoryCache
	case v1alpha1.BucketStateStoreKind:
		entries = c.s3RepositoryCache
	default:
		return nil, fmt.Errorf("unknown state store type: %s", kind)
	}

	repo := entries[stateStore.GetName()]
	delete(entries, stateStore.GetName())
	return repo, nil
}

var errRepositoryCleanedUp = errors.New("repository was cleaned up because its state store changed")

// cleanedUpWriter is the writer of a repository removed from the cache.
type cleanedUpWriter struct{}

func (cleanedUpWriter) Init(string) (string, error) { return "", errRepositoryCleanedUp }
func (cleanedUpWriter) Reset() error                { return errRepositoryCleanedUp }
func (cleanedUpWriter) UpdateFiles(string, string, []v1alpha1.Workload, []string) (string, error) {
	return "", errRepositoryCleanedUp
}
func (cleanedUpWriter) ReadFile(string) ([]byte, error)    { return nil, errRepositoryCleanedUp }
func (cleanedUpWriter) ValidatePermissions() error         { return errRepositoryCleanedUp }
func (cleanedUpWriter) DeleteFiles(string, []string) error { return errRepositoryCleanedUp }
func (cleanedUpWriter) Check(context.Context, string) (delivery.Result, error) {
	return delivery.Result{}, errRepositoryCleanedUp
}
func (cleanedUpWriter) Deliver(context.Context, delivery.Target, []v1alpha1.Workload, []string) (delivery.Result, error) {
	return delivery.Result{}, errRepositoryCleanedUp
}
func (cleanedUpWriter) WriteCanary(context.Context, string, []v1alpha1.Workload) (bool, error) {
	return false, errRepositoryCleanedUp
}
func (cleanedUpWriter) RemoveCanary(context.Context, string, []string) error {
	return errRepositoryCleanedUp
}
