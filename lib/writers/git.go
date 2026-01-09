package writers

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/davecgh/go-spew/spew"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
)

type GitWriter struct {
	GitServer gitServer
	Author    gitAuthor
	Path      string
	Log       logr.Logger
	BasicAuth bool
	client    GitClient
}

type gitServer struct {
	URL    string
	Branch string
	Auth   transport.AuthMethod
}

type gitAuthor struct {
	Name  string
	Email string
}

type GitRepo struct {
	LocalTmpDir string
	Repo        *git.Repository
	Worktree    *git.Worktree
}

// TODO: rename
type authx struct {
	transport.AuthMethod
	Creds
}

func NewGitWriter(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string, creds map[string][]byte) (StateStoreWriter, error) {

	repoPath := strings.TrimPrefix(path.Join(
		stateStoreSpec.Path,
		destinationPath,
	), "/")

	authMethod, err := setAuth(stateStoreSpec, destinationPath, creds)
	if err != nil {
		return nil, fmt.Errorf("could not create auth method: %w", err)
	}

	nativeGitClient, err := NewGitClient(
		GitClientRequest{
			RawRepoURL: stateStoreSpec.URL,
			Root:       repoPath,
			Creds:      authMethod,
			// NOTE: intentionally not allowing insecure connections
			Insecure: false,
			Proxy:    "",
			NoProxy:  "",
		})

	return &GitWriter{
		client: nativeGitClient,
		// TODO: use this value for forceBasicAuth in git native client
		BasicAuth: stateStoreSpec.AuthMethod == v1alpha1.BasicAuthMethod,
		GitServer: gitServer{
			URL:    stateStoreSpec.URL,
			Branch: stateStoreSpec.Branch,
			Auth:   authMethod,
		},
		Author: gitAuthor{
			Name:  stateStoreSpec.GitAuthor.Name,
			Email: stateStoreSpec.GitAuthor.Email,
		},
		Log: logger.WithValues(
			"repo", stateStoreSpec.URL,
			"branch", stateStoreSpec.Branch,
		),
		Path: repoPath,
	}, nil
}

func (g *GitWriter) UpdateFiles(subDir string, workPlacementName string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) (string, error) {
	return g.update(subDir, workPlacementName, workloadsToCreate, workloadsToDelete)
}

func (g *GitWriter) update(subDir, workPlacementName string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) (string, error) {
	if len(workloadsToCreate) == 0 && len(workloadsToDelete) == 0 && subDir == "" {
		return "", nil
	}

	dirInGitRepo := filepath.Join(g.Path, subDir)
	logger := g.Log.WithValues(
		"dir", dirInGitRepo,
		"branch", g.GitServer.Branch,
	)

	gr, err := g.setupLocalDirectoryWithRepo(logger)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(filepath.Dir(gr.LocalTmpDir)) //nolint:errcheck

	err = g.deleteExistingFiles(subDir != "", dirInGitRepo, workloadsToDelete, gr.Worktree, logger)
	if err != nil {
		return "", err
	}

	for _, file := range workloadsToCreate {
		//worker-cluster/resources/<rr-namespace>/<promise-name>/<rr-name>/foo/bar/baz.yaml
		worktreeFilePath := filepath.Join(dirInGitRepo, file.Filepath)
		log := logger.WithValues(
			"filepath", worktreeFilePath,
		)

		///tmp/git-dir/worker-cluster/resources/<rr-namespace>/<promise-name>/<rr-name>/foo/bar/baz.yaml
		absoluteFilePath := filepath.Join(gr.LocalTmpDir, worktreeFilePath)

		//We need to protect against paths containing `..`
		//filepath.Join expands any '../' in the Path to the actual, e.g. /tmp/foo/../ resolves to /tmp/
		//To ensure they can't write to files on disk outside the tmp git repository we check the absolute Path
		//returned by `filepath.Join` is still contained with the git repository:
		// Note: This means `../` can still be used, but only if the end result is still contained within the git repository
		if !strings.HasPrefix(absoluteFilePath, gr.LocalTmpDir) {
			logging.Warn(log, "path of file to write is not located within the git repository", "absolutePath", absoluteFilePath, "tmpDir", gr.LocalTmpDir)
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

		if _, err := gr.Worktree.Add(worktreeFilePath); err != nil {
			logging.Error(log, err, "could not add file to worktree")
			return "", err
		}
	}

	action := "Delete"
	if len(workloadsToCreate) > 0 {
		action = "Update"
	}
	return g.commitAndPush(gr.Repo, gr.Worktree, action, workPlacementName, logger)
}

// deleteExistingFiles removes all files in dir when removeDirectory is set to true
// else it removes files listed in workloadsToDelete
func (g *GitWriter) deleteExistingFiles(removeDirectory bool, dir string, workloadsToDelete []string, worktree *git.Worktree, logger logr.Logger) error {
	if removeDirectory {
		if _, err := worktree.Filesystem.Lstat(dir); err == nil {
			logging.Info(logger, "deleting existing content")
			if _, err := worktree.Remove(dir); err != nil {
				logging.Error(logger, err, "could not add directory deletion to worktree", "dir", dir)
				return err
			}
		}
	} else {
		for _, file := range workloadsToDelete {
			worktreeFilePath := filepath.Join(dir, file)
			log := logger.WithValues(
				"filepath", worktreeFilePath,
			)
			if _, err := worktree.Filesystem.Lstat(worktreeFilePath); err != nil {
				logging.Debug(log, "file requested to be deleted from worktree but does not exist")
				continue
			}
			if _, err := worktree.Remove(worktreeFilePath); err != nil {
				logging.Error(logger, err, "could not remove file from worktree")
				return err
			}
			logging.Debug(logger, "successfully deleted file from worktree")
		}
	}
	return nil
}

func (g *GitWriter) ReadFile(filePath string) ([]byte, error) {
	fullPath := filepath.Join(g.Path, filePath)
	logger := g.Log.WithValues(
		"Path", fullPath,
		"branch", g.GitServer.Branch,
	)

	gr, err := g.setupLocalDirectoryWithRepo(logger)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(gr.LocalTmpDir)) //nolint:errcheck

	if _, err := gr.Worktree.Filesystem.Lstat(fullPath); err != nil {
		logging.Debug(logger, "could not stat file", "error", err)
		return nil, ErrFileNotFound
	}

	var content []byte
	if content, err = os.ReadFile(filepath.Join(gr.LocalTmpDir, fullPath)); err != nil {
		logging.Error(logger, err, "could not read file")
		return nil, err
	}
	return content, nil
}

