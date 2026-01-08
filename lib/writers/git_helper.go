package writers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/fips140"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
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
	"unicode"
	"unicode/utf8"

	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"github.com/syntasso/kratix/internal/logging"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	///////////////////////////////////////////////////////////
	certutil "github.com/argoproj/argo-cd/v3/util/cert"
	"github.com/argoproj/argo-cd/v3/util/env"
	executil "github.com/argoproj/argo-cd/v3/util/exec"
	"github.com/argoproj/argo-cd/v3/util/proxy"
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
	// CommitAndPush commits and pushes changes to the target branch.
	Clone() (*git.Repository, error)
	Checkout(revision string) (string, error)
	CommitAndPush(branch, message string) (string, error)
	Fetch(revision string, depth int64) error
	Init() (*git.Repository, error)
	Root() string
}

func NewGitClient(rawRepoURL string, root string, creds Creds, insecure bool, proxy string, noProxy string, opts ...ClientOpts) (GitClient, error) {

	client := &nativeGitClient{
		repoURL:      rawRepoURL,
		root:         root,
		creds:        creds,
		insecure:     insecure,
		proxy:        proxy,
		noProxy:      noProxy,
		gitConfigEnv: BuiltinGitConfigEnv,
	}
	for i := range opts {
		opts[i](client)
	}
	return client, nil
}

func Run(cmd *exec.Cmd) (string, error) {
	return RunWithRedactor(cmd, nil)
}

func RunWithRedactor(cmd *exec.Cmd, redactor func(text string) string) (string, error) {
	opts := ExecRunOpts{Redactor: redactor}
	return RunWithExecRunOpts(cmd, opts)
}

func RunWithExecRunOpts(cmd *exec.Cmd, opts ExecRunOpts) (string, error) {
	cmdOpts := CmdOpts{
		Timeout:          timeout,
		FatalTimeout:     fatalTimeout,
		Redactor:         opts.Redactor,
		TimeoutBehavior:  opts.TimeoutBehavior,
		SkipErrorLogging: opts.SkipErrorLogging,
		CaptureStderr:    opts.CaptureStderr}
	return RunCommandExt(cmd, cmdOpts)
}

// GetCommandArgsToLog represents the given command in a way that we can copy-and-paste into a terminal
func GetCommandArgsToLog(cmd *exec.Cmd) string {
	var argsToLog []string
	for _, arg := range cmd.Args {
		if arg == "" {
			argsToLog = append(argsToLog, `""`)
			continue
		}

		containsSpace := false
		for _, r := range arg {
			if unicode.IsSpace(r) {
				containsSpace = true
				break
			}
		}
		if containsSpace {
			// add quotes and escape any internal quotes
			argsToLog = append(argsToLog, strconv.Quote(arg))
		} else {
			argsToLog = append(argsToLog, arg)
		}
	}
	args := strings.Join(argsToLog, " ")
	return args
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
func RunCommandExt(cmd *exec.Cmd, opts CmdOpts) (string, error) {
	execId, err := RandHex(5)
	if err != nil {
		return "", err
	}
	logCtx := logrus.WithFields(logrus.Fields{"execID": execId})

	redactor := DefaultCmdOpts.Redactor
	if opts.Redactor != nil {
		redactor = opts.Redactor
	}

	// log in a way we can copy-and-paste into a terminal
	args := strings.Join(cmd.Args, " ")
	logCtx.WithFields(logrus.Fields{"dir": cmd.Dir}).Info(redactor(args))

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
				logCtx.WithFields(logrus.Fields{"duration": time.Since(start)}).Debug(redactor(output))
				err = newCmdError(redactor(args), fmt.Errorf("fatal timeout after %v", timeout+fatalTimeout), "")
				logCtx.Error(err.Error())
				return strings.TrimSuffix(output, "\n"), err
			}
		}
		// either did not wait for timeout or cmd did respect SIGTERM
		output := stdout.String()
		if opts.CaptureStderr {
			output += stderr.String()
		}
		logCtx.WithFields(logrus.Fields{"duration": time.Since(start)}).Debug(redactor(output))
		err = newCmdError(redactor(args), fmt.Errorf("timeout after %v", timeout), "")
		logCtx.Error(err.Error())
		return strings.TrimSuffix(output, "\n"), err
	case err := <-done:
		if err != nil {
			output := stdout.String()
			if opts.CaptureStderr {
				output += stderr.String()
			}
			logCtx.WithFields(logrus.Fields{"duration": time.Since(start)}).Debug(redactor(output))
			err := newCmdError(redactor(args), errors.New(redactor(err.Error())), strings.TrimSpace(redactor(stderr.String())))
			if !opts.SkipErrorLogging {
				logCtx.Error(err.Error())
			}
			return strings.TrimSuffix(output, "\n"), err
		}
	}
	output := stdout.String()
	if opts.CaptureStderr {
		output += stderr.String()
	}
	logCtx.WithFields(logrus.Fields{"duration": time.Since(start)}).Debug(redactor(output))

	return strings.TrimSuffix(output, "\n"), nil
}

