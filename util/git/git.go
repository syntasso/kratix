// Portions derived from Argo CD (https://github.com/argoproj/argo-cd/commit/c56f4400f22c7e9fe9c5c12b85576b74369fb6b8),
// licensed under the Apache License, Version 2.0.
//
// Modifications Copyright 2026 Syntasso

//go:generate mockgen -source=git.go -destination=mocks/mock_client.go -package=mocks
package git

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/kelseyhightower/envconfig"
	gocache "github.com/patrickmn/go-cache"
	"golang.org/x/net/http/httpproxy"

	"github.com/syntasso/kratix/internal/logging"
)

var (
	timeout      time.Duration
	fatalTimeout time.Duration

	Unredacted    = Redact(nil)
	sshURLRegex   = regexp.MustCompile("^(ssh://)?([^/:]*?)@[^@]+$")
	httpsURLRegex = regexp.MustCompile("^(https://).*")
)

type GitClientRequest struct {
	RawRepoURL string
	Root       string
	Auth       *Auth
	Insecure   bool
	Proxy      string
	NoProxy    string
	Opts       []ClientOpts
	Log        logr.Logger
}

func NewGitClient(req GitClientRequest) (*nativeGitClient, error) {
	var accessToken string

	switch req.Auth.Creds.(type) {
	case SSHCreds:
		if ok, _ := IsSSHURL(req.RawRepoURL); !ok {
			return nil, fmt.Errorf("invalid URL for SSH auth method: %s", req.RawRepoURL)
		}
	default:
		if !IsHTTPSURL(req.RawRepoURL) {
			return nil, fmt.Errorf("invalid URL for HTTPS auth method: %s", req.RawRepoURL)
		}
	}

	config := Config{}
	err := envconfig.Process("", &config)
	if err != nil {
		return nil, fmt.Errorf("could not load config: %w", err)
	}

	client := &nativeGitClient{
		accessToken:  accessToken,
		repoURL:      req.RawRepoURL,
		root:         req.Root,
		creds:        req.Auth.Creds,
		insecure:     req.Insecure,
		proxy:        req.Proxy,
		noProxy:      req.NoProxy,
		gitConfigEnv: BuiltinGitConfigEnv,
		log:          req.Log,
		config:       config,
	}
	for i := range req.Opts {
		req.Opts[i](client)
	}

	client.setConfig()

	return client, nil
}

// IsSSHURL returns true if supplied URL is SSH URL
func IsSSHURL(url string) (bool, string) {
	matches := sshURLRegex.FindStringSubmatch(url)
	if len(matches) > 2 {
		return true, matches[2]
	}
	return false, ""
}

// injectGitHubAppCredentials adds username and token to a Git URL's authority section.
// Example: https://github.com/user/repo.git -> https://x-access-token:token@github.com/user/repo.git
func injectGitHubAppCredentials(gitURL, token string) (string, error) {
	u, err := url.Parse(gitURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL: %w", err)
	}

	u.User = url.UserPassword("x-access-token", token)

	return u.String(), nil
}

func (m *nativeGitClient) setConfig() {
	timeout = m.config.Timeout
	fatalTimeout = m.config.FatalTimeout

	githubAppTokenCache = gocache.New(m.config.GithubAppCredsExpirationDuration, 1*time.Minute)

	BuiltinGitConfigEnv = append(
		BuiltinGitConfigEnv, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(builtinGitConfig)))
	idx := 0
	for k, v := range builtinGitConfig {
		BuiltinGitConfigEnv = append(BuiltinGitConfigEnv, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", idx, k))
		BuiltinGitConfigEnv = append(BuiltinGitConfigEnv, fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", idx, v))
		idx++
	}
}

func Redact(items []string) func(text string) string {
	return func(text string) string {
		for _, item := range items {
			text = strings.ReplaceAll(text, item, "******")
		}
		return text
	}
}

func RandHex(n int) (string, error) {
	bytes := make([]byte, n/2+1) // we need one extra letter to discard
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes)[0:n], nil
}

// builtinGitConfig configuration contains statements that are needed
// for correct Git-client operation. These settings will override any
// user-provided configuration of same options.
var builtinGitConfig = map[string]string{
	"maintenance.autoDetach": "false",
	"gc.autoDetach":          "false",
}

// BuiltinGitConfigEnv contains builtin git configuration in the
// format acceptable by Git.
var BuiltinGitConfigEnv []string

