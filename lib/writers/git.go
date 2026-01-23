package writers

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/util/git"
)

type GitWriter struct {
	GitServer gitServer
	Author    gitAuthor
	Path      string
	Log       logr.Logger
	BasicAuth bool

	Runner GitExecutor
}

type gitServer struct {
	URL    string
	Branch string
}

type gitAuthor struct {
	Name  string
	Email string
}

type GitExecutor interface {
	Add(files ...string) (string, error)
	Clone(branch string) (repoDir string, err error)
	CommitAndPush(branch, message, author, email string) (string, error)
	Push(branch string, force bool) (string, error)
	Root() string
	HasChanges() (bool, error)
	// TODO: merge these two methods
	RemoveDirectory(dir string) error
	RemoveFile(file string) error
}

func NewGitWriter(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string, creds map[string][]byte) (StateStoreWriter, error) {
	repoPath := strings.TrimPrefix(path.Join(
		stateStoreSpec.Path,
		destinationPath,
	), "/")

	auth, err := git.SetAuth(stateStoreSpec, creds)
	if err != nil {
		return nil, fmt.Errorf("could not create auth method: %w", err)
	}

	nativeGitClient, err := git.NewGitClient(
		git.GitClientRequest{
			RawRepoURL: stateStoreSpec.URL,
			Root:       repoPath,
			Auth:       auth,
			// NOTE: intentionally allowing insecure connections,
			// due to pre-existing setting:
			// https://github.com/syntasso/kratix/blob/59231e70b0a4a428067e3b909fd2e9dc07110997/lib/writers/git.go#L373
			Insecure: true,
			Log:      logger,
		})
	if err != nil {
		return nil, fmt.Errorf("could not create git native client: %w", err)
	}

	gw := &GitWriter{
		// TODO: use this value for forceBasicAuth in git native client
		BasicAuth: stateStoreSpec.AuthMethod == v1alpha1.BasicAuthMethod,
		GitServer: gitServer{
			URL:    stateStoreSpec.URL,
			Branch: stateStoreSpec.Branch,
		},
		Author: gitAuthor{
			Name:  stateStoreSpec.GitAuthor.Name,
			Email: stateStoreSpec.GitAuthor.Email,
		},
		Log: logger.WithValues(
			"repo", stateStoreSpec.URL,
			"branch", stateStoreSpec.Branch,
		),
		Path:   repoPath,
		Runner: nativeGitClient,
	}

	return gw, nil
}

func (g *GitWriter) UpdateFiles(subDir string, workPlacementName string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) (string, error) {
	return g.update(subDir, workPlacementName, workloadsToCreate, workloadsToDelete)
}

func (g *GitWriter) update(subDir, workPlacementName string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) (string, error) {
	if len(workloadsToCreate) == 0 && len(workloadsToDelete) == 0 && subDir == "" {
		return "", nil
	}

	localDir, err := g.Runner.Clone(g.GitServer.Branch)
	if err != nil {
		return "", err
	}

	dirInGitRepo := filepath.Join(g.Runner.Root(), g.Path, subDir)
	logger := g.Log.WithValues(
		"dir", dirInGitRepo,
		"branch", g.GitServer.Branch,
	)

	defer os.RemoveAll(filepath.Dir(localDir)) //nolint:errcheck

	err = g.deleteExistingFiles(subDir != "", dirInGitRepo, workloadsToDelete, logger)
	if err != nil {
		return "", err
	}

	for _, file := range workloadsToCreate {
		//worker-cluster/resources/<rr-namespace>/<promise-name>/<rr-name>/foo/bar/baz.yaml
		log := logger.WithValues(
			"filepath", file.Filepath,
		)

		///tmp/git-dir/worker-cluster/resources/<rr-namespace>/<promise-name>/<rr-name>/foo/bar/baz.yaml
		absoluteFilePath := filepath.Join(dirInGitRepo, file.Filepath)

		//We need to protect against paths containing `..`
		//filepath.Join expands any '../' in the Path to the actual, e.g. /tmp/foo/../ resolves to /tmp/
		//To ensure they can't write to files on disk outside the tmp git repository we check the absolute Path
		//returned by `filepath.Join` is still contained with the git repository:
		// Note: This means `../` can still be used, but only if the end result is still contained within the git repository
		if !strings.HasPrefix(absoluteFilePath, localDir) {
			logging.Warn(log,
				"path of file to write is not located within the git repository",
				"absolutePath",
				absoluteFilePath, "tmpDir", localDir)
			return "", nil //We don't want to retry as this isn't a recoverable error. Log error and return nil.
		}

		if err := os.MkdirAll(filepath.Dir(absoluteFilePath), 0700); err != nil {
			logging.Error(log, err, "could not generate local directories")
			return "", err
		}

		if err := os.WriteFile(absoluteFilePath, []byte(file.Content), 0644); err != nil {
			logging.Error(log, err, "could not write to file")
			return "", err
		}

		if _, err := g.Runner.Add(absoluteFilePath); err != nil {
			logging.Error(log, err, "could not add file to worktree")
			return "", err
		}
	}

	// TODO: make it a type
	action := "Delete"
	if len(workloadsToCreate) > 0 {
		action = "Update"
	}
	return g.commitAndPush(action, workPlacementName, logger)
}