func RunCommand(name string, opts CmdOpts, arg ...string) (string, error) {
	return RunCommandExt(exec.CommandContext(context.Background(), name, arg...), opts)
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

// RevisionReference contains a reference to a some information that is related in some way to another commit. For now,
// it supports only references to a commit. In the future, it may support other types of references.
type RevisionReference struct {
	// Commit contains metadata about the commit that is related in some way to another commit.
	Commit *CommitMetadata
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
}

type runOpts struct {
	SkipErrorLogging bool
	CaptureStderr    bool
}

var (
	maxAttemptsCount = 1
	maxRetryDuration time.Duration
	retryDuration    time.Duration
	factor           int64
)

// TODO: move it to constructor
func init() {
	if countStr := os.Getenv(EnvGitAttemptsCount); countStr != "" {
		cnt, err := strconv.Atoi(countStr)
		if err != nil {
			panic(fmt.Sprintf("Invalid value in %s env variable: %v", EnvGitAttemptsCount, err))
		}
		maxAttemptsCount = int(math.Max(float64(cnt), 1))
	}

	maxRetryDuration = env.ParseDurationFromEnv(EnvGitRetryMaxDuration, DefaultGitRetryMaxDuration, 0, math.MaxInt64)
	retryDuration = env.ParseDurationFromEnv(EnvGitRetryDuration, DefaultGitRetryDuration, 0, math.MaxInt64)
	factor = env.ParseInt64FromEnv(EnvGitRetryFactor, DefaultGitRetryFactor, 0, math.MaxInt64)

	BuiltinGitConfigEnv = append(BuiltinGitConfigEnv, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(builtinGitConfig)))
	idx := 0
	for k, v := range builtinGitConfig {
		BuiltinGitConfigEnv = append(BuiltinGitConfigEnv, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", idx, k))
		BuiltinGitConfigEnv = append(BuiltinGitConfigEnv, fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", idx, v))
		idx++
	}
}

type ClientOpts func(c *nativeGitClient)

// WithCache sets git revisions cacher as well as specifies if client should tries to use cached resolved revision
func WithCache(cache gitRefCache, loadRefFromCache bool) ClientOpts {
	return func(c *nativeGitClient) {
		c.gitRefCache = cache
		c.loadRefFromCache = loadRefFromCache
	}
}

func WithBuiltinGitConfig(enable bool) ClientOpts {
	return func(c *nativeGitClient) {
		if enable {
			c.gitConfigEnv = BuiltinGitConfigEnv
		} else {
			c.gitConfigEnv = nil
		}
	}
}

// WithEventHandlers sets the git client event handlers
func WithEventHandlers(handlers EventHandlers) ClientOpts {
	return func(c *nativeGitClient) {
		c.EventHandlers = handlers
	}
}

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
				log.Errorf("Could not load Client Certificate: %v", err)
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

func newAuth(repoURL string, creds Creds) (transport.AuthMethod, error) {
	switch creds := creds.(type) {
	case SSHCreds:
		var sshUser string
		if isSSH, user := IsSSHURL(repoURL); isSSH {
			sshUser = user
		}
		signer, err := ssh.ParsePrivateKey([]byte(creds.sshPrivateKey))
		if err != nil {
			return nil, err
		}
		auth := &PublicKeysWithOptions{}
		auth.User = sshUser
		auth.Signer = signer
		if creds.insecure {
			auth.HostKeyCallback = ssh.InsecureIgnoreHostKey()
		} else {
			// Set up validation of SSH known hosts for using our ssh_known_hosts
			// file.
			auth.HostKeyCallback, err = knownhosts.New(certutil.GetSSHKnownHostsDataPath())
			if err != nil {
				log.Errorf("Could not set-up SSH known hosts callback: %v", err)
			}
		}
		return auth, nil
	case HTTPSCreds:
		if creds.bearerToken != "" {
			return &githttp.TokenAuth{Token: creds.bearerToken}, nil
		}
		auth := githttp.BasicAuth{Username: creds.username, Password: creds.password}
		if auth.Username == "" {
			auth.Username = "x-access-token"
		}
		return &auth, nil
	case GitHubAppCreds:
		token, err := creds.getAccessToken()
		if err != nil {
			return nil, err
		}
		auth := githttp.BasicAuth{Username: "x-access-token", Password: token}
		return &auth, nil
	}

	return nil, nil
}

func (m *nativeGitClient) Root() string {
	return m.root
}

// Init initializes a local git repository and sets the remote origin
func (m *nativeGitClient) Init() (*git.Repository, error) {

	var err error
	m.root, err = createLocalDirectory(m.log)
	if err != nil {
		logging.Error(m.log, err, "could not create temporary repository directory")
		return nil, err
	}

	repo, err := git.PlainOpen(m.root)
	// repo already exists
	if err == nil {
		return repo, nil
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, err
	}

	// create repo locally
	logging.Debug(m.log, "initialising repo %s to %s", m.repoURL, m.root)
	err = os.RemoveAll(m.root)
	if err != nil {
		return nil, fmt.Errorf("unable to clean repo at %s: %w", m.root, err)
	}
	err = os.MkdirAll(m.root, 0o755)
	if err != nil {
		return nil, err
	}
	repo, err = git.PlainInit(m.root, false)
	if err != nil {
		return nil, err
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: git.DefaultRemoteName,
		URLs: []string{m.repoURL},
	})
	return repo, err
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

// Fetch downloads commits, branches, and tags from the remote repository
// without modifying the working directory or current branch. Updates remote-tracking
// branches (e.g., origin/main) to reflect the current state of the remote.
//
// Parameters:
//
//	revision: Specific branch/tag/commit to fetch (empty string fetches all)
//	depth: Number of commits to fetch (0 for full history)
//
// Flags used:
//
//	--force: Allow non-fast-forward updates (handles force pushes)
//	--prune: Remove remote-tracking branches that no longer exist on remote
//	--tags: Fetch all tags (only when depth == 0, as tags don't work well with shallow clones)
func (m *nativeGitClient) Fetch(revision string, depth int64) error {
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
// and tags, sets up remote tracking, and checks out the default branch. This is
// a one-time setup operation for getting a repository for the first time.
//
// Common flags:
//
//	--depth N: Create shallow clone with only N commits of history
//	--branch: Clone specific branch instead of default
//	--single-branch: Only clone one branch
func (m *nativeGitClient) Clone() (*git.Repository, error) {

	logging.Debug(m.log, "cloning repo")

	repo, err := m.Init()
	if err != nil {
		return nil, err
	}
	err = m.Fetch("main", 0)
	if err != nil {
		return nil, err
	}
	out, err := m.Checkout("main")
	if err != nil {
		logging.Error(m.log, err, "could not clone repo: %v", out)
		return nil, err
	}

	return repo, nil
}

func getGitTags(refs []*plumbing.Reference) []string {
	var tags []string
	for _, ref := range refs {
		if ref.Name().IsTag() {
			tags = append(tags, ref.Name().Short())
		}
	}
	return tags
}

func truncate(str string) string {
	if utf8.RuneCountInString(str) > 100 {
		return string([]rune(str)[0:97]) + "..."
	}
	return str
}

var shaRegex = regexp.MustCompile(`^[0-9a-f]{5,40}$`)

// GetReferences extracts related commit metadata from the commit message trailers. If referenced commit
// metadata is present, we return a slice containing a single metadata object. If no related commit metadata is found,
// we return a nil slice.
//
// If a trailer fails validation, we log an error and skip that trailer. We truncate the trailer values to 100
// characters to avoid excessively long log messages.
//
// We also return the commit message body with all valid Argocd-reference-commit-* trailers removed.
func GetReferences(logCtx *log.Entry, commitMessageBody string) ([]RevisionReference, string) {
	unrelatedLines := strings.Builder{}
	var relatedCommit CommitMetadata
	scanner := bufio.NewScanner(strings.NewReader(commitMessageBody))
	for scanner.Scan() {
		line := scanner.Text()
		updated := updateCommitMetadata(logCtx, &relatedCommit, line)
		if !updated {
			unrelatedLines.WriteString(line + "\n")
		}
	}
	var relatedCommits []RevisionReference
	if relatedCommit != (CommitMetadata{}) {
		relatedCommits = append(relatedCommits, RevisionReference{
			Commit: &relatedCommit,
		})
	}
	return relatedCommits, unrelatedLines.String()
}

// updateCommitMetadata checks if the line is a valid Argocd-reference-commit-* trailer. If so, it updates
// the relatedCommit object and returns true. If the line is not a valid trailer, it returns false.
func updateCommitMetadata(logCtx *log.Entry, relatedCommit *CommitMetadata, line string) bool {
	if !strings.HasPrefix(line, "Argocd-reference-commit-") {
		return false
	}
	parts := strings.SplitN(line, ": ", 2)
	if len(parts) != 2 {
		return false
	}
	trailerKey := parts[0]
	trailerValue := parts[1]
	switch trailerKey {
	case "Argocd-reference-commit-repourl":
		_, err := url.Parse(trailerValue)
		if err != nil {
			logCtx.Errorf("failed to parse repo URL %q: %v", truncate(trailerValue), err)
			return false
		}
		relatedCommit.RepoURL = trailerValue
	case "Argocd-reference-commit-author":
		address, err := mail.ParseAddress(trailerValue)
		if err != nil || address == nil {
			logCtx.Errorf("failed to parse author email %q: %v", truncate(trailerValue), err)
			return false
		}
		relatedCommit.Author = *address
	case "Argocd-reference-commit-date":
		// Validate that it's the correct date format.
		t, err := time.Parse(time.RFC3339, trailerValue)
		if err != nil {
			logCtx.Errorf("failed to parse date %q with RFC3339 format: %v", truncate(trailerValue), err)
			return false
		}
		relatedCommit.Date = t.Format(time.RFC3339)
	case "Argocd-reference-commit-subject":
		relatedCommit.Subject = trailerValue
	case "Argocd-reference-commit-body":
		body := ""
		err := json.Unmarshal([]byte(trailerValue), &body)
		if err != nil {
			logCtx.Errorf("failed to parse body %q as JSON: %v", truncate(trailerValue), err)
			return false
		}
		relatedCommit.Body = body
	case "Argocd-reference-commit-sha":
		if !shaRegex.MatchString(trailerValue) {
			logCtx.Errorf("invalid commit SHA %q in trailer %s: must be a lowercase hex string 5-40 characters long", truncate(trailerValue), trailerKey)
			return false
		}
		relatedCommit.SHA = trailerValue
	default:
		return false
	}
	return true
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

// CommitAndPush commits and pushes changes to the target branch.
func (m *nativeGitClient) CommitAndPush(branch, message string) (string, error) {
	ctx := context.Background()
	out, err := m.runCmd(ctx, "add", ".")
	if err != nil {
		return out, fmt.Errorf("failed to add files: %w", err)
	}

	out, err = m.runCmd(ctx, "commit", "-m", message)
	if err != nil {
		if strings.Contains(out, "nothing to commit, working tree clean") {
			return out, nil
		}
		return out, fmt.Errorf("failed to commit: %w", err)
	}

	if m.OnPush != nil {
		done := m.OnPush(m.repoURL)
		defer done()
	}

	err = m.runCredentialedCmd(ctx, "push", "origin", branch)
	if err != nil {
		return "", fmt.Errorf("failed to push: %w", err)
	}

	return "", nil
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
				log.Warnf("runCmdOutput: Could not parse repo URL '%s'", m.repoURL)
			} else {
				caPath, err := certutil.GetCertBundlePathForRepository(parsedURL.Host)
				if err == nil && caPath != "" {
					cmd.Env = append(cmd.Env, "GIT_SSL_CAINFO="+caPath)
				}
			}
		}
	}
	cmd.Env = proxy.UpsertEnv(cmd, m.proxy, m.noProxy)
	opts := executil.ExecRunOpts{
		TimeoutBehavior: executil.TimeoutBehavior{
			Signal:     syscall.SIGTERM,
			ShouldWait: true,
		},
		SkipErrorLogging: ropts.SkipErrorLogging,
		CaptureStderr:    ropts.CaptureStderr,
	}
	return executil.RunWithExecRunOpts(cmd, opts)
}

const (
	// EnvVarSSODebug is an environment variable to enable additional OAuth debugging in the API server
	EnvVarSSODebug = "ARGOCD_SSO_DEBUG"
	// EnvVarRBACDebug is an environment variable to enable additional RBAC debugging in the API server
	EnvVarRBACDebug = "ARGOCD_RBAC_DEBUG"
	// EnvVarSSHDataPath overrides the location where SSH known hosts for repo access data is stored
	EnvVarSSHDataPath = "KRATIX_SSH_DATA_PATH"
	// EnvVarTLSDataPath overrides the location where TLS certificate for repo access data is stored
	EnvVarTLSDataPath = "ARGOCD_TLS_DATA_PATH"
	// EnvGitAttemptsCount specifies number of git remote operations attempts count
	EnvGitAttemptsCount = "ARGOCD_GIT_ATTEMPTS_COUNT"
	// EnvGitRetryMaxDuration specifies max duration of git remote operation retry
	EnvGitRetryMaxDuration = "ARGOCD_GIT_RETRY_MAX_DURATION"
	// EnvGitRetryDuration specifies duration of git remote operation retry
	EnvGitRetryDuration = "ARGOCD_GIT_RETRY_DURATION"
	// EnvGitRetryFactor specifies factor of git remote operation retry
	EnvGitRetryFactor = "ARGOCD_GIT_RETRY_FACTOR"
	// EnvGnuPGHome is the path to ArgoCD's GnuPG keyring for signature verification
	EnvGnuPGHome = "ARGOCD_GNUPGHOME"
	// EnvWatchAPIBufferSize is the buffer size used to transfer K8S watch events to watch API consumer
	EnvWatchAPIBufferSize = "ARGOCD_WATCH_API_BUFFER_SIZE"
	// EnvPauseGenerationAfterFailedAttempts will pause manifest generation after the specified number of failed generation attempts
	EnvPauseGenerationAfterFailedAttempts = "ARGOCD_PAUSE_GEN_AFTER_FAILED_ATTEMPTS"
	// EnvPauseGenerationMinutes pauses manifest generation for the specified number of minutes, after sufficient manifest generation failures
	EnvPauseGenerationMinutes = "ARGOCD_PAUSE_GEN_MINUTES"
	// EnvPauseGenerationRequests pauses manifest generation for the specified number of requests, after sufficient manifest generation failures
	EnvPauseGenerationRequests = "ARGOCD_PAUSE_GEN_REQUESTS"
	// EnvControllerReplicas is the number of controller replicas
	EnvControllerReplicas = "ARGOCD_CONTROLLER_REPLICAS"
	// EnvControllerHeartbeatTime will update the heartbeat for application controller to claim shard
	EnvControllerHeartbeatTime = "ARGOCD_CONTROLLER_HEARTBEAT_TIME"
	// EnvControllerShard is the shard number that should be handled by controller
	EnvControllerShard = "ARGOCD_CONTROLLER_SHARD"
	// EnvControllerShardingAlgorithm is the distribution sharding algorithm to be used: legacy or round-robin
	EnvControllerShardingAlgorithm = "ARGOCD_CONTROLLER_SHARDING_ALGORITHM"
	// EnvEnableDynamicClusterDistribution enables dynamic sharding (ALPHA)
	EnvEnableDynamicClusterDistribution = "ARGOCD_ENABLE_DYNAMIC_CLUSTER_DISTRIBUTION"
	// EnvEnableGRPCTimeHistogramEnv enables gRPC metrics collection
	EnvEnableGRPCTimeHistogramEnv = "ARGOCD_ENABLE_GRPC_TIME_HISTOGRAM"
	// EnvGithubAppCredsExpirationDuration controls the caching of Github app credentials. This value is in minutes (default: 60)
	EnvGithubAppCredsExpirationDuration = "ARGOCD_GITHUB_APP_CREDS_EXPIRATION_DURATION"
	// EnvHelmIndexCacheDuration controls how the helm repository index file is cached for (default: 0)
	EnvHelmIndexCacheDuration = "ARGOCD_HELM_INDEX_CACHE_DURATION"
	// EnvAppConfigPath allows to override the configuration path for repo server
	EnvAppConfigPath = "ARGOCD_APP_CONF_PATH"
	// EnvAuthToken is the environment variable name for the auth token used by the CLI
	EnvAuthToken = "ARGOCD_AUTH_TOKEN"
	// EnvLogFormat log format that is defined by `--logformat` option
	EnvLogFormat = "ARGOCD_LOG_FORMAT"
	// EnvLogLevel log level that is defined by `--loglevel` option
	EnvLogLevel = "ARGOCD_LOG_LEVEL"
	// EnvLogFormatEnableFullTimestamp enables the FullTimestamp option in logs
	EnvLogFormatEnableFullTimestamp = "ARGOCD_LOG_FORMAT_ENABLE_FULL_TIMESTAMP"
	// EnvLogFormatTimestamp is the timestamp format used in logs
	EnvLogFormatTimestamp = "ARGOCD_LOG_FORMAT_TIMESTAMP"
	// EnvMaxCookieNumber max number of chunks a cookie can be broken into
	EnvMaxCookieNumber = "ARGOCD_MAX_COOKIE_NUMBER"
	// EnvPluginSockFilePath allows to override the pluginSockFilePath for repo server and cmp server
	EnvPluginSockFilePath = "ARGOCD_PLUGINSOCKFILEPATH"
	// EnvCMPChunkSize defines the chunk size in bytes used when sending files to the cmp server
	EnvCMPChunkSize = "ARGOCD_CMP_CHUNK_SIZE"
	// EnvCMPWorkDir defines the full path of the work directory used by the CMP server
	EnvCMPWorkDir = "ARGOCD_CMP_WORKDIR"
	// EnvGPGDataPath overrides the location where GPG keyring for signature verification is stored
	EnvGPGDataPath = "ARGOCD_GPG_DATA_PATH"
	// EnvServer is the server address of the Argo CD API server.
	EnvServer = "ARGOCD_SERVER"
	// EnvServerName is the name of the Argo CD server component, as specified by the value under the LabelKeyAppName label key.
	EnvServerName = "ARGOCD_SERVER_NAME"
	// EnvRepoServerName is the name of the Argo CD repo server component, as specified by the value under the LabelKeyAppName label key.
	EnvRepoServerName = "ARGOCD_REPO_SERVER_NAME"
	// EnvAppControllerName is the name of the Argo CD application controller component, as specified by the value under the LabelKeyAppName label key.
	EnvAppControllerName = "ARGOCD_APPLICATION_CONTROLLER_NAME"
	// EnvRedisName is the name of the Argo CD redis component, as specified by the value under the LabelKeyAppName label key.
	EnvRedisName = "ARGOCD_REDIS_NAME"
	// EnvRedisHaProxyName is the name of the Argo CD Redis HA proxy component, as specified by the value under the LabelKeyAppName label key.
	EnvRedisHaProxyName = "ARGOCD_REDIS_HAPROXY_NAME"
	// EnvGRPCKeepAliveMin defines the GRPCKeepAliveEnforcementMinimum, used in the grpc.KeepaliveEnforcementPolicy. Expects a "Duration" format (e.g. 10s).
	EnvGRPCKeepAliveMin = "ARGOCD_GRPC_KEEP_ALIVE_MIN"
	// EnvServerSideDiff defines the env var used to enable ServerSide Diff feature.
	// If defined, value must be "true" or "false".
	EnvServerSideDiff = "ARGOCD_APPLICATION_CONTROLLER_SERVER_SIDE_DIFF"
	// EnvGRPCMaxSizeMB is the environment variable to look for a max GRPC message size
	EnvGRPCMaxSizeMB = "ARGOCD_GRPC_MAX_SIZE_MB"

	DefaultGitRetryMaxDuration time.Duration = time.Second * 5        // 5s
	DefaultGitRetryDuration    time.Duration = time.Millisecond * 250 // 0.25s
	DefaultGitRetryFactor                    = int64(2)
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

var (
	commitSHARegex = regexp.MustCompile("^[0-9A-Fa-f]{40}$")
	sshURLRegex    = regexp.MustCompile("^(ssh://)?([^/:]*?)@[^@]+$")
	httpsURLRegex  = regexp.MustCompile("^(https://).*")
	httpURLRegex   = regexp.MustCompile("^(http://).*")
)

// IsSSHURL returns true if supplied URL is SSH URL
func IsSSHURL(url string) (bool, string) {
	matches := sshURLRegex.FindStringSubmatch(url)
	if len(matches) > 2 {
		return true, matches[2]
	}
	return false, ""
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
