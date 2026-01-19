package git

import (
	"bytes"
	"context"
	"crypto/fips140"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-logr/logr"
	gocache "github.com/patrickmn/go-cache"
	"github.com/syntasso/kratix/internal/logging"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/http/httpproxy"
)

type GitClientError error

var (
	ErrNoFilesChanged  GitClientError = errors.New("no files changed")
	ErrNothingToCommit GitClientError = errors.New("nothing to commit, working tree clean")
)

var (
	timeout      time.Duration
	fatalTimeout time.Duration
	Unredacted   = Redact(nil)
)

type ExecRunOpts struct {
	// Redactor redacts tokens from the output
	Redactor func(text string) string
	// TimeoutBehavior configures what to do in case of timeout
	TimeoutBehavior TimeoutBehavior
	// SkipErrorLogging determines whether to skip logging of execution errors (rc > 0)
	SkipErrorLogging bool
	// CaptureStderr determines whether to capture stderr in addition to stdout
	CaptureStderr bool
}

// TODO: remove init
func init() {
	initTimeout()

	githubAppCredsExp := GithubAppCredsExpirationDuration
	if exp := os.Getenv(EnvGithubAppCredsExpirationDuration); exp != "" {
		if qps, err := strconv.Atoi(exp); err != nil {
			githubAppCredsExp = time.Duration(qps) * time.Minute
		}
	}

	githubAppTokenCache = gocache.New(githubAppCredsExp, 1*time.Minute)
	// TODO: inspect whether this is needed for Azure
	//azureTokenCache = gocache.New(gocache.NoExpiration, 0)
	githubInstallationIdCache = gocache.New(60*time.Minute, 60*time.Minute)
}

func initTimeout() {
	var err error
	timeout, err = time.ParseDuration(os.Getenv("KRATIX_EXEC_TIMEOUT"))
	if err != nil {
		timeout = 90 * time.Second
	}
	fatalTimeout, err = time.ParseDuration(os.Getenv("KRATIX_EXEC_FATAL_TIMEOUT"))
	if err != nil {
		fatalTimeout = 10 * time.Second
	}
}

// Client is a generic git client interface
type Client interface {
	Add(files ...string) (string, error)
	Checkout(revision string) (string, error)
	Clone(string) (string, error)
	CommitAndPush(branch, message, author, email string) (string, error)
	Fetch(revision string, depth int64) error
	HasFileChanged(filePath string) (bool, error)
	Init() (string, error)
	Push(branch string) (string, error)
	Root() string

	RemoveDirectory(dir string) error
	RemoveFile(file string) error
	HasChanges() (bool, error)
}

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

var (
	sshURLRegex   = regexp.MustCompile("^(ssh://)?([^/:]*?)@[^@]+$")
	httpsURLRegex = regexp.MustCompile("^(https://).*")
)

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

func NewGitClient(req GitClientRequest) (Client, error) {

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
	}
	for i := range req.Opts {
		req.Opts[i](client)
	}

	return client, nil
}

func RunWithExecRunOpts(cmd *exec.Cmd, opts ExecRunOpts, logger logr.Logger) (string, error) {
	cmdOpts := CmdOpts{
		Timeout:          timeout,
		FatalTimeout:     fatalTimeout,
		Redactor:         opts.Redactor,
		TimeoutBehavior:  opts.TimeoutBehavior,
		SkipErrorLogging: opts.SkipErrorLogging,
		CaptureStderr:    opts.CaptureStderr}
	return RunCommandExt(cmd, cmdOpts, logger)
}

type CmdError struct {
	Args   string
	Stderr string
	Cause  error
}

func (ce *CmdError) Error() string {
	res := fmt.Sprintf("`%v` failed %v", ce.Args, ce.Cause)
	if ce.Stderr != "" {
		res = fmt.Sprintf("%s: %s", res, ce.Stderr)
	}
	return res
}

func (ce *CmdError) String() string {
	return ce.Error()
}

func newCmdError(args string, cause error, stderr string) *CmdError {
	return &CmdError{Args: args, Stderr: stderr, Cause: cause}
}