// deleteExistingFiles removes all files in dir when removeDirectory is set to true
// else it removes files listed in workloadsToDelete
func (g *GitWriter) deleteExistingFiles(removeDirectory bool, dir string, workloadsToDelete []string, logger logr.Logger) error {

	if removeDirectory {
		if _, err := os.Lstat(dir); err == nil {
			logging.Info(logger, "deleting existing content")
			if err := g.Runner.RemoveDirectory(dir); err != nil {
				logging.Error(logger, err, "could not add directory deletion to worktree", "dir", dir)
				return err
			}
		}
	} else {
		for _, file := range workloadsToDelete {
			filePath := filepath.Join(g.Runner.Root(), file)
			log := logger.WithValues(
				"filepath", filePath,
			)
			if _, err := os.Lstat(filePath); err != nil {
				logging.Debug(log, "file requested to be deleted from worktree but does not exist")
				continue
			}
			if err := g.Runner.RemoveFile(file); err != nil {
				logging.Error(logger, err, "could not remove file from worktree")
				return err
			}
			logging.Debug(logger, "successfully deleted file from worktree")
		}
	}
	return nil
}

func (g *GitWriter) ReadFile(filePath string) ([]byte, error) {

	localDir, err := g.Runner.Clone(g.GitServer.Branch)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(localDir)) //nolint:errcheck

	fullPath := filepath.Join(g.Runner.Root(), filePath)
	logger := g.Log.WithValues(
		"Path", fullPath,
		"branch", g.GitServer.Branch,
	)

	if _, err := os.Lstat(fullPath); err != nil {
		logging.Debug(logger, "could not stat file", "error", err)
		return nil, ErrFileNotFound
	}

	var content []byte
	if content, err = os.ReadFile(fullPath); err != nil {
		logging.Error(logger, err, "could not read file")
		return nil, err
	}
	return content, nil
}

// ValidatePermissions checks if the GitWriter has the necessary permissions to write to the repository.
// It performs a dry run validation to check authentication and branch existence without making changes.
func (g *GitWriter) ValidatePermissions() error {
	// Setup local directory with repo (this already checks if we can clone - read access)
	localDir, cloneErr := g.Runner.Clone(g.GitServer.Branch)
	if cloneErr != nil && !errors.Is(cloneErr, ErrAuthSucceededAfterTrim) {
		return fmt.Errorf("failed to set up local directory with repo: %w", cloneErr)
	}
	defer os.RemoveAll(localDir) //nolint:errcheck

	_, err := g.Runner.Push(g.GitServer.Branch, false)
	if err != nil {
		return fmt.Errorf("write permission validation failed: %w", err)
	}

	logging.Info(g.Log, "successfully validated git repository permissions")
	return cloneErr
}

func (g *GitWriter) commitAndPush(action, workPlacementName string, logger logr.Logger) (string, error) {
	hasChanged, err := g.Runner.HasChanges()
	if err != nil {
		logging.Error(logger, err, "could not get check local changes")
		return "", err
	}
	if action != "Delete" && !hasChanged {
		logging.Info(logger, "no changes to be committed")
		return "", git.ErrNoFilesChanged
	}

	logging.Info(logger, "pushing changes")

	// Run a commit with author and message
	commitMsg := fmt.Sprintf("%s from: %s", action, workPlacementName)
	commitSha, err := g.Runner.CommitAndPush(g.GitServer.Branch, commitMsg, g.Author.Name, g.Author.Email)
	if err != nil {
		logging.Error(logger, err, "could not push changes")
		return "", err
	}
	return commitSha, nil
}
