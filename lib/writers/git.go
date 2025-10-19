package writers

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-logr/logr"
	"github.com/golang-jwt/jwt/v5"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
)

type GitWriter struct {
	GitServer gitServer
	Author    gitAuthor
	Path      string
	Log       logr.Logger
	BasicAuth bool
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

type sshAuthCreds struct {
	SSHPrivateKey []byte
	KnownHosts    []byte
	SSHUser       string
}

type basicAuthCreds struct {
	Username string
	Password string
}

type githubAppCreds struct {
	AppID          string
	InstallationID string
	PrivateKey     string
	ApiUrl         string
}

func NewGitWriter(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string, creds map[string][]byte) (StateStoreWriter, error) {
	var authMethod transport.AuthMethod
	switch stateStoreSpec.AuthMethod {
	case v1alpha1.SSHAuthMethod:
		sshCreds, err := newSSHAuthCreds(stateStoreSpec, creds)
		if err != nil {
			return nil, err
		}

		sshKey, err := ssh.NewPublicKeys(sshCreds.SSHUser, sshCreds.SSHPrivateKey, "")
		if err != nil {
			return nil, fmt.Errorf("error parsing sshKey: %w", err)
		}

		knownHostsFile, err := os.CreateTemp("", "knownHosts")
		if err != nil {
			return nil, fmt.Errorf("error creating knownHosts file: %w", err)
		}

		_, err = knownHostsFile.Write(sshCreds.KnownHosts)
		if err != nil {
			return nil, fmt.Errorf("error writing knownHosts file: %w", err)
		}

		knownHostsCallback, err := ssh.NewKnownHostsCallback(knownHostsFile.Name())
		if err != nil {
			return nil, fmt.Errorf("error parsing known hosts: %w", err)
		}

		sshKey.HostKeyCallback = knownHostsCallback
		err = os.Remove(knownHostsFile.Name())
		if err != nil {
			return nil, fmt.Errorf("error removing knownHosts file: %w", err)
		}

		authMethod = sshKey
	case v1alpha1.BasicAuthMethod:
		basicCreds, err := newBasicAuthCreds(stateStoreSpec, creds)
		if err != nil {
			return nil, err
		}

		authMethod = &githttp.BasicAuth{
			Username: basicCreds.Username,
			Password: basicCreds.Password,
		}
	case v1alpha1.GitHubAppAuthMethod:
		appCreds, err := newGithubAppCreds(stateStoreSpec, creds)
		if err != nil {
			return nil, err
		}

		j, err := GenerateGitHubAppJWT(appCreds.AppID, appCreds.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("failed to generate GitHub App JWT: %w", err)
		}

		token, err := GetGitHubInstallationToken(appCreds.ApiUrl, appCreds.InstallationID, j)
		if err != nil {
			return nil, fmt.Errorf("failed to get GitHub installation token: %w", err)
		}

		authMethod = &githttp.BasicAuth{
			Username: "x-access-token",
			Password: token,
		}
	}

	return &GitWriter{
		GitServer: gitServer{
			URL:    stateStoreSpec.URL,
			Branch: stateStoreSpec.Branch,
			Auth:   authMethod,
		},
		Author: gitAuthor{
			Name:  stateStoreSpec.GitAuthor.Name,
			Email: stateStoreSpec.GitAuthor.Email,
		},
		Log: logger,
		Path: strings.TrimPrefix(path.Join(
			stateStoreSpec.Path,
			destinationPath,
		), "/"),
		BasicAuth: stateStoreSpec.AuthMethod == v1alpha1.BasicAuthMethod,
	}, nil
}

func newSSHAuthCreds(stateStoreSpec v1alpha1.GitStateStoreSpec, creds map[string][]byte) (*sshAuthCreds, error) {
	sshPrivateKey, ok := creds["sshPrivateKey"]
	namespace := stateStoreSpec.SecretRef.Namespace
	name := stateStoreSpec.SecretRef.Name
	if !ok {
		return nil, fmt.Errorf("sshKey not found in secret %s/%s", namespace, name)
	}
	knownHosts, ok := creds["knownHosts"]
	if !ok {
		return nil, fmt.Errorf("knownHosts not found in secret %s/%s", namespace, name)
	}
	sshUser, err := sshUsernameFromURL(stateStoreSpec.URL)
	if err != nil {
		return nil, fmt.Errorf("error parsing GitStateStore url: %w", err)
	}
	return &sshAuthCreds{
		SSHPrivateKey: sshPrivateKey,
		KnownHosts:    knownHosts,
		SSHUser:       sshUser,
	}, nil
}

func newBasicAuthCreds(stateStoreSpec v1alpha1.GitStateStoreSpec, creds map[string][]byte) (*basicAuthCreds, error) {
	namespace := stateStoreSpec.SecretRef.Namespace
	name := stateStoreSpec.SecretRef.Name
	username, ok := creds["username"]
	if !ok {
		return nil, fmt.Errorf("username not found in secret %s/%s", namespace, name)
	}
	password, ok := creds["password"]
	if !ok {
		return nil, fmt.Errorf("password not found in secret %s/%s", namespace, name)
	}
	return &basicAuthCreds{
		Username: string(username),
		Password: string(password),
	}, nil
}

func newGithubAppCreds(stateStoreSpec v1alpha1.GitStateStoreSpec, creds map[string][]byte) (*githubAppCreds, error) {
	namespace := stateStoreSpec.SecretRef.Namespace
	name := stateStoreSpec.SecretRef.Name
	appID, ok := creds["appID"]
	if !ok {
		return nil, fmt.Errorf("appID not found in secret %s/%s", namespace, name)
	}
	installationID, ok := creds["installationID"]
	if !ok {
		return nil, fmt.Errorf("installationID not found in secret %s/%s", namespace, name)
	}
	privateKey, ok := creds["privateKey"]
	if !ok {
		return nil, fmt.Errorf("privateKey not found in secret %s/%s", namespace, name)
	}
	return &githubAppCreds{
		AppID:          string(appID),
		InstallationID: string(installationID),
		PrivateKey:     string(privateKey),
		ApiUrl:         "https://api.github.com",
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

	localTmpDir, repo, worktree, err := g.setupLocalDirectoryWithRepo(logger)
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(filepath.Dir(localTmpDir)) //nolint:errcheck

	err = g.deleteExistingFiles(subDir != "", dirInGitRepo, workloadsToDelete, worktree, logger)
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
		absoluteFilePath := filepath.Join(localTmpDir, worktreeFilePath)

		//We need to protect against paths containing `..`
		//filepath.Join expands any '../' in the Path to the actual, e.g. /tmp/foo/../ resolves to /tmp/
		//To ensure they can't write to files on disk outside the tmp git repository we check the absolute Path
		//returned by `filepath.Join` is still contained with the git repository:
		// Note: This means `../` can still be used, but only if the end result is still contained within the git repository
		if !strings.HasPrefix(absoluteFilePath, localTmpDir) {
			logging.Warn(log, "path of file to write is not located within the git repository", "absolutePath", absoluteFilePath, "tmpDir", localTmpDir)
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

		if _, err := worktree.Add(worktreeFilePath); err != nil {
			logging.Error(log, err, "could not add file to worktree")
			return "", err
		}
	}

	action := "Delete"
	if len(workloadsToCreate) > 0 {
		action = "Update"
	}
	return g.commitAndPush(repo, worktree, action, workPlacementName, logger)
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

	localTmpDir, _, worktree, err := g.setupLocalDirectoryWithRepo(logger)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(localTmpDir)) //nolint:errcheck

	if _, err := worktree.Filesystem.Lstat(fullPath); err != nil {
		logging.Debug(logger, "could not stat file", "error", err)
		return nil, ErrFileNotFound
	}

	var content []byte
	if content, err = os.ReadFile(filepath.Join(localTmpDir, fullPath)); err != nil {
		logging.Error(logger, err, "could not read file")
		return nil, err
	}
	return content, nil
}

func (g *GitWriter) setupLocalDirectoryWithRepo(logger logr.Logger) (string, *git.Repository, *git.Worktree, error) {
	localTmpDir, err := createLocalDirectory(logger)
	if err != nil {
		logging.Error(logger, err, "could not create temporary repository directory")
		return "", nil, nil, err
	}

	repo, cloneErr := g.cloneRepo(localTmpDir, logger)
	if cloneErr != nil && !errors.Is(cloneErr, ErrAuthSucceededAfterTrim) {
		logging.Error(logger, cloneErr, "could not clone repository")
		return "", nil, nil, cloneErr
	}

	if repo == nil {
		return "", nil, nil, fmt.Errorf("clone returned nil repository")
	}

	worktree, err := repo.Worktree()
	if err != nil {
		logging.Error(logger, err, "could not access repo worktree")
		return "", nil, nil, err
	}
	return localTmpDir, repo, worktree, cloneErr
}

func (g *GitWriter) push(repo *git.Repository, logger logr.Logger) error {
	err := repo.Push(&git.PushOptions{
		RemoteName:      "origin",
		Auth:            g.GitServer.Auth,
		InsecureSkipTLS: true,
	})
	if err != nil {
		logging.Error(logger, err, "could not push to remote")
		return err
	}
	return nil
}

// validatePush attempts to validate write permissions by pushing no changes to the remote
// If the push errors with "NoErrAlreadyUpToDate", it means we can write.
func (g *GitWriter) validatePush(repo *git.Repository, logger logr.Logger) error {
	err := repo.Push(&git.PushOptions{
		RemoteName:      "origin",
		Auth:            g.GitServer.Auth,
		InsecureSkipTLS: true,
	})

	// NoErrAlreadyUpToDate means we have write permissions
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		logging.Info(logger, "push validation successful - repository is up-to-date")
		return nil
	}

	if err != nil {
		return fmt.Errorf("write permission validation failed: %w", err)
	}

	return nil
}

