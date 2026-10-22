package writers

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

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
	Insecure  bool

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
	Commit(message, author, email string) (sha string, err error)
	// Push passes branch to git as a refspec, i.e. "HEAD:refs/heads/<name>"
	Push(branch string, force bool) (string, error)
	Root() string
	HasChanges() (bool, error)
	ResetToRemote(branch string) error
	// TODO: merge these two methods
	RemoveDirectory(dir string) error
	RemoveFile(file string) error
}

type batchFileRemover interface {
	RemoveFiles(files ...string) error
}

type GitWriterOption func(*gitWriterConfig)

type gitWriterConfig struct {
	minimumFetchInterval time.Duration
	githubAPIURL         string
}

func WithMinimumFetchInterval(interval time.Duration) GitWriterOption {
	return func(c *gitWriterConfig) {
		c.minimumFetchInterval = interval
	}
}

func WithGitHubAPIURL(apiURL string) GitWriterOption {
	return func(c *gitWriterConfig) {
		c.githubAPIURL = apiURL
	}
}

func NewGitExecutor(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, repoPath string, creds map[string][]byte, opts ...GitWriterOption) (GitExecutor, error) {
	config := gitWriterConfig{
		minimumFetchInterval: git.DefaultMinimumFetchInterval,
	}
	for _, opt := range opts {
		opt(&config)
	}

	auth, err := git.SetAuth(stateStoreSpec, creds, git.WithGitHubAPIURL(config.githubAPIURL))
	if err != nil {
		return nil, fmt.Errorf("could not create auth method: %w", err)
	}

	nativeGitClient, err := git.NewGitClient(
		git.GitClientRequest{
			RawRepoURL: stateStoreSpec.URL,
			Root:       repoPath,
			Auth:       auth,
			Insecure:   stateStoreSpec.TLSVerificationDisabled(),
			Log:        logger,
			Opts: []git.ClientOpts{
				git.WithMinimumFetchInterval(config.minimumFetchInterval),
			},
		})
	if err != nil {
		return nil, fmt.Errorf("could not create git native client: %w", err)
	}
	return nativeGitClient, nil
}

func NewGitWriter(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string, creds map[string][]byte, opts ...GitWriterOption) (StateStoreWriter, error) {
	repoPath := strings.TrimPrefix(path.Join(
		stateStoreSpec.Path,
		destinationPath,
	), "/")

	executor, err := NewGitExecutor(logger, stateStoreSpec, repoPath, creds, opts...)
	if err != nil {
		return nil, err
	}

	gw := &GitWriter{
		// TODO: use this value for forceBasicAuth in git native client
		BasicAuth: stateStoreSpec.AuthMethod == v1alpha1.BasicAuthMethod,
		Insecure:  stateStoreSpec.TLSVerificationDisabled(),
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
		Runner: executor,
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

	dirInGitRepo := filepath.Join(g.Runner.Root(), g.Path, subDir)
	logger := g.Log.WithValues(
		"dir", dirInGitRepo,
		"branch", g.GitServer.Branch,
	)

	if err := g.Stage(subDir, workloadsToCreate, workloadsToDelete); err != nil {
		if errors.Is(err, ErrPathOutsideRepository) {
			return "", nil
		}
		return "", err
	}

	// TODO: make it a type
	action := "Delete"
	if len(workloadsToCreate) > 0 {
		action = "Update"
	}
	return g.commitAndPush(action, workPlacementName, logger)
}

// Stage puts a change in the worktree without committing it. A named subDir is replaced wholesale.
func (g *GitWriter) Stage(subDir string, workloadsToCreate []v1alpha1.Workload, workloadsToDelete []string) error {
	dirInGitRepo := filepath.Join(g.Runner.Root(), g.Path, subDir)
	logger := g.Log.WithValues("dir", dirInGitRepo, "branch", g.GitServer.Branch)

	if err := g.deleteExistingFiles(subDir != "", dirInGitRepo, workloadsToDelete, logger); err != nil {
		return err
	}
	return WriteFiles(g.Runner, dirInGitRepo, workloadsToCreate, logger)
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
		filesToRemove := make([]string, 0, len(workloadsToDelete))
		for _, file := range workloadsToDelete {
			filePath := filepath.Join(g.Runner.Root(), g.Path, file)
			log := logger.WithValues(
				"filepath", filePath,
			)
			if _, err := os.Lstat(filePath); err != nil {
				logging.Debug(log, "file requested to be deleted from worktree but does not exist")
				continue
			}
			filesToRemove = append(filesToRemove, filePath)
		}

		if len(filesToRemove) == 0 {
			return nil
		}

		if remover, ok := g.Runner.(batchFileRemover); ok {
			if err := remover.RemoveFiles(filesToRemove...); err != nil {
				logging.Error(logger, err, "could not remove files from worktree")
				return err
			}
			logging.Debug(logger, "successfully deleted files from worktree")
			return nil
		}

		for _, filePath := range filesToRemove {
			if err := g.Runner.RemoveFile(filePath); err != nil {
				logging.Error(logger, err, "could not remove file from worktree")
				return err
			}
		}
		logging.Debug(logger, "successfully deleted files from worktree")
	}
	return nil
}

