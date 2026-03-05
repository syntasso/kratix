package controller

import (
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	corev1 "k8s.io/api/core/v1"
)

type Repository struct {
	sync.Mutex
	Path   string
	Branch string

	Writer writers.StateStoreWriter
}

//counterfeiter:generate . RepositoryCache
type RepositoryCache interface {
	InitRepository(logger logr.Logger, stateStore StateStore, secret *corev1.Secret) (*Repository, *StateStoreError)
	GetRepositoryByTypeAndName(stateStoreType string, name string) (*Repository, error)
}

type repositoryCache struct {
	sync.Mutex
	gitRepositoryCache map[string]*Repository
	s3RepositoryCache  map[string]*Repository
}

func NewRepositoryCache() RepositoryCache {
	return &repositoryCache{
		gitRepositoryCache: map[string]*Repository{},
		s3RepositoryCache:  map[string]*Repository{},
	}
}

func (c *repositoryCache) InitRepository(logger logr.Logger, stateStore StateStore, secret *corev1.Secret) (*Repository, *StateStoreError) {
	c.Lock()
	defer c.Unlock()

	if repository, ok := c.gitRepositoryCache[stateStore.GetName()]; ok {
		return repository, nil
	}
	var repo *Repository

	kind := stateStore.GetObjectKind().GroupVersionKind().Kind
	var err *StateStoreError
	switch kind {

	case "GitStateStore":
		repo, err = c.initGitRepository(logger, stateStore, secret)
		if err != nil {
			return nil, err
		}

		c.gitRepositoryCache[stateStore.GetName()] = repo

	case "BucketStateStore":
		repo, err = c.initBucketRepository(logger, stateStore, secret)
		if err != nil {
			return nil, err
		}

		c.s3RepositoryCache[stateStore.GetName()] = repo

	default:
		return nil, NewInitialiseWriterError(fmt.Errorf("unknown state store type: %s", kind))
	}
	return repo, nil
}

func (c *repositoryCache) GetRepositoryByTypeAndName(stateStoreType string, name string) (*Repository, error) {
	c.Lock()
	defer c.Unlock()
	switch stateStoreType {
	case "GitStateStore":
		if repository, ok := c.gitRepositoryCache[name]; ok {
			return repository, nil
		}

		return nil, ErrCacheMiss
	case "BucketStateStore":
		if repository, ok := c.s3RepositoryCache[name]; ok {
			return repository, nil
		}

		return nil, ErrCacheMiss
	default:
		return nil, NewInitialiseWriterError(fmt.Errorf("unknown state store type: %s", stateStoreType))
	}

}

func (c *repositoryCache) initGitRepository(logger logr.Logger, store StateStore, secret *corev1.Secret) (*Repository, *StateStoreError) {
	stateStore := store.(*v1alpha1.GitStateStore)
	gitWriter, err := newGitWriter(
		logger.WithName("writers").WithName("GitStateStoreWriter"),
		stateStore.Spec,
		"",
		secret.Data,
	)
	if err != nil {
		return nil, NewInitialiseWriterError(fmt.Errorf("unable to create git writer: %w", err))
	}

	repoDir, err := gitWriter.Init(stateStore.Spec.Branch)
	if err != nil {
		return nil, NewInitialiseWriterError(fmt.Errorf("unable to clone repository: %w", err))
	}

	repo := &Repository{
		Path:   repoDir,
		Branch: stateStore.Spec.Branch,
		Writer: gitWriter,
	}
	return repo, nil
}

func (c *repositoryCache) initBucketRepository(logger logr.Logger, store StateStore, secret *corev1.Secret) (*Repository, *StateStoreError) {
	stateStore := store.(*v1alpha1.BucketStateStore)
	s3Writer, err := newS3Writer(
		logger.WithName("writers").WithName("S3StateStoreWriter"),
		stateStore.Spec,
		"",
		secret.Data,
	)

	if err != nil {
		return nil, NewInitialiseWriterError(fmt.Errorf("unable to create bucket writer: %w", err))
	}

	repo := &Repository{
		Writer: s3Writer,
	}
	return repo, nil
}

var ErrCacheMiss = fmt.Errorf("cache miss")