// ValidatePermissions checks if the GitWriter has the necessary permissions to write to the repository.
// It performs a dry run validation to check authentication and branch existence without making changes.
func (g *GitWriter) ValidatePermissions() error {
	// Setup local directory with repo (this already checks if we can clone - read access)
	localTmpDir, repo, _, cloneErr := g.setupLocalDirectoryWithRepo(g.Log)
	if cloneErr != nil && !errors.Is(cloneErr, ErrAuthSucceededAfterTrim) {
		return fmt.Errorf("failed to set up local directory with repo: %w", cloneErr)
	}
	defer os.RemoveAll(localTmpDir) //nolint:errcheck

	if err := g.validatePush(repo, g.Log); err != nil {
		return err
	}

	logging.Info(g.Log, "successfully validated git repository permissions")
	return cloneErr
}

func (g *GitWriter) cloneRepo(localRepoFilePath string, logger logr.Logger) (*git.Repository, error) {
	// Azure DevOps requires multi_ack and multi_ack_detailed capabilities, which go-git doesn't
	// implement. But: it's possible to do a full clone by saying it's _not_ _un_supported, in which
	// case the library happily functions so long as it doesn't _actually_ get a multi_ack packet. See
	// https://github.com/go-git/go-git/blob/v5.5.1/_examples/azure_devops/main.go.
	oldUnsupportedCaps := transport.UnsupportedCapabilities

	// This check is crude, but avoids having another dependency to parse the git URL.
	if strings.Contains(g.GitServer.URL, "dev.azure.com") {
		transport.UnsupportedCapabilities = []capability.Capability{
			capability.ThinPack,
		}
	}
	defer func() { transport.UnsupportedCapabilities = oldUnsupportedCaps }()

	logging.Debug(logger, "cloning repo")
	cloneOpts := &git.CloneOptions{
		Auth:            g.GitServer.Auth,
		URL:             g.GitServer.URL,
		ReferenceName:   plumbing.NewBranchReferenceName(g.GitServer.Branch),
		SingleBranch:    true,
		Depth:           1,
		NoCheckout:      false,
		InsecureSkipTLS: true,
	}
	repo, err := git.PlainClone(localRepoFilePath, false, cloneOpts)

	if err != nil && isAuthError(err) {
		if g.BasicAuth {
			if trimmed, changed := trimmedBasicAuthCopy(g.GitServer.Auth); changed {
				logging.Debug(logger, "auth failed there are trailing spaces in credentials; will retry again with trimmed credentials")
				cloneOpts.Auth = &trimmed
				_ = os.RemoveAll(localRepoFilePath)
				if retryRepo, retryErr := git.PlainClone(localRepoFilePath, false, cloneOpts); retryErr == nil {
					logging.Warn(logger, "authentication succeeded after trimming trailing whitespace; please fix your GitStateStore Secret")
					g.GitServer.Auth = &trimmed
					return retryRepo, ErrAuthSucceededAfterTrim
				}
			}
		}
	}

	return repo, err
}

