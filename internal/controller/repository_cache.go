package controller

import (
	"fmt"
	"sync"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
)

type Repository struct {
	sync.Mutex
	Path   string
	Branch string

	Writer writers.StateStoreWriter
}

type RepositoryCache struct {
	sync.Mutex
	gitRepositoryCache map[string]*Repository
	s3RepositoryCache  map[string]*Repository
}

func NewRepositoryCache() *RepositoryCache {
	return &RepositoryCache{
		gitRepositoryCache: map[string]*Repository{},
		s3RepositoryCache:  map[string]*Repository{},
	}
}

func (c *RepositoryCache) InitRepository(ctx *stateStoreReconcileContext) (*Repository, *StateStoreError) {
	c.Lock()
	defer c.Unlock()

	if repository, ok := c.gitRepositoryCache[ctx.stateStore.GetName()]; ok {
		return repository, nil
	}
	var repo *Repository

	kind := ctx.stateStore.GetObjectKind().GroupVersionKind().Kind
	var err *StateStoreError
	switch kind {

	case "GitStateStore":
		repo, err = c.initGitRepository(ctx)
		if err != nil {
			return nil, err
		}

		c.gitRepositoryCache[ctx.stateStore.GetName()] = repo

	case "BucketStateStore":
		repo, err = c.initBucketRepository(ctx)
		if err != nil {
			return nil, err
		}

		c.s3RepositoryCache[ctx.stateStore.GetName()] = repo

	default:
		return nil, NewInitialiseWriterError(fmt.Errorf("unknown state store type: %s", kind))
	}
	return repo, nil
}

func (c *RepositoryCache) GetRepositoryByTypeAndName(stateStoreType string, name string) (*Repository, error) {
	c.Lock()
	defer c.Unlock()
	switch stateStoreType {
	case "GitStateStore":
		if repository, ok := c.gitRepositoryCache[name]; ok {
			return repository, nil
		}

		return nil, CacheMissError
	case "BucketStateStore":
		if repository, ok := c.s3RepositoryCache[name]; ok {
			return repository, nil
		}

		return nil, CacheMissError
	default:
		return nil, NewInitialiseWriterError(fmt.Errorf("unknown state store type: %s", stateStoreType))
	}

}

func (c *RepositoryCache) initGitRepository(ctx *stateStoreReconcileContext) (*Repository, *StateStoreError) {
	stateStore := ctx.stateStore.(*v1alpha1.GitStateStore)
	gitWriter, err := newGitWriter(
		ctx.logger.WithName("writers").WithName("GitStateStoreWriter"),
		stateStore.Spec,
		"",
		ctx.stateStoreSecret.Data,
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

func (c *RepositoryCache) initBucketRepository(ctx *stateStoreReconcileContext) (*Repository, *StateStoreError) {
	stateStore := ctx.stateStore.(*v1alpha1.BucketStateStore)
	s3Writer, err := newS3Writer(
		ctx.logger.WithName("writers").WithName("S3StateStoreWriter"),
		stateStore.Spec,
		"",
		ctx.stateStoreSecret.Data,
	)

	if err != nil {
		return nil, NewInitialiseWriterError(fmt.Errorf("unable to create bucket writer: %w", err))
	}

	repo := &Repository{
		Writer: s3Writer,
	}
	return repo, nil
}

var CacheMissError = fmt.Errorf("cache miss")
