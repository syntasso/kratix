package writers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/go-git/go-git/v5"
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
	*nativeGitClient
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
type Auth struct {
	transport.AuthMethod
	Creds
}

func NewGitWriter(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string, creds map[string][]byte) (StateStoreWriter, error) {

	repoPath := strings.TrimPrefix(path.Join(
		stateStoreSpec.Path,
		destinationPath,
	), "/")

	auth, err := setAuth(stateStoreSpec, destinationPath, creds)
	if err != nil {
		return nil, fmt.Errorf("could not create auth method: %w", err)
	}

	nativeGitClient, err := NewGitClient(
		GitClientRequest{
			RawRepoURL: stateStoreSpec.URL,
			Root:       repoPath,
			Auth:       auth,
			// NOTE: intentionally not allowing insecure connections
			Insecure: false,
			Proxy:    "",
			NoProxy:  "",
			log:      logger,
		})
	if err != nil {
		return nil, fmt.Errorf("could not create git native client: %w", err)
	}

	m := &GitWriter{
		// TODO: use this value for forceBasicAuth in git native client
		BasicAuth: stateStoreSpec.AuthMethod == v1alpha1.BasicAuthMethod,
		GitServer: gitServer{
			URL:    stateStoreSpec.URL,
			Branch: stateStoreSpec.Branch,
			Auth:   auth.AuthMethod,
		},
		Author: gitAuthor{
			Name:  stateStoreSpec.GitAuthor.Name,
			Email: stateStoreSpec.GitAuthor.Email,
		},
		Log: logger.WithValues(
			"repo", stateStoreSpec.URL,
			"branch", stateStoreSpec.Branch,
		),
		Path:            repoPath,
		nativeGitClient: nativeGitClient,
	}

	return m, nil
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

	localDir, err := g.setupLocalDirectoryWithRepo()
	if err != nil {
		return "", err
	}

	defer os.RemoveAll(filepath.Dir(localDir)) //nolint:errcheck

	err = g.deleteExistingFiles(subDir != "", dirInGitRepo, workloadsToDelete, logger)
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
		absoluteFilePath := filepath.Join(localDir, worktreeFilePath)

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

		if _, err := g.Add(worktreeFilePath); err != nil {
			logging.Error(log, err, "could not add file to worktree")
			return "", err
		}
	}

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
			if err := g.nativeGitClient.RemoveDirectory(dir); err != nil {
				logging.Error(logger, err, "could not add directory deletion to worktree", "dir", dir)
				return err
			}
		}
	} else {
		for _, file := range workloadsToDelete {
			filePath := filepath.Join(dir, file)
			log := logger.WithValues(
				"filepath", filePath,
			)
			if _, err := os.Lstat(filePath); err != nil {
				logging.Debug(log, "file requested to be deleted from worktree but does not exist")
				continue
			}
			if err := g.nativeGitClient.RemoveFile(file); err != nil {
				logging.Error(logger, err, "could not remove file from worktree")
				return err
			}
			logging.Debug(logger, "successfully deleted file from worktree")
		}
	}
	return nil
}

func (g *GitWriter) ReadFile(filePath string) ([]byte, error) {

	localDir, err := g.setupLocalDirectoryWithRepo()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(localDir)) //nolint:errcheck

	fullPath := filepath.Join(g.Path, filePath)
	logger := g.Log.WithValues(
		"Path", fullPath,
		"branch", g.GitServer.Branch,
	)

	path := filepath.Join(localDir, fullPath)

	if _, err := os.Lstat(path); err != nil {
		logging.Debug(logger, "could not stat file", "error", err)
		return nil, ErrFileNotFound
	}

	var content []byte
	if content, err = os.ReadFile(path); err != nil {
		logging.Error(logger, err, "could not read file")
		return nil, err
	}
	return content, nil
}

// Initialise a local directory with the Git repository and returns
// localDir that should be cleaned up after use.
func (g *GitWriter) setupLocalDirectoryWithRepo() (string, error) {
	localDir, err := g.Clone(g.GitServer.Branch)
	if err != nil {
		return "", fmt.Errorf("could not clone: %w", err)
	}

	return localDir, nil
}

func (g *GitWriter) push() error {
	_, err := g.Push("main")
	if err != nil {
		return fmt.Errorf("could not push changes: %w", err)
	}

	return nil
}