func (g *GitWriter) commitAndPush(repo *git.Repository, worktree *git.Worktree, action, workPlacementName string, logger logr.Logger) (string, error) {
	status, err := worktree.Status()
	if err != nil {
		logging.Error(logger, err, "could not get worktree status")
		return "", err
	}

	if status.IsClean() {
		logging.Info(logger, "no changes to be committed")
		return "", nil
	}

	commitHash, err := worktree.Commit(fmt.Sprintf("%s from: %s", action, workPlacementName), &git.CommitOptions{
		Author: &object.Signature{
			Name:  g.Author.Name,
			Email: g.Author.Email,
			When:  time.Now(),
		},
	})

	var sha string
	if !commitHash.IsZero() {
		sha = commitHash.String()
	}

	if err != nil {
		logging.Error(logger, err, "could not commit file to worktree")
		return "", err
	}

	logging.Info(logger, "pushing changes")
	if err := g.push(repo, logger); err != nil {
		logging.Error(logger, err, "could not push changes")
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

func sshUsernameFromURL(url string) (string, error) {
	ep, err := transport.NewEndpoint(url)
	if err != nil {
		return "", fmt.Errorf("failed to parse Git URL: %w", err)
	}
	if ep.User == "" {
		return "git", nil
	}
	return ep.User, nil
}

// for unit tests

var GenerateGitHubAppJWT = generateGitHubAppJWT
var GetGitHubInstallationToken = getGitHubInstallationToken

// generateGitHubAppJWT creates a signed JWT for GitHub App authentication
func generateGitHubAppJWT(appID string, privateKey string) (string, error) {
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return "", errors.New("invalid private key: failed to parse PEM block")
	}

	parsedKey, err := parseRSAPrivateKeyFromPEM(block)
	if err != nil {
		return "", err
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Unix() - 60,
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": appID,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(parsedKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}
	return signed, nil
}

func parseRSAPrivateKeyFromPEM(block *pem.Block) (*rsa.PrivateKey, error) {
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}

	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("failed to parse RSA private key")
	}
	if rsaKey, ok := k.(*rsa.PrivateKey); ok {
		return rsaKey, nil
	}
	return nil, errors.New("private key is not RSA")
}

