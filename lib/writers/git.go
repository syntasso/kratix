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
	urlpkg "net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

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
	Creds     gitCredentials
}

type gitServer struct {
	URL    string
	Branch string
}

type gitAuthor struct {
	Name  string
	Email string
}

type gitCredentials struct {
	AuthMethod    string
	Username      string
	Password      string
	SSHPrivateKey []byte
	KnownHosts    []byte
	SSHUser       string
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
	var writerCreds gitCredentials

	switch stateStoreSpec.AuthMethod {
	case v1alpha1.SSHAuthMethod:
		sshCreds, err := newSSHAuthCreds(stateStoreSpec, creds)
		if err != nil {
			return nil, err
		}
		writerCreds = gitCredentials{
			AuthMethod:    stateStoreSpec.AuthMethod,
			SSHPrivateKey: sshCreds.SSHPrivateKey,
			KnownHosts:    sshCreds.KnownHosts,
			SSHUser:       sshCreds.SSHUser,
		}

	case v1alpha1.BasicAuthMethod:
		basicCreds, err := newBasicAuthCreds(stateStoreSpec, creds)
		if err != nil {
			return nil, err
		}
		writerCreds = gitCredentials{
			AuthMethod: stateStoreSpec.AuthMethod,
			Username:   basicCreds.Username,
			Password:   basicCreds.Password,
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

		writerCreds = gitCredentials{
			AuthMethod: stateStoreSpec.AuthMethod,
			Username:   "x-access-token",
			Password:   token,
		}

	default:
		return nil, fmt.Errorf("unsupported auth method %q", stateStoreSpec.AuthMethod)
	}

	return &GitWriter{
		GitServer: gitServer{
			URL:    stateStoreSpec.URL,
			Branch: stateStoreSpec.Branch,
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
		Creds:     writerCreds,
	}, nil
}

func newSSHAuthCreds(stateStoreSpec v1alpha1.GitStateStoreSpec, creds map[string][]byte) (*sshAuthCreds, error) {
	namespace := stateStoreSpec.SecretRef.Namespace
	name := stateStoreSpec.SecretRef.Name

	sshPrivateKey, ok := creds["sshPrivateKey"]
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

	repoCtx, err := g.setupLocalDirectoryWithRepo(logger)
	if err != nil {
		return "", err
	}
	defer repoCtx.cleanup() //nolint:errcheck

	if err := g.deleteExistingFiles(subDir != "", dirInGitRepo, workloadsToDelete, repoCtx.dir, logger); err != nil {
		return "", err
	}

	for _, file := range workloadsToCreate {
		worktreeFilePath := filepath.Join(dirInGitRepo, file.Filepath)
		log := logger.WithValues("filepath", worktreeFilePath)

		absoluteFilePath := filepath.Join(repoCtx.dir, worktreeFilePath)

		if !strings.HasPrefix(absoluteFilePath, repoCtx.dir) {
			logging.Warn(log, "path of file to write is not located within the git repository", "absolutePath", absoluteFilePath, "tmpDir", repoCtx.dir)
			return "", nil
		}

		if err := os.MkdirAll(filepath.Dir(absoluteFilePath), 0700); err != nil {
			logging.Error(log, err, "could not generate local directories")
			return "", err
		}

		if err := os.WriteFile(absoluteFilePath, []byte(file.Content), 0644); err != nil {
			logging.Error(log, err, "could not write to file")
			return "", err
		}
	}

	if err := g.stageChanges(repoCtx, logger); err != nil {
		return "", err
	}

	action := "Delete"
	if len(workloadsToCreate) > 0 {
		action = "Update"
	}
	return g.commitAndPush(repoCtx, action, workPlacementName, logger)
}

func (g *GitWriter) ReadFile(filePath string) ([]byte, error) {
	fullPath := filepath.Join(g.Path, filePath)
	logger := g.Log.WithValues(
		"Path", fullPath,
		"branch", g.GitServer.Branch,
	)

	repoCtx, err := g.setupLocalDirectoryWithRepo(logger)
	if err != nil {
		return nil, err
	}
	defer repoCtx.cleanup() //nolint:errcheck

	targetPath := filepath.Join(repoCtx.dir, fullPath)
	if _, err := os.Lstat(targetPath); err != nil {
		logging.Debug(logger, "could not stat file", "error", err)
		return nil, ErrFileNotFound
	}

	content, err := os.ReadFile(targetPath)
	if err != nil {
		logging.Error(logger, err, "could not read file")
		return nil, err
	}

	return content, nil
}

type gitRepoContext struct {
	dir     string
	env     []string
	cleanup func()
}

func (g *GitWriter) setupLocalDirectoryWithRepo(logger logr.Logger) (gitRepoContext, error) {
	repoDir, err := os.MkdirTemp("", "kratix-repo")
	if err != nil {
		logging.Error(logger, err, "could not create temporary repository directory")
		return gitRepoContext{}, err
	}

	// IMPORTANT: keep repoDir empty for `git clone` by creating auth artifacts elsewhere.
	authDir, err := os.MkdirTemp("", "kratix-git-auth")
	if err != nil {
		_ = os.RemoveAll(repoDir)
		return gitRepoContext{}, err
	}

	env, authCleanup, envErr := g.gitEnv(authDir)
	if envErr != nil {
		_ = os.RemoveAll(repoDir)
		_ = os.RemoveAll(authDir)
		return gitRepoContext{}, envErr
	}

	repoCtx := gitRepoContext{
		dir: repoDir,
		env: env,
		cleanup: func() {
			authCleanup()
			_ = os.RemoveAll(repoDir)
			_ = os.RemoveAll(authDir)
		},
	}

	if cloneErr := g.cloneRepo(repoCtx.dir, repoCtx.env, logger); cloneErr != nil {
		repoCtx.cleanup()
		logging.Error(logger, cloneErr, "could not clone repository")
		return gitRepoContext{}, cloneErr
	}

	return repoCtx, nil
}

func (g *GitWriter) push(repoCtx gitRepoContext, logger logr.Logger) error {
	_, err := GitCommand(repoCtx.dir, repoCtx.env, "push", "origin", g.GitServer.Branch)
	if err != nil {
		logging.Error(logger, err, "could not push to remote")
		return err
	}
	return nil
}

func (g *GitWriter) validatePush(repoCtx gitRepoContext, logger logr.Logger) error {
	output, err := GitCommand(repoCtx.dir, repoCtx.env, "push", "--dry-run", "origin", g.GitServer.Branch)
	if err != nil {
		return fmt.Errorf("write permission validation failed: %w", err)
	}
	logging.Info(logger, "push validation successful", "output", strings.TrimSpace(string(output)))
	return nil
}

func (g *GitWriter) ValidatePermissions() error {
	repoCtx, err := g.setupLocalDirectoryWithRepo(g.Log)
	if err != nil {
		return fmt.Errorf("failed to set up local directory with repo: %w", err)
	}
	defer repoCtx.cleanup() //nolint:errcheck

	if err := g.validatePush(repoCtx, g.Log); err != nil {
		return err
	}

	logging.Info(g.Log, "successfully validated git repository permissions")
	return nil
}

func (g *GitWriter) deleteExistingFiles(removeDirectory bool, dir string, workloadsToDelete []string, repoPath string, logger logr.Logger) error {
	if removeDirectory {
		target := filepath.Join(repoPath, dir)
		if _, err := os.Lstat(target); err == nil {
			logging.Info(logger, "deleting existing content")
			return os.RemoveAll(target)
		}
		return nil
	}

	for _, file := range workloadsToDelete {
		worktreeFilePath := filepath.Join(dir, file)
		log := logger.WithValues("filepath", worktreeFilePath)

		target := filepath.Join(repoPath, worktreeFilePath)
		if _, err := os.Lstat(target); err != nil {
			logging.Debug(log, "file requested to be deleted from worktree but does not exist")
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			logging.Error(log, err, "could not remove file from worktree")
			return err
		}
		logging.Debug(log, "successfully deleted file from worktree")
	}

	return nil
}

func (g *GitWriter) cloneRepo(localRepoFilePath string, env []string, logger logr.Logger) error {
	logging.Debug(logger, "cloning repo")

	cloneURL := g.GitServer.URL
	var err error
	if g.Creds.AuthMethod == v1alpha1.BasicAuthMethod || g.Creds.AuthMethod == v1alpha1.GitHubAppAuthMethod {
		cloneURL, err = buildAuthenticatedURL(g.Creds.Username, g.Creds.Password, g.GitServer.URL)
		if err != nil {
			return err
		}
	}

	parent := filepath.Dir(localRepoFilePath)

	_, err = GitCommand(parent, env,
		"clone",
		"--depth", "1",
		"--single-branch",
		"--branch", g.GitServer.Branch,
		cloneURL,
		localRepoFilePath,
	)
	return err
}

func buildAuthenticatedURL(username, password, rawURL string) (string, error) {
	parsed, err := urlpkg.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse git url: %w", err)
	}
	parsed.User = urlpkg.UserPassword(username, password)
	return parsed.String(), nil
}

func (g *GitWriter) gitStatusIsClean(repoCtx gitRepoContext, logger logr.Logger) (bool, error) {
	output, err := GitCommand(repoCtx.dir, repoCtx.env, "status", "--porcelain")
	if err != nil {
		logging.Error(logger, err, "could not get worktree status")
		return false, err
	}
	return strings.TrimSpace(string(output)) == "", nil
}

func (g *GitWriter) stageChanges(repoCtx gitRepoContext, logger logr.Logger) error {
	if _, err := GitCommand(repoCtx.dir, repoCtx.env, "add", "-A"); err != nil {
		logging.Error(logger, err, "could not stage changes")
		return err
	}
	return nil
}

func (g *GitWriter) commitAndPush(repoCtx gitRepoContext, action, workPlacementName string, logger logr.Logger) (string, error) {
	clean, err := g.gitStatusIsClean(repoCtx, logger)
	if err != nil {
		return "", err
	}
	if clean {
		logging.Info(logger, "no changes to be committed")
		return "", nil
	}

	commitMessage := fmt.Sprintf("%s from: %s", action, workPlacementName)

	_, err = GitCommand(repoCtx.dir, repoCtx.env,
		"-c", fmt.Sprintf("user.name=%s", g.Author.Name),
		"-c", fmt.Sprintf("user.email=%s", g.Author.Email),
		"commit",
		"--author", fmt.Sprintf("%s <%s>", g.Author.Name, g.Author.Email),
		"-m", commitMessage,
	)
	if err != nil {
		logging.Error(logger, err, "could not commit file to worktree")
		return "", err
	}

	sha := ""
	if output, revErr := GitCommand(repoCtx.dir, repoCtx.env, "rev-parse", "HEAD"); revErr == nil {
		sha = strings.TrimSpace(string(output))
	}

	logging.Info(logger, "pushing changes")
	if err := g.push(repoCtx, logger); err != nil {
		logging.Error(logger, err, "could not push changes")
		return "", err
	}

	return sha, nil
}

func runGitCommand(dir string, env []string, args ...string) ([]byte, error) {
	if dir == "" {
		return nil, fmt.Errorf("git %s: working directory must not be empty", strings.Join(args, " "))
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

var GitCommand = runGitCommand

func (g *GitWriter) gitEnv(authDir string) ([]string, func(), error) {
	cleanup := func() {}
	env := []string{"GIT_TERMINAL_PROMPT=0"}

	if g.Creds.AuthMethod != v1alpha1.SSHAuthMethod {
		return env, cleanup, nil
	}

	keyFile, err := os.CreateTemp(authDir, "ssh-key-*")
	if err != nil {
		return nil, cleanup, err
	}
	if err := os.Chmod(keyFile.Name(), 0600); err != nil {
		_ = keyFile.Close()
		return nil, cleanup, err
	}
	if _, err := keyFile.Write(g.Creds.SSHPrivateKey); err != nil {
		_ = keyFile.Close()
		return nil, cleanup, err
	}
	_ = keyFile.Close()

	knownHostsFile, err := os.CreateTemp(authDir, "known-hosts-*")
	if err != nil {
		_ = os.Remove(keyFile.Name())
		return nil, cleanup, err
	}
	if _, err := knownHostsFile.Write(g.Creds.KnownHosts); err != nil {
		_ = knownHostsFile.Close()
		_ = os.Remove(keyFile.Name())
		_ = os.Remove(knownHostsFile.Name())
		return nil, cleanup, err
	}
	_ = knownHostsFile.Close()

	sshCmd := fmt.Sprintf(
		"ssh -i %s -o UserKnownHostsFile=%s -o StrictHostKeyChecking=yes -o IdentitiesOnly=yes",
		keyFile.Name(),
		knownHostsFile.Name(),
	)
	env = append(env, fmt.Sprintf("GIT_SSH_COMMAND=%s", sshCmd))

	cleanup = func() {
		_ = os.Remove(keyFile.Name())
		_ = os.Remove(knownHostsFile.Name())
	}
	return env, cleanup, nil
}

func sshUsernameFromURL(raw string) (string, error) {
	parsed, err := urlpkg.Parse(raw)
	if err == nil && parsed != nil {
		if parsed.User != nil && parsed.User.Username() != "" {
			return parsed.User.Username(), nil
		}
		return "git", nil
	}
	if at := strings.Index(raw, "@"); at != -1 {
		user := raw[:at]
		if user != "" {
			return user, nil
		}
	}
	return "git", nil
}

// --- GitHub App helpers (unchanged) ---
var GenerateGitHubAppJWT = generateGitHubAppJWT
var GetGitHubInstallationToken = getGitHubInstallationToken

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
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "authentication required") ||
		strings.Contains(s, "authorization failed") ||
		strings.Contains(s, "authentication failed") ||
		strings.Contains(s, "could not read username") ||
		strings.Contains(s, "401") || strings.Contains(s, "403")
}

func trimRightWhitespace(s string) (string, bool) {
	trimmed := strings.TrimRightFunc(s, unicode.IsSpace)
	return trimmed, trimmed != s
}