func (g *GitWriter) setupLocalDirectoryWithRepo(logger logr.Logger) (*GitRepo, error) {

	var err error
	gr := &GitRepo{}

	gr.Repo, err = g.client.Clone()
	if err != nil && !errors.Is(err, ErrAuthSucceededAfterTrim) {
		return nil, fmt.Errorf("could not clone: %w", err)
	}
	if gr.Repo == nil {
		return nil, fmt.Errorf("clone returned nil repository")
	}

	gr.Worktree, err = gr.Repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("could not create worktree: %w", err)
	}
	gr.LocalTmpDir = g.client.Root()

	return gr, nil
}

func (g *GitWriter) push(repo *git.Repository, logger logr.Logger) error {
	operation := func() (error, bool) {
		err := repo.Push(&git.PushOptions{
			RemoteName:      "origin",
			Auth:            g.GitServer.Auth,
			InsecureSkipTLS: true,
		})
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			return nil, false
		}
		if isAuthError(err) {
			return err, false
		}
		return err, true
	}
	/*
		client, err := NewClientExt("https://github.com/argoproj/argo-cd.git", dir, NopCreds{}, false, false, "", "")
			require.NoError(t, err)

			err = client.Init()
			require.NoError(t, err)
	*/

	if err := retryGitOperation(logger, "push", operation); err != nil {
		logging.Error(logger, err, "could not push to remotexxxxxxxxxxxxxxxxxxxx")
		return err
	}

	return nil
}

func retryGitOperation(logger logr.Logger, operation string, fn func() (error, bool)) error {
	err, retry := fn()
	if err == nil {
		return nil
	}

	if !retry {
		return err
	}

	logging.Error(logger, err, fmt.Sprintf("git %s failed; retrying once", operation))
	time.Sleep(1 * time.Second)

	if retryErr, _ := fn(); retryErr != nil {
		return retryErr
	}

	logging.Info(logger, fmt.Sprintf("git %s succeeded on retry", operation))
	return nil
}

// validatePush attempts to validate write permissions by pushing no changes to the remote
// If the push errors with "NoErrAlreadyUpToDate", it means we can write.
func (g *GitWriter) validatePush(repo *git.Repository, logger logr.Logger) error {

	_, err := g.client.Push(g.GitServer.Branch)
	/*
		err := repo.Push(&git.PushOptions{
			RemoteName: "origin",
			Auth:            g.GitServer.Auth,
			InsecureSkipTLS: true,
		})
	*/
	fmt.Printf("auth::: %s\n", spew.Sdump(g.GitServer))
	fmt.Printf("---auth::: %s\n", spew.Sdump(g.GitServer.Auth))
	fmt.Printf("eeeeeeeeeeeeerrrrrRRR::: %v\n", err)

	if err != nil {
		return fmt.Errorf("write permission validation failed: %w", err)
	}
	logging.Info(logger, "push validation successful - repository is up-to-date")

	return nil
}