// getGitHubInstallationToken exchanges a JWT for a GitHub installation access token
func getGitHubInstallationToken(apiURL, installationID, jwtToken string) (string, error) {
	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", apiURL, installationID)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("GitHub API request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusCreated {
		var body struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return "", fmt.Errorf("GitHub API error: status=%d, message=%s", resp.StatusCode, body.Message)
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	if result.Token == "" {
		return "", errors.New("empty installation token received")
	}
	return result.Token, nil
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, transport.ErrAuthenticationRequired) || errors.Is(err, transport.ErrAuthorizationFailed) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "authentication required") ||
		strings.Contains(s, "authorization failed") ||
		strings.Contains(s, "401") || strings.Contains(s, "403")
}

func trimmedBasicAuthCopy(auth transport.AuthMethod) (githttp.BasicAuth, bool) {
	basicAuth, ok := auth.(*githttp.BasicAuth)
	if !ok {
		return githttp.BasicAuth{}, false
	}

	u, uChanged := trimRightWhitespace(basicAuth.Username)
	p, pChanged := trimRightWhitespace(basicAuth.Password)
	if !uChanged && !pChanged {
		return githttp.BasicAuth{}, false
	}
	return githttp.BasicAuth{Username: u, Password: p}, true
}

func trimRightWhitespace(s string) (string, bool) {
	trimmed := strings.TrimRightFunc(s, unicode.IsSpace)
	return trimmed, trimmed != s
}