// validatePush attempts to validate write permissions by pushing no changes to the remote
// If the push doesn't return an error, it means we can write.
func (g *GitWriter) validatePush(logger logr.Logger) error {
	_, err := g.Push(g.GitServer.Branch)
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
	localDir, cloneErr := g.setupLocalDirectoryWithRepo()
	if cloneErr != nil && !errors.Is(cloneErr, ErrAuthSucceededAfterTrim) {
		return fmt.Errorf("failed to set up local directory with repo: %w", cloneErr)
	}
	defer os.RemoveAll(localDir) //nolint:errcheck

	if err := g.validatePush(g.Log); err != nil {
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

// HasChanges returns whether there are pending changes
// on the repository.
func (m *nativeGitClient) HasChanges() (bool, error) {
	out, err := m.runCmd(context.Background(), "status")
	if err != nil {
		return false, fmt.Errorf("failed to diff: %w", err)
	}
	if out == "" {
		return false, nil
	}

	return strings.Contains(out, "working tree clean"), nil
}

func (g *GitWriter) commitAndPush(action, workPlacementName string, logger logr.Logger) (string, error) {
	hasChanged, err := g.nativeGitClient.HasChanges()
	if err != nil {
		logging.Error(logger, err, "could not get check local changes")
		return "", err
	}
	if !hasChanged {
		logging.Info(logger, "no changes to be committed")
		return "", err
	}

	logging.Info(logger, "pushing changes")

	// Run a commit with author and message
	commitMsg := fmt.Sprintf("%s from:::::::: %s", action, workPlacementName)
	author := fmt.Sprintf("%s <%s>", g.Author.Name, g.Author.Email)
	commitSha, err := g.CommitAndPush(g.GitServer.Branch, commitMsg, author)
	if err != nil {
		logging.Error(logger, err, "could not push changes")
		return "", err
	}
	return commitSha, nil
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

// Fetch downloads commits, branches, and tags from the remote repository
// without modifying the working directory or current branch. Updates remote-tracking
// branches (e.g., origin/main) to reflect the current state of the remote.
//
// Parameters:
//
//	revision: Specific branch/tag/commit to 	fetch (empty string fetches all)
//	depth: Number of commits to fetch (0 for full history)
//
// Flags used:
//
//	--force: Allow non-fast-forward updates (handles force pushes)
//	--prune: Remove remote-tracking branches that no longer exist on remote
//	--tags: Fetch all tags (only when depth == 0, as tags don't work well with shallow clones)
func (m *nativeGitClient) Fetch(revision string, depth int64) error {
	// TODO: revisit handlers
	if m.OnFetch != nil {
		done := m.OnFetch(m.repoURL)
		defer done()
	}
	ctx := context.Background()

	err := m.fetch(ctx, revision, depth)
	if err != nil {
		return err
	}

	return err
}

// Checkout switches the working directory to the specified revision (branch, tag, or commit),
// updating all files to match that revision's state. Changes the current branch (updates
// .git/HEAD) and modifies working directory files. After checkout, performs aggressive
// cleanup to remove all untracked files, directories, and nested repositories.
//
// Parameters:
//
//	revision: Branch, tag, or commit to checkout (empty string or "HEAD" defaults to "origin/HEAD")
//
// Behavior:
//   - Uses --force flag to discard any local modifications
//   - Runs git clean -ffdx after checkout to remove:
//   - Untracked files and directories (first "f")
//   - Untracked nested Git repositories like submodules (second "f")
//   - Ignored files from .gitignore ("x")
//   - All untracked directories ("d")
//
// Returns:
//   - Empty string on success
//   - Command output on error, along with the error itself
func (m *nativeGitClient) Checkout(revision string) (string, error) {
	if revision == "" || revision == "HEAD" {
		revision = "origin/HEAD"
	}
	ctx := context.Background()
	if out, err := m.runCmd(ctx, "checkout", "--force", revision); err != nil {
		return out, fmt.Errorf("failed to checkout %s: %w", revision, err)
	}
	// NOTE
	// The double “f” in the arguments is not a typo: the first “f” tells
	// `git clean` to delete untracked files and directories, and the second “f”
	// tells it to clean untracked nested Git repositories (for example a
	// submodule which has since been removed).
	if out, err := m.runCmd(ctx, "clean", "-ffdx"); err != nil {
		return out, fmt.Errorf("failed to clean: %w", err)
	}
	return "", nil
}

// Clone creates a new Git repository by downloading the entire repository
// from a remote URL. Creates the .git directory, downloads all commits, branches,
// and tags, sets up remote tracking, and checks out a desired branch. This is
// a one-time setup operation for getting a repository for the first time.
func (m *nativeGitClient) Clone(branch string) (string, error) {
	logging.Debug(m.log, "cloning repo")
	localDir, err := m.Init()
	if err != nil {
		return "", fmt.Errorf("could not run init: %w", err)
	}
	err = m.Fetch(branch, 0)
	if err != nil {
		return "", fmt.Errorf("could not run fetch: %w", err)
	}
	out, err := m.Checkout(branch)
	if err != nil {
		logging.Error(m.log, err, "could not clone repo: %v", out)
		return "", fmt.Errorf("could not run checkout: %w", err)
	}

	return localDir, nil
}

// Add files to the repository
func (m *nativeGitClient) Add(files ...string) (string, error) {
	ctx := context.Background()
	args := append([]string{"add"}, files...)
	out, err := m.runCmd(ctx, args...)
	if err != nil {
		return out, fmt.Errorf("failed to add files: %w", err)
	}
	return out, nil
}
