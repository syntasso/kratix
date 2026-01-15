package writers

import (
	"bytes"
	"context"
	"crypto/fips140"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-git/v5/plumbing"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-logr/logr"
	gocache "github.com/patrickmn/go-cache"
	"github.com/syntasso/kratix/internal/logging"
	"golang.org/x/crypto/ssh"

	// TODO: review these imports
	"github.com/argoproj/argo-cd/v3/common"
	certutil "github.com/argoproj/argo-cd/v3/util/cert"
	"github.com/argoproj/argo-cd/v3/util/env"
	"github.com/argoproj/argo-cd/v3/util/proxy"
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

	githubAppCredsExp := common.GithubAppCredsExpirationDuration
	if exp := os.Getenv(common.EnvGithubAppCredsExpirationDuration); exp != "" {
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

// GitClient is a generic git client interface
type GitClient interface {
	Clone(string) (string, error)
	Checkout(revision string) (string, error)
	CommitAndPush(branch, message, author, email string) (string, error)
	Push(branch string) (string, error)
	Fetch(revision string, depth int64) error
	Init() (string, error)
	Root() string
}

type GitClientRequest struct {
	RawRepoURL string
	Root       string
	Auth       *GitAuth
	Insecure   bool
	Proxy      string
	NoProxy    string
	Opts       []ClientOpts
	log        logr.Logger
}

var (
	sshURLRegex   = regexp.MustCompile("^(ssh://)?([^/:]*?)@[^@]+$")
	httpsURLRegex = regexp.MustCompile("^(https://).*")
	httpURLRegex  = regexp.MustCompile("^(http://).*")
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

func NewGitClient(req GitClientRequest) (*nativeGitClient, error) {

	var accessToken string

	switch req.Auth.Creds.(type) {
	case SSHCreds:
		if ok, _ := IsSSHURL(req.RawRepoURL); !ok {
			return nil, fmt.Errorf("invalid URL for SSH auth method: %s", req.RawRepoURL)
		}

	// TODO: do we still need this?
	case GitHubAppCreds:
		/*
			tokenAuth, ok := req.GitAuth.AuthMethod.(*githttp.TokenAuth)
			if !ok {
				return nil, fmt.Errorf("GitHub app auth method is not *githttp.TokenAuth")
			}
			if tokenAuth == nil {
				return nil, fmt.Errorf("auth token not set")
			}
			accessToken = tokenAuth.Token
		*/
		/*
			// THIS makes the last githubapp auth test pass
			// but we need to not store the token on a local file,
			// let's try to fix change the url temporarily or use extra auth
			// headers
			// i've tried using insteadOf in git cli, but it's not running fine in
			// go, while it works when run manually, maybe a difference in the
			// env???? try to check the git env vars and their differences between
			// the two methods
				req.RawRepoURL, err = injectCredentials(req.RawRepoURL, "x-access-token", tokenAuth.Token)
				if !ok {
					return nil, fmt.Errorf("failed to inject credentials into repository URL: %w", err)
				}
		*/
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
		log:          req.log,
	}
	for i := range req.Opts {
		req.Opts[i](client)
	}

	return client, nil
}

func Run(cmd *exec.Cmd, logger logr.Logger) (string, error) {
	return RunWithRedactor(cmd, nil, logger)
}

func RunWithRedactor(cmd *exec.Cmd, redactor func(text string) string, logger logr.Logger) (string, error) {
	opts := ExecRunOpts{Redactor: redactor}
	return RunWithExecRunOpts(cmd, opts, logger)
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

// TimeoutBehavior defines behavior for when the command takes longer than the passed in timeout to exit
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

func RunCommand(name string, opts CmdOpts, logger logr.Logger, arg ...string) (string, error) {
	return RunCommandExt(exec.CommandContext(context.Background(), name, arg...), opts, logger)
}

/////////////////////

var (
	ErrInvalidRepoURL = errors.New("repo URL is invalid")
	ErrNoNoteFound    = errors.New("no note found")
)

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
	// unless it is properly validated and/or sanitized first.
	RepoURL string
}

// this should match reposerver/repository/repository.proto/RefsList
type Refs struct {
	Branches []string
	Tags     []string
	// heads and remotes are also refs, but are not needed at this time.
}

type gitRefCache interface {
	SetGitReferences(repo string, references []*plumbing.Reference) error
	GetOrLockGitReferences(repo string, lockId string, references *[]*plumbing.Reference) (string, error)
	UnlockGitReferences(repo string, lockId string) error
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
	// gitRefCache knows how to cache git refs
	gitRefCache gitRefCache
	// indicates if client allowed to load refs from cache
	loadRefFromCache bool
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

var gitClientTimeout = env.ParseDurationFromEnv("ARGOCD_GIT_REQUEST_TIMEOUT", 15*time.Second, 0, math.MaxInt64)

// Returns a HTTP client object suitable for go-git to use using the following
// pattern:
//   - If insecure is true, always returns a client with certificate verification
//     turned off.
//   - If one or more custom certificates are stored for the repository, returns
//     a client with those certificates in the list of root CAs used to verify
//     the server's certificate.
//   - Otherwise (and on non-fatal errors), a default HTTP client is returned.
func GetRepoHTTPClient(repoURL string, insecure bool, creds Creds, proxyURL string, noProxy string) *http.Client {
	// Default HTTP client
	customHTTPClient := &http.Client{
		// 15 second timeout by default
		Timeout: gitClientTimeout,
		// don't follow redirect
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	proxyFunc := proxy.GetCallback(proxyURL, noProxy)

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
	serverCertificatePem, err := certutil.GetCertificateForConnect(parsedURL.Host)
	if err != nil {
		return customHTTPClient
	}
	if len(serverCertificatePem) > 0 {
		certPool := certutil.GetCertPoolFromPEMData(serverCertificatePem)
		transport.TLSClientConfig.RootCAs = certPool
	}
	return customHTTPClient
}

func (m *nativeGitClient) Root() string {
	return m.root
}

// Init initializes a local git repository and sets the remote origin
func (m *nativeGitClient) Init() (string, error) {
	ctx := context.Background()

	var err error
	m.root, err = createLocalDirectory(m.log)
	if err != nil {
		logging.Error(m.log, err, "could not create temporary repository directory")
		return "", err
	}

	logging.Debug(m.log, "initialising repo %s to %s", m.repoURL, m.root)
	//	err = os.RemoveAll(m.root)
	if err != nil {
		return "", fmt.Errorf("unable to clean repo at %s: %w", m.root, err)
	}
	err = os.MkdirAll(m.root, 0o755)
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

func truncate(str string) string {
	if utf8.RuneCountInString(str) > 100 {
		return string([]rune(str)[0:97]) + "..."
	}
	return str
}

// config runs a git config command.
func (m *nativeGitClient) config(ctx context.Context, args ...string) (string, error) {
	args = append([]string{"config"}, args...)
	out, err := m.runCmd(ctx, args...)
	if err != nil {
		return out, fmt.Errorf("failed to run git config: %w", err)
	}
	return out, nil
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
	///////////////////////////////
	err = m.runCredentialedCmd(ctx, "pull", "origin", "--rebase")
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
	closer, environ, err := m.creds.Environ()
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
	// Add Git configuration options that are essential for ArgoCD operation
	cmd.Env = append(cmd.Env, m.gitConfigEnv...)

	cmd.Env = append(cmd.Env,
		// TODO; revisit as we have GIT_TERMINAL_PROMPT above
		// "GIT_TERMINAL_PROMPT=0", // Disable terminal prompts
		"GIT_ASKPASS=true",   // Disable password prompts by setting it to the binary `true``
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
				caPath, err := certutil.GetCertBundlePathForRepository(parsedURL.Host)
				if err == nil && caPath != "" {
					cmd.Env = append(cmd.Env, "GIT_SSL_CAINFO="+caPath)
				}
			}
		}
	}
	cmd.Env = proxy.UpsertEnv(cmd, m.proxy, m.noProxy)
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

const (
	EnvVarGitSshInsecure = "KRATIX_GIT_SSH_INSECURE"

	// EnvVarSSHDataPath overrides the location where SSH known hosts for repo access data is stored
	EnvVarSSHDataPath = "KRATIX_SSH_DATA_PATH"
)

// EnsurePrefix idempotently ensures that a base string has a given prefix.
func ensurePrefix(s, prefix string) string {
	if !strings.HasPrefix(s, prefix) {
		s = prefix + s
	}
	return s
}

func NormalizeGitURL(repo string) string {
	repo = strings.ToLower(strings.TrimSpace(repo))
	if yes, _ := IsSSHURL(repo); yes {
		if !strings.HasPrefix(repo, "ssh://") {
			// We need to replace the first colon in git@server... style SSH URLs with a slash, otherwise
			// net/url.Parse will interpret it incorrectly as the port.
			repo = strings.Replace(repo, ":", "/", 1)
			repo = ensurePrefix(repo, "ssh://")
		}
	}
	repo = strings.TrimSuffix(repo, ".git")
	repoURL, err := url.Parse(repo)
	if err != nil {
		return ""
	}
	normalized := repoURL.String()
	return strings.TrimPrefix(normalized, "ssh://")
}

// IsHTTPSURL returns true if supplied URL is HTTPS URL
func IsHTTPSURL(url string) bool {
	return httpsURLRegex.MatchString(url)
}

// IsHTTPURL returns true if supplied URL is HTTP URL
func IsHTTPURL(url string) bool {
	return httpURLRegex.MatchString(url)
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