// ValidatePermissions checks if the GitWriter has the necessary permissions to write to the repository.
// It performs a dry run validation to check authentication and branch existence without making changes.
func (g *GitWriter) ValidatePermissions() error {
	// Setup local directory with repo (this already checks if we can clone - read access)
	gr, cloneErr := g.setupLocalDirectoryWithRepo(g.Log)
	if cloneErr != nil && !errors.Is(cloneErr, ErrAuthSucceededAfterTrim) {
		return fmt.Errorf("failed to set up local directory with repo: %w", cloneErr)
	}
	defer os.RemoveAll(gr.LocalTmpDir) //nolint:errcheck

	if err := g.validatePush(gr.Repo, g.Log); err != nil {
		return err
	}

	logging.Info(g.Log, "successfully validated git repository permissions")
	return cloneErr
}

func (g *GitWriter) cloneRepo(localRepoFilePath string, logger logr.Logger) (*git.Repository, error) {

	logging.Debug(logger, "cloning repo")
	/* TODO: make sure we have the same settings in the new client
	cloneOpts := &git.CloneOptions{
		Auth:            g.GitServer.Auth,
		URL:             g.GitServer.URL,
		ReferenceName:   plumbing.NewBranchReferenceName(g.GitServer.Branch),
		SingleBranch:    true,
		Depth:           1,
		NoCheckout:      false,
		InsecureSkipTLS: true,
	}
	*/

	//	repo, err := git.PlainClone(, false, cloneOpts)

	repo, err := git.PlainOpen(localRepoFilePath)

	if isAuthError(err) && g.BasicAuth {
		/* TODO: convert this
		if trimmed, changed := trimmedBasicAuthCopy(g.GitServer.Auth); changed {
			logging.Info(logger, "auth failed there are trailing spaces in credentials; will retry again with trimmed credentials")
			cloneOpts.Auth = &trimmed
			_ = os.RemoveAll(localRepoFilePath)
			if retryRepo, retryErr := git.PlainClone(localRepoFilePath, false, cloneOpts); retryErr == nil {
				logging.Warn(logger, "authentication succeeded after trimming trailing whitespace; please fix your GitStateStore Secret")
				g.GitServer.Auth = &trimmed
				return retryRepo, ErrAuthSucceededAfterTrim
			}
		}
		*/
	}

	return repo, err
}

func (g *GitWriter) commitAndPush(repo *git.Repository, worktree *git.Worktree, action, workPlacementName string, logger logr.Logger) (string, error) {
	var sha string
	operation := func() (error, bool) {
		status, err := worktree.Status()
		if err != nil {
			logging.Error(logger, err, "could not get worktree status")
			return err, true
		}

		if status.IsClean() {
			logging.Info(logger, "no changes to be committed")
			return nil, false
		}

		commitHash, err := worktree.Commit(fmt.Sprintf("%s from: %s", action, workPlacementName), &git.CommitOptions{
			Author: &object.Signature{
				Name:  g.Author.Name,
				Email: g.Author.Email,
				When:  time.Now(),
			},
		})

		if !commitHash.IsZero() {
			sha = commitHash.String()
		}

		if err != nil {
			return err, true
		}
		return nil, false
	}

	if err := retryGitOperation(logger, "commit", operation); err != nil {
		logging.Error(logger, err, "could not commit file to worktree")
		return "", err
	}

	logging.Info(logger, "pushing changes")
	/*
		iface.Push() {
			if enabled:
			  use new client from argo
			else:
			  use old one:
			  g.push
		}
	*/

	if err := g.push(repo, logger); err != nil {
		logging.Error(logger, err, "could not push changesyyyyyyyyyyyyyyyyyyyyyyyy")
		return "", err
	}
	return sha, nil
}

func createLocalDirectory(logger logr.Logger) (string, error) {
	logging.Debug(logger, "creating local directory")
	dir, err := os.MkdirTemp("", "kratix-repo")
	if err != nil {
		return "", err
	}

	return dir, nil
}

func trimRightWhitespace(s string) (string, bool) {
	trimmed := strings.TrimRightFunc(s, unicode.IsSpace)
	return trimmed, trimmed != s
}