// nativeGitClient implements Client interface using git CLI
type nativeGitClient struct {
	log    logr.Logger
	config Config

	// URL of the repository
	repoURL string
	// Root path of repository
	root string
	// Authenticator credentials for private repositories
	creds Creds
	// Whether to connect insecurely to repository, e.g. don't verify certificate
	insecure bool
	// HTTP/HTTPS proxy used to access repository
	proxy string
	// list of targets that shouldn't use the proxy, applies only if the proxy is set
	noProxy string
	// git configuration environment variables
	gitConfigEnv []string
	// access token or installation access token
	accessToken string
}

type ClientOpts func(c *nativeGitClient)

func serverNameWithoutPort(serverName string) string {
	return strings.Split(serverName, ":")[0]
}

func parseTLSCertificatesFromPath(sourceFile string) ([]string, error) {
	data, err := os.ReadFile(sourceFile)
	if err != nil {
		return nil, err
	}
	return parseTLSCertificatesFromPEM(data)
}

func parseTLSCertificatesFromPEM(pemData []byte) ([]string, error) {
	var certs []string
	for {
		var block *pem.Block
		block, pemData = pem.Decode(pemData)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		certs = append(certs, string(pem.EncodeToMemory(block)))
	}
	return certs, nil
}

func getCertificateForConnect(serverName string) ([]string, error) {
	dataPath := os.Getenv("KRATIX_TLS_DATA_PATH")
	certPath, err := filepath.Abs(filepath.Join(dataPath, serverNameWithoutPort(serverName)))
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(certPath, dataPath) {
		return nil, fmt.Errorf("could not get certificate for host %s", serverName)
	}
	certificates, err := parseTLSCertificatesFromPath(certPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(certificates) == 0 {
		return nil, errors.New("no certificates found in existing file")
	}
	return certificates, nil
}

func getCertBundlePathForRepository(serverName string) string {
	certPath := filepath.Join(os.Getenv("KRATIX_TLS_DATA_PATH"), serverNameWithoutPort(serverName))
	certs, err := getCertificateForConnect(serverName)
	if err != nil {
		return ""
	}
	if len(certs) == 0 {
		return ""
	}
	return certPath
}

func getCertPoolFromPEMData(pemData []string) *x509.CertPool {
	certPool := x509.NewCertPool()
	for _, pemEntry := range pemData {
		certPool.AppendCertsFromPEM([]byte(pemEntry))
	}
	return certPool
}

func upsertProxyEnv(cmd *exec.Cmd, proxyURL string, noProxy string) []string {
	envs := []string{}
	if proxyURL == "" {
		return cmd.Env
	}
	for _, env := range cmd.Env {
		proxyEnv := strings.ToLower(env)
		if strings.HasPrefix(proxyEnv, "http_proxy") ||
			strings.HasPrefix(proxyEnv, "https_proxy") ||
			strings.HasPrefix(proxyEnv, "no_proxy") {
			continue
		}
		envs = append(envs, env)
	}
	return append(envs, "http_proxy="+proxyURL, "https_proxy="+proxyURL, "no_proxy="+noProxy)
}

func getProxyCallback(proxyURL string, noProxy string) func(*http.Request) (*url.URL, error) {
	if proxyURL != "" {
		c := httpproxy.Config{
			HTTPProxy:  proxyURL,
			HTTPSProxy: proxyURL,
			NoProxy:    noProxy,
		}
		return func(r *http.Request) (*url.URL, error) {
			if r != nil {
				return c.ProxyFunc()(r.URL)
			}
			return url.Parse(c.HTTPProxy)
		}
	}
	return http.ProxyFromEnvironment
}

func (m *nativeGitClient) Root() string {
	return m.root
}

// Init initialises a local git repository and sets the remote origin
func (m *nativeGitClient) Init() (string, error) {
	ctx := context.Background()

	var err error
	m.root, err = createLocalDirectory(m.log)
	if err != nil {
		logging.Error(m.log, err, "could not create temporary repository directory")
		return "", err
	}

	logging.Debug(m.log, fmt.Sprintf("initialising repo %s to %s", m.repoURL, m.root))
	err = os.RemoveAll(m.root)
	if err != nil {
		return "", fmt.Errorf("unable to clean repo at %s: %w", m.root, err)
	}
	err = os.MkdirAll(m.root, 0o750)
	if err != nil {
		return "", err
	}

	args := []string{"init", "."}
	err = m.runCredentialedCmd(ctx, args...)
	if err != nil {
		logging.Error(m.log, err, "could not initialise temporary repository directory")
		return "", err
	}

	args = []string{"remote", "add", "origin", m.repoURL}
	err = m.runCredentialedCmd(ctx, args...)
	if err != nil {
		logging.Error(m.log, err, "could not add remote origin")
		return "", err
	}

	return m.root, nil
}

func (m *nativeGitClient) RemoveDirectory(dir string) error {
	args := []string{"rm", "-r", dir}
	ctx := context.Background()
	err := m.runCredentialedCmd(ctx, args...)
	if err != nil {
		logging.Error(m.log, err, "could not remove directory")
		return err
	}
	return nil
}

func (m *nativeGitClient) RemoveFile(file string) error {
	args := []string{"rm", file}
	ctx := context.Background()
	err := m.runCredentialedCmd(ctx, args...)
	if err != nil {
		logging.Error(m.log, err, "could not remove file")
		return err
	}
	return nil
}

func (m *nativeGitClient) fetch(ctx context.Context, revision string, depth int64) error {
	args := []string{"fetch", "origin"}
	if revision != "" {
		args = append(args, revision)
	}

	if depth > 0 {
		args = append(args, "--depth", strconv.FormatInt(depth, 10))
	} else {
		args = append(args, "--tags")
	}
	args = append(args, "--force", "--prune")
	return m.runCredentialedCmd(ctx, args...)
}

// Push pushes changes to the target branch.
func (m *nativeGitClient) Push(branch string, force bool) (string, error) {
	ctx := context.Background()

	args := []string{"push", "origin", branch}
	if force {
		args = append(args, "--force")
	}

	err := m.runCredentialedCmd(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("failed to push: %w", err)
	}

	return "", nil
}

// CommitAndPush commits and pushes changes to the target branch.
func (m *nativeGitClient) CommitAndPush(branch, message, author string, email string) (string, error) {
	ctx := context.Background()
	// out, err := m.runCmd(ctx, "add", ".")
	// if err != nil {
	// 	return out, fmt.Errorf("failed to add files: %w", err)
	// }

	authorId := fmt.Sprintf("%s <%s>", author, email)
	out, err := m.runCmd(ctx,
		"-c", fmt.Sprintf("user.name=%s", author),
		"-c", fmt.Sprintf("user.email=%s", email),
		"commit",
		"-m", message,
		fmt.Sprintf("--author=%s", authorId),
	)
	if err != nil {
		if strings.Contains(out, ErrNothingToCommit.Error()) {
			return out, ErrNothingToCommit
		}
		return out, fmt.Errorf("failed to commit: %w", err)
	}

	err = m.runCredentialedCmd(ctx,
		"-c", fmt.Sprintf("user.name=%s", author),
		"-c", fmt.Sprintf("user.email=%s", email),
		"pull", "origin", "--rebase")
	if err != nil {
		return "", fmt.Errorf("failed to pull from origin (rebase): %w", err)
	}

	err = m.runCredentialedCmd(ctx, "push", "origin", branch)
	if err != nil {
		return "", fmt.Errorf("failed to push: %w", err)
	}

	commitSha, err := m.runCmd(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to push: %w", err)
	}

	return commitSha, nil
}

// IsHTTPSURL returns true if supplied URL is HTTPS URL
func IsHTTPSURL(url string) bool {
	return httpsURLRegex.MatchString(url)
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
// Behaviour:
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
// It will return the temporary location where the repository was checked out.
func (m *nativeGitClient) Clone(branch string) (string, error) {
	logging.Debug(m.log, "cloning repo")

	localDir, err := m.Init()
	if err != nil {
		return "", fmt.Errorf("could not run init: %w", err)
	}

	err = m.Fetch(branch, 1)
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

func createLocalDirectory(logger logr.Logger) (string, error) {
	logging.Debug(logger, "creating local directory")
	dir, err := os.MkdirTemp("", "kratix-repo")
	if err != nil {
		return "", err
	}

	return dir, nil
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

	return strings.Contains(out, "Changes to be committed"), nil
}

// Helper function to parse a time duration from an environment variable. Returns a
// default if env is not set, is not parseable to a duration, exceeds maximum (if
// maximum is greater than 0) or is less than minimum.
func ParseDurationFromEnv(logger logr.Logger, env string, defaultValue, minimum, maximum time.Duration) time.Duration {
	logCtx := logger.WithValues()

	str := os.Getenv(env)
	if str == "" {
		return defaultValue
	}
	dur, err := time.ParseDuration(str)
	if err != nil {
		logging.Debug(logCtx, "Could not parse '%s' as a duration string from environment %s", str, env)
		return defaultValue
	}

	if dur < minimum {
		logging.Debug(logCtx, "Value in %s is %s, which is less than minimum %s allowed", env, dur, minimum)
		return defaultValue
	}
	if dur > maximum {
		logging.Debug(logCtx, "Value in %s is %s, which is greater than maximum %s allowed", env, dur, maximum)
		return defaultValue
	}
	return dur
}