var ErrPathOutsideRepository = fmt.Errorf("workload path is outside the git repository")

// WriteFiles writes each workload under dir in the clone and stages it.
func WriteFiles(runner GitExecutor, dir string, workloads []v1alpha1.Workload, logger logr.Logger) error {
	localDir := runner.Root()

	filesToAdd := make([]string, 0, len(workloads))
	for _, file := range workloads {
		log := logger.WithValues("filepath", file.Filepath)

		absoluteFilePath := filepath.Join(dir, file.Filepath)

		// filepath.Join resolves `..`, so the result must still sit inside the clone.
		if !strings.HasPrefix(absoluteFilePath, localDir) {
			logging.Warn(log,
				"path of file to write is not located within the git repository",
				"absolutePath", absoluteFilePath, "tmpDir", localDir)
			return ErrPathOutsideRepository
		}

		if err := os.MkdirAll(filepath.Dir(absoluteFilePath), 0700); err != nil {
			logging.Error(log, err, "could not generate local directories")
			return err
		}

		if err := os.WriteFile(absoluteFilePath, []byte(file.Content), 0644); err != nil {
			logging.Error(log, err, "could not write to file")
			return err
		}
		filesToAdd = append(filesToAdd, absoluteFilePath)
	}

	if len(filesToAdd) == 0 {
		return nil
	}

	if _, err := runner.Add(filesToAdd...); err != nil {
		logging.Error(logger, err, "could not add files to worktree")
		return err
	}
	return nil
}

func (g *GitWriter) ReadFile(filePath string) ([]byte, error) {
	fullPath := filepath.Join(g.Runner.Root(), g.Path, filePath)
	logger := g.Log.WithValues(
		"Path", fullPath,
		"branch", g.GitServer.Branch,
	)

	if _, err := os.Lstat(fullPath); err != nil {
		logging.Debug(logger, "could not stat file", "error", err)
		return nil, ErrFileNotFound
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		logging.Error(logger, err, "could not read file")
		return nil, err
	}
	return content, nil
}

// ValidatePermissions checks if the GitWriter has the necessary permissions to write to the repository.
// It performs a dry run validation to check authentication and branch existence without making changes.
func (g *GitWriter) ValidatePermissions() error {
	_, err := g.Runner.Push(g.GitServer.Branch, false)
	if err != nil {
		return fmt.Errorf("write permission validation failed: %w", err)
	}

	logging.Info(g.Log, "successfully validated git repository permissions")
	return nil
}

func (g *GitWriter) commitAndPush(action, workPlacementName string, logger logr.Logger) (string, error) {
	hasChanged, err := g.Runner.HasChanges()
	if err != nil {
		logging.Error(logger, err, "could not get check local changes")
		return "", err
	}
	if !hasChanged {
		logging.Info(logger, "no changes to be committed")
		return "", nil
	}

	logging.Info(logger, "pushing changes")

	commitMsg := fmt.Sprintf("%s from: %s", action, workPlacementName)
	commitSha, err := g.Runner.Commit(commitMsg, g.Author.Name, g.Author.Email)
	if err != nil {
		logging.Error(logger, err, "could not commit changes")
		return "", err
	}

	if _, err := g.Runner.Push(g.GitServer.Branch, false); err != nil {
		logging.Error(logger, err, "could not push changes")
		return "", err
	}
	return commitSha, nil
}

func (g *GitWriter) Init(branch string) (string, error) {
	return g.Runner.Clone(branch)
}

func (g *GitWriter) Reset() error {
	return g.Runner.ResetToRemote(g.GitServer.Branch)
}

func (g *GitWriter) DeleteFiles(workplacementName string, files []string) error {
	for _, file := range files {
		fullPath := filepath.Join(g.Runner.Root(), g.Path, file)
		fileInfo, err := os.Lstat(fullPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if fileInfo.IsDir() {
			if err := g.Runner.RemoveDirectory(fullPath); err != nil {
				return err
			}
			continue
		}

		if err := g.Runner.RemoveFile(fullPath); err != nil {
			return err
		}
	}

	_, err := g.commitAndPush("Delete", workplacementName, g.Log)
	return err
}

// apiHTTPTimeout matches the git client's, so an API call cannot hang longer than a clone.
const apiHTTPTimeout = 15 * time.Second

// APIHTTPClient talks to the state store host's API with the trust the git connection uses,
// including a private certificate authority under KRATIX_TLS_DATA_PATH.
func (g *GitWriter) APIHTTPClient() *http.Client {
	var host string
	if parsed, err := url.Parse(g.GitServer.URL); err == nil {
		host = parsed.Host
	}

	return &http.Client{
		Timeout: apiHTTPTimeout,
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: git.APITLSConfig(host, g.Insecure),
		},
	}
}