// TimeoutBehavior defines behaviour for when the command takes longer than the passed in timeout to exit
// By default, SIGKILL is sent to the process and it is not waited upon
type TimeoutBehavior struct {
	// Signal determines the signal to send to the process
	Signal syscall.Signal
	// ShouldWait determines whether to wait for the command to exit once timeout is reached
	ShouldWait bool
}

type CmdOpts struct {
	// Timeout determines how long to wait for the command to exit
	Timeout time.Duration
	// FatalTimeout is the amount of additional time to wait after Timeout before fatal SIGKILL
	FatalTimeout time.Duration
	// Redactor redacts tokens from the output
	Redactor func(text string) string
	// TimeoutBehavior configures what to do in case of timeout
	TimeoutBehavior TimeoutBehavior
	// SkipErrorLogging defines whether to skip logging of execution errors (rc > 0)
	SkipErrorLogging bool
	// CaptureStderr defines whether to capture stderr in addition to stdout
	CaptureStderr bool
}

var DefaultCmdOpts = CmdOpts{
	Timeout:          time.Duration(0),
	FatalTimeout:     time.Duration(0),
	Redactor:         Unredacted,
	TimeoutBehavior:  TimeoutBehavior{syscall.SIGKILL, false},
	SkipErrorLogging: false,
	CaptureStderr:    false,
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

// RunCommandExt is a convenience function to run/log a command and return/log stderr in an error upon
// failure.
func RunCommandExt(cmd *exec.Cmd, opts CmdOpts, logger logr.Logger) (string, error) {
	execId, err := RandHex(5)
	if err != nil {
		return "", err
	}
	logCtx := logger.WithValues("execID", execId, "dir", cmd.Dir)

	redactor := DefaultCmdOpts.Redactor
	if opts.Redactor != nil {
		redactor = opts.Redactor
	}

	// log in a way we can copy-and-paste into a terminal
	args := strings.Join(cmd.Args, " ")
	logging.Debug(logCtx, "running command", "args", redactor(args))

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err = cmd.Start()
	if err != nil {
		return "", err
	}

	done := make(chan error)
	go func() { done <- cmd.Wait() }()

	// Start timers for timeout
	timeout := DefaultCmdOpts.Timeout
	fatalTimeout := DefaultCmdOpts.FatalTimeout

	if opts.Timeout != time.Duration(0) {
		timeout = opts.Timeout
	}

	if opts.FatalTimeout != time.Duration(0) {
		fatalTimeout = opts.FatalTimeout
	}

	var timoutCh <-chan time.Time
	if timeout != 0 {
		timoutCh = time.NewTimer(timeout).C
	}

	var fatalTimeoutCh <-chan time.Time
	if fatalTimeout != 0 {
		fatalTimeoutCh = time.NewTimer(timeout + fatalTimeout).C
	}

	timeoutBehavior := DefaultCmdOpts.TimeoutBehavior
	fatalTimeoutBehaviour := syscall.SIGKILL
	if opts.TimeoutBehavior.Signal != syscall.Signal(0) {
		timeoutBehavior = opts.TimeoutBehavior
	}

	select {
	// noinspection ALL
	case <-timoutCh:
		// send timeout signal
		_ = cmd.Process.Signal(timeoutBehavior.Signal)
		// wait on timeout signal and fallback to fatal timeout signal
		if timeoutBehavior.ShouldWait {
			select {
			case <-done:
			case <-fatalTimeoutCh:
				// upgrades to SIGKILL if cmd does not respect SIGTERM
				_ = cmd.Process.Signal(fatalTimeoutBehaviour)
				// now original cmd should exit immediately after SIGKILL
				<-done
				// return error with a marker indicating that cmd exited only after fatal SIGKILL
				output := stdout.String()
				if opts.CaptureStderr {
					output += stderr.String()
				}
				logging.Debug(logCtx, "command output", "duration", time.Since(start), "output", redactor(output))
				err = newCmdError(redactor(args), fmt.Errorf("fatal timeout after %v", timeout+fatalTimeout), "")
				logging.Error(logCtx, err, "command failed", "args", redactor(args), "duration", time.Since(start))
				return strings.TrimSuffix(output, "\n"), err
			}
		}
		// either did not wait for timeout or cmd did respect SIGTERM
		output := stdout.String()
		if opts.CaptureStderr {
			output += stderr.String()
		}
		logging.Debug(logCtx, "command output", "duration", time.Since(start), "output", redactor(output))
		err = newCmdError(redactor(args), fmt.Errorf("timeout after %v", timeout), "")
		logging.Error(logCtx, err, "command failed", "args", redactor(args), "duration", time.Since(start))
		return strings.TrimSuffix(output, "\n"), err
	case err := <-done:
		if err != nil {
			output := stdout.String()
			if opts.CaptureStderr {
				output += stderr.String()
			}
			logging.Debug(logCtx, "command output", "duration", time.Since(start), "output", redactor(output))
			err := newCmdError(redactor(args), errors.New(redactor(err.Error())), strings.TrimSpace(redactor(stderr.String())))
			if !opts.SkipErrorLogging {
				logging.Error(logCtx, err, "command failed", "args", redactor(args), "duration", time.Since(start))
			}
			return strings.TrimSuffix(output, "\n"), err
		}
	}
	output := stdout.String()
	if opts.CaptureStderr {
		output += stderr.String()
	}
	logging.Debug(logCtx, "command output", "duration", time.Since(start), "output", redactor(output))

	return strings.TrimSuffix(output, "\n"), nil
}

// builtinGitConfig configuration contains statements that are needed
// for correct ArgoCD operation. These settings will override any
// user-provided configuration of same options.
var builtinGitConfig = map[string]string{
	"maintenance.autoDetach": "false",
	"gc.autoDetach":          "false",
}

// BuiltinGitConfigEnv contains builtin git configuration in the
// format acceptable by Git.
var BuiltinGitConfigEnv []string

// CommitMetadata contains metadata about a commit that is related in some way to another commit.
type CommitMetadata struct {
	// Author is the author of the commit.
	// Comes from the Argocd-reference-commit-author trailer.
	Author mail.Address
	// Date is the date of the commit, formatted as by `git show -s --format=%aI`.
	// May be an empty string if the date is unknown.
	// Comes from the Argocd-reference-commit-date trailer.
	Date string
	// Subject is the commit message subject, i.e. `git show -s --format=%s`.
	// Comes from the Argocd-reference-commit-subject trailer.
	Subject string
	// Body is the commit message body, excluding the subject, i.e. `git show -s --format=%b`.
	// Comes from the Argocd-reference-commit-body trailer.
	Body string
	// SHA is the commit hash.
	// Comes from the Argocd-reference-commit-sha trailer.
	SHA string
	// RepoURL is the URL of the repository where the commit is located.
	// Comes from the Argocd-reference-commit-repourl trailer.
	// This value is not validated beyond confirming that it's a URL, and it should not be used to construct UI links
	// unless it is properly validated and/or sanitised first.
	RepoURL string
}

// this should match reposerver/repository/repository.proto/RefsList
type Refs struct {
	Branches []string
	Tags     []string
	// heads and remotes are also refs, but are not needed at this time.
}

type EventHandlers struct {
	OnLsRemote func(repo string) func()
	OnFetch    func(repo string) func()
	OnPush     func(repo string) func()
}

// nativeGitClient implements Client interface using git CLI
type nativeGitClient struct {
	EventHandlers
	log logr.Logger

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

type runOpts struct {
	SkipErrorLogging bool
	CaptureStderr    bool
}

// TODO: move it to constructor
func init() {
	BuiltinGitConfigEnv = append(BuiltinGitConfigEnv, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(builtinGitConfig)))
	idx := 0
	for k, v := range builtinGitConfig {
		BuiltinGitConfigEnv = append(BuiltinGitConfigEnv, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", idx, k))
		BuiltinGitConfigEnv = append(BuiltinGitConfigEnv, fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", idx, v))
		idx++
	}
}

type ClientOpts func(c *nativeGitClient)

// Returns a HTTP client object suitable for go-git to use using the following
// pattern:
//   - If insecure is true, always returns a client with certificate verification
//     turned off.
//   - If one or more custom certificates are stored for the repository, returns
//     a client with those certificates in the list of root CAs used to verify
//     the server's certificate.
//   - Otherwise (and on non-fatal errors), a default HTTP client is returned.
func GetRepoHTTPClient(logger logr.Logger, repoURL string, insecure bool, creds Creds, proxyURL string, noProxy string) *http.Client {
	// Default HTTP client
	gitClientTimeout := ParseDurationFromEnv(logger, "KRATIX_GIT_REQUEST_TIMEOUT", 15*time.Second, 0, math.MaxInt64)
	customHTTPClient := &http.Client{
		// 15 second timeout by default
		Timeout: gitClientTimeout,
		// don't follow redirect
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	proxyFunc := getProxyCallback(proxyURL, noProxy)

	// Callback function to return any configured client certificate
	// We never return err, but an empty cert instead.
	clientCertFunc := func(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
		var err error
		cert := tls.Certificate{}

		// If we aren't called with GenericHTTPSCreds, then we just return an empty cert
		httpsCreds, ok := creds.(GenericHTTPSCreds)
		if !ok {
			return &cert, nil
		}

		// If the creds contain client certificate data, we return a TLS.Certificate
		// populated with the cert and its key.
		if httpsCreds.HasClientCert() {
			cert, err = tls.X509KeyPair([]byte(httpsCreds.GetClientCertData()), []byte(httpsCreds.GetClientCertKey()))
			if err != nil {
				logging.Error(logr.Discard(), err, "could not load client certificate")
				return &cert, nil
			}
		}

		return &cert, nil
	}
	transport := &http.Transport{
		Proxy: proxyFunc,
		TLSClientConfig: &tls.Config{
			// #nosec G402
			GetClientCertificate: clientCertFunc,
		},
		DisableKeepAlives: true,
	}
	customHTTPClient.Transport = transport
	if insecure {
		transport.TLSClientConfig.InsecureSkipVerify = true
		return customHTTPClient
	}
	parsedURL, err := url.Parse(repoURL)
	if err != nil {
		return customHTTPClient
	}
	serverCertificatePem, err := getCertificateForConnect(parsedURL.Host)
	if err != nil {
		return customHTTPClient
	}
	if len(serverCertificatePem) > 0 {
		certPool := getCertPoolFromPEMData(serverCertificatePem)
		transport.TLSClientConfig.RootCAs = certPool
	}
	return customHTTPClient
}

const envVarTLSDataPath = "KRATIX_TLS_DATA_PATH"

func getTLSCertificateDataPath() string {
	if envPath := os.Getenv(envVarTLSDataPath); envPath != "" {
		return envPath
	}
	return DefaultPathTLSConfig
}

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
	dataPath := getTLSCertificateDataPath()
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
	certPath := filepath.Join(getTLSCertificateDataPath(), serverNameWithoutPort(serverName))
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
func (m *nativeGitClient) Push(branch string) (string, error) {
	ctx := context.Background()

	if m.OnPush != nil {
		done := m.OnPush(m.repoURL)
		defer done()
	}

	err := m.runCredentialedCmd(ctx, "push", "origin", branch)
	if err != nil {
		return "", fmt.Errorf("failed to push: %w", err)
	}

	return "", nil
}

// CommitAndPush commits and pushes changes to the target branch.
func (m *nativeGitClient) CommitAndPush(branch, message, author string, email string) (string, error) {
	ctx := context.Background()
	out, err := m.runCmd(ctx, "add", ".")
	if err != nil {
		return out, fmt.Errorf("failed to add files: %w", err)
	}

	authorId := fmt.Sprintf("%s <%s>", author, email)
	out, err = m.runCmd(ctx,
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

	if m.OnPush != nil {
		done := m.OnPush(m.repoURL)
		defer done()
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

// runCmd is a convenience function to run a command in a given directory and return its output
func (m *nativeGitClient) runCmd(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	return m.runCmdOutput(cmd, runOpts{})
}

// runCredentialedCmd is a convenience function to run a git command with username/password credentials
func (m *nativeGitClient) runCredentialedCmd(ctx context.Context, args ...string) error {
	closer, environ, err := m.creds.Environ(m.log)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()

	// If a basic auth header is explicitly set, tell Git to send it to the
	// server to force use of basic auth instead of negotiating the auth scheme
	for _, e := range environ {
		if strings.HasPrefix(e, forceBasicAuthHeaderEnv+"=") {
			args = append([]string{"--config-env", "http.extraHeader=" + forceBasicAuthHeaderEnv}, args...)
		} else if strings.HasPrefix(e, bearerAuthHeaderEnv+"=") {
			args = append([]string{"--config-env", "http.extraHeader=" + bearerAuthHeaderEnv}, args...)
		}
	}

	if m.accessToken != "" {
		urlWithCreds, err := injectGitHubAppCredentials(m.repoURL, m.accessToken)
		if err != nil {

		}
		args = append([]string{"-c", fmt.Sprintf("url.'%s'.insteadOf='%s'", urlWithCreds, m.repoURL)}, args...)
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(cmd.Env, environ...)
	_, err = m.runCmdOutput(cmd, runOpts{})
	return err
}

func (m *nativeGitClient) runCmdOutput(cmd *exec.Cmd, ropts runOpts) (string, error) {
	cmd.Dir = m.root
	cmd.Env = append(os.Environ(), cmd.Env...)
	// Set $HOME to nowhere, so we can execute Git regardless of any external
	// authentication keys (e.g. in ~/.ssh) -- this is especially important for
	// running tests on local machines and/or CircleCI.
	cmd.Env = append(cmd.Env, "HOME=/dev/null")
	// Skip LFS for most Git operations except when explicitly requested
	cmd.Env = append(cmd.Env, "GIT_LFS_SKIP_SMUDGE=1")
	// Disable Git terminal prompts in case we're running with a tty
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=false")
	// Add Git configuration options that are essential for the command to work
	cmd.Env = append(cmd.Env, m.gitConfigEnv...)

	cmd.Env = append(cmd.Env,
		"GIT_ASKPASS=true",   // Disable password prompts by setting it to the binary `/bin/true`
		"GIT_CONFIG_COUNT=1", // Number of config settings
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=", // Disable credential helper
	)

	// For HTTPS repositories, we need to consider insecure repositories as well
	// as custom CA bundles from the cert database.
	if IsHTTPSURL(m.repoURL) {
		if m.insecure {
			cmd.Env = append(cmd.Env, "GIT_SSL_NO_VERIFY=true")
		} else {
			parsedURL, err := url.Parse(m.repoURL)
			// We don't fail if we cannot parse the URL, but log a warning in that
			// case. And we execute the command in a verbatim way.
			if err != nil {
				logging.Warn(m.log, "could not parse repo URL", "repoURL", m.repoURL)
			} else {
				caPath := getCertBundlePathForRepository(parsedURL.Host)
				if err == nil && caPath != "" {
					cmd.Env = append(cmd.Env, "GIT_SSL_CAINFO="+caPath)
				}
			}
		}
	}
	cmd.Env = upsertProxyEnv(cmd, m.proxy, m.noProxy)
	opts := ExecRunOpts{
		TimeoutBehavior: TimeoutBehavior{
			Signal:     syscall.SIGTERM,
			ShouldWait: true,
		},
		SkipErrorLogging: ropts.SkipErrorLogging,
		//CaptureStderr:    ropts.CaptureStderr,
		// TODO: restore to above
		CaptureStderr: true,
	}
	return RunWithExecRunOpts(cmd, opts, m.log)
}

// IsHTTPSURL returns true if supplied URL is HTTPS URL
func IsHTTPSURL(url string) bool {
	return httpsURLRegex.MatchString(url)
}

// SupportedSSHKeyExchangeAlgorithms is a list of all currently supported algorithms for SSH key exchange
// Unfortunately, crypto/ssh does not offer public constants or list for
// this.
var SupportedSSHKeyExchangeAlgorithms = []string{
	"curve25519-sha256",
	"curve25519-sha256@libssh.org",
	"ecdh-sha2-nistp256",
	"ecdh-sha2-nistp384",
	"ecdh-sha2-nistp521",
	"diffie-hellman-group-exchange-sha256",
	"diffie-hellman-group14-sha256",
	"diffie-hellman-group14-sha1",
}

// SupportedFIPSCompliantSSHKeyExchangeAlgorithms is a list of all currently supported algorithms for SSH key exchange
// that are FIPS compliant
var SupportedFIPSCompliantSSHKeyExchangeAlgorithms = []string{
	"ecdh-sha2-nistp256",
	"ecdh-sha2-nistp384",
	"ecdh-sha2-nistp521",
	"diffie-hellman-group-exchange-sha256",
	"diffie-hellman-group14-sha256",
}

// PublicKeysWithOptions is an auth method for go-git's SSH client that
// inherits from PublicKeys, but provides the possibility to override
// some client options.
type PublicKeysWithOptions struct {
	KexAlgorithms []string
	gitssh.PublicKeys
}

// Name returns the name of the auth method
func (a *PublicKeysWithOptions) Name() string {
	return gitssh.PublicKeysName
}

// String returns the configured user and auth method name as string
func (a *PublicKeysWithOptions) String() string {
	return fmt.Sprintf("user: %s, name: %s", a.User, a.Name())
}

// ClientConfig returns a custom SSH client configuration
func (a *PublicKeysWithOptions) ClientConfig() (*ssh.ClientConfig, error) {
	// Algorithms used for kex can be configured
	var kexAlgos []string
	if len(a.KexAlgorithms) > 0 {
		kexAlgos = a.KexAlgorithms
	} else {
		kexAlgos = getDefaultSSHKeyExchangeAlgorithms()
	}
	config := ssh.Config{KeyExchanges: kexAlgos}
	opts := &ssh.ClientConfig{Config: config, User: a.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(a.Signer)}}
	return a.SetHostKeyCallback(opts)
}

// getDefaultSSHKeyExchangeAlgorithms returns the default key exchange algorithms to be used
func getDefaultSSHKeyExchangeAlgorithms() []string {
	if fips140.Enabled() {
		return SupportedFIPSCompliantSSHKeyExchangeAlgorithms
	}
	return SupportedSSHKeyExchangeAlgorithms
}

// HasFileChanged returns the outout of git diff considering whether it is tracked or un-tracked
func (m *nativeGitClient) HasFileChanged(filePath string) (bool, error) {
	// Step 1: Is it UNTRACKED? (file is new to git)
	_, err := m.runCmd(context.Background(), "ls-files", "--error-unmatch", filePath)
	if err != nil {
		// File is NOT tracked by git → means it's new/unadded
		return true, nil
	}

	// use git diff --quiet and check exit code .. --cached is to consider files staged for deletion
	_, err = m.runCmd(context.Background(), "diff", "--quiet", "--", filePath)
	if err == nil {
		return false, nil // No changes
	}
	// Exit code 1 indicates: changes found
	if strings.Contains(err.Error(), "exit status 1") {
		return true, nil
	}
	// always return the actual wrapped error
	return false, fmt.Errorf("git diff failed: %w", err)
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

// Argo CD application related constants
const (

	// GithubAppCredsExpirationDuration is the default time used to cache the GitHub app credentials
	GithubAppCredsExpirationDuration = time.Minute * 60
)

// Environment variables for tuning and debugging Argo CD
const (
	// EnvGithubAppCredsExpirationDuration controls the caching of Github app credentials. This value is in minutes (default: 60)
	// #nosec G101
	EnvGithubAppCredsExpirationDuration = "KRATIX_GITHUB_APP_CREDS_EXPIRATION_DURATION"
)

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
