package git

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	giturls "github.com/chainguard-dev/git-urls"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-logr/logr"

	tx_ssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v69/github"
	"github.com/syntasso/kratix/api/v1alpha1"

	gocache "github.com/patrickmn/go-cache"

	"github.com/bradleyfalzon/ghinstallation/v2"
	log "github.com/sirupsen/logrus"
)

var (
	// In memory cache for storing github APP api token credentials
	githubAppTokenCache *gocache.Cache

	// installationIdCache caches installation IDs for organisations to avoid redundant API calls.
	githubInstallationIdCache      *gocache.Cache
	githubInstallationIdCacheMutex sync.RWMutex // For bulk API call coordination
)

const (
	// The x-access-token username is actually just a convention - it's not validated by GitHub.
	// GitHub ignores the username field entirely when a valid token is in the password field.
	// You could use any string (or even empty string) as the username, but x-access-token
	// is recommended for clarity.
	githubAccessTokenUsername = "x-access-token"
	forceBasicAuthHeaderEnv   = "KRATIX_GIT_AUTH_HEADER"
	// #nosec G101
	bearerAuthHeaderEnv = "KRATIX_GIT_BEARER_AUTH_HEADER"
)

var tempDir = defaultTempDir()

// TODO: rename
type Auth struct {
	transport.AuthMethod
	Creds
}

type NoopCredsStore struct{}

func (d NoopCredsStore) Add(_ string, _ string) string {
	return ""
}

func (d NoopCredsStore) Remove(_ string) {
}

func (d NoopCredsStore) Environ(_ string) []string {
	return []string{}
}

type CredsStore interface {
	Add(username string, password string) string
	Remove(id string)
	// Environ returns the environment variables that should be set to use the credentials for the given credential ID.
	Environ(id string) []string
}

type Creds interface {
	Environ(logger logr.Logger) (io.Closer, []string, error)
	// GetUserInfo gets the username and email address for the credentials, if they're available.
	GetUserInfo(ctx context.Context, logger logr.Logger) (string, string, error)
}

// nop implementation
type NopCloser struct{}

func (c NopCloser) Close() error {
	return nil
}

var _ Creds = NopCreds{}

type NopCreds struct{}

func (c NopCreds) Environ(_ logr.Logger) (io.Closer, []string, error) {
	return NopCloser{}, nil, nil
}

// GetUserInfo returns empty strings for user info
func (c NopCreds) GetUserInfo(_ context.Context, _ logr.Logger) (name string, email string, err error) {
	return "", "", nil
}

var _ io.Closer = NopCloser{}

type GenericHTTPSCreds interface {
	HasClientCert() bool
	GetClientCertData() string
	GetClientCertKey() string
	Creds
}

var (
	_ GenericHTTPSCreds = HTTPSCreds{}
	_ Creds             = HTTPSCreds{}
)

// HTTPS creds implementation
type HTTPSCreds struct {
	// Username for authentication
	username string
	// Password for authentication
	password string
	// Bearer token for authentication
	bearerToken string
	// Whether to ignore invalid server certificates
	insecure bool
	// Client certificate to use
	clientCertData string
	// Client certificate key to use
	clientCertKey string
	// temporal credentials store
	store CredsStore
	// whether to force usage of basic auth
	forceBasicAuth bool
}

func NewHTTPSCreds(username string, password string, bearerToken string, clientCertData string, clientCertKey string, insecure bool, store CredsStore, forceBasicAuth bool) GenericHTTPSCreds {
	return HTTPSCreds{
		username,
		password,
		bearerToken,
		insecure,
		clientCertData,
		clientCertKey,
		store,
		forceBasicAuth,
	}
}

// GetUserInfo returns the username and email address for the credentials, if they're available.
func (creds HTTPSCreds) GetUserInfo(_ context.Context, _ logr.Logger) (string, string, error) {
	// Email not implemented for HTTPS creds.
	return creds.username, "", nil
}

func (creds HTTPSCreds) BasicAuthHeader() string {
	h := "Authorization: Basic "
	t := creds.username + ":" + creds.password
	h += base64.StdEncoding.EncodeToString([]byte(t))
	return h
}

func (creds HTTPSCreds) BearerAuthHeader() string {
	h := "Authorization: Bearer " + creds.bearerToken
	return h
}

// Get additional required environment variables for executing git client to
// access specific repository via HTTPS.
func (creds HTTPSCreds) Environ(_ logr.Logger) (io.Closer, []string, error) {
	var env []string

	httpCloser := authFilePaths(make([]string, 0))

	// GIT_SSL_NO_VERIFY is used to tell git not to validate the server's cert at
	// all.
	if creds.insecure {
		env = append(env, "GIT_SSL_NO_VERIFY=true")
	}

	// In case the repo is configured for using a TLS client cert, we need to make
	// sure git client will use it. The certificate's key must not be password
	// protected.
	// TODO: extract the common code here to a helper
	//nolint:dupl
	if creds.HasClientCert() {
		var certFile, keyFile *os.File

		// We need to actually create two temp files, one for storing cert data and
		// another for storing the key. If we fail to create second fail, the first
		// must be removed.
		certFile, err := os.CreateTemp(tempDir, "")
		if err != nil {
			return NopCloser{}, nil, err
		}
		// TODO: reevaluate this
		//nolint:errcheck
		defer certFile.Close()
		keyFile, err = os.CreateTemp(tempDir, "")
		if err != nil {
			removeErr := os.Remove(certFile.Name())
			if removeErr != nil {
				log.Errorf("Could not remove previously created tempfile %s: %v", certFile.Name(), removeErr)
			}
			return NopCloser{}, nil, err
		}
		// TODO: reevaluate this
		//nolint:errcheck
		defer keyFile.Close()

		// We should have both temp files by now
		httpCloser = authFilePaths([]string{certFile.Name(), keyFile.Name()})

		_, err = certFile.WriteString(creds.clientCertData)
		if err != nil {
			// TODO: reevaluate this
			//nolint:errcheck,gosec
			httpCloser.Close()
			return NopCloser{}, nil, err
		}
		// GIT_SSL_CERT is the full path to a client certificate to be used
		env = append(env, "GIT_SSL_CERT="+certFile.Name())

		_, err = keyFile.WriteString(creds.clientCertKey)
		if err != nil {
			// TODO: reevaluate this
			//nolint:errcheck,gosec
			httpCloser.Close()
			return NopCloser{}, nil, err
		}
		// GIT_SSL_KEY is the full path to a client certificate's key to be used
		env = append(env, "GIT_SSL_KEY="+keyFile.Name())
	}
	// If at least password is set, we will set KRATIX_GIT_AUTH_HEADER to
	// hold the HTTP authorization header, so auth mechanism negotiation is
	// skipped. This is insecure, but some environments may need it.
	if creds.password != "" && creds.forceBasicAuth {
		env = append(env, fmt.Sprintf("%s=%s", forceBasicAuthHeaderEnv, creds.BasicAuthHeader()))
	} else if creds.bearerToken != "" {
		// If bearer token is set, we will set KRATIX_GIT_BEARER_AUTH_HEADER to hold the HTTP authorization header
		env = append(env, fmt.Sprintf("%s=%s", bearerAuthHeaderEnv, creds.BearerAuthHeader()))
	}
	nonce := creds.store.Add(
		firstNonEmpty(creds.username, githubAccessTokenUsername), creds.password)
	env = append(env, creds.store.Environ(nonce)...)

	return newCloser(func() error {
		creds.store.Remove(nonce)
		return httpCloser.Close()
	}), env, nil
}

func (creds HTTPSCreds) HasClientCert() bool {
	return creds.clientCertData != "" && creds.clientCertKey != ""
}

func (creds HTTPSCreds) GetClientCertData() string {
	return creds.clientCertData
}

func (creds HTTPSCreds) GetClientCertKey() string {
	return creds.clientCertKey
}

func defaultTempDir() string {
	fileInfo, err := os.Stat("/dev/shm")
	if err == nil && fileInfo.IsDir() {
		return "/dev/shm"
	}
	return ""
}

func firstNonEmpty(args ...string) string {
	for _, value := range args {
		if len(value) > 0 {
			return value
		}
	}
	return ""
}

type closerFunc struct {
	closeFn func() error
}

func (c closerFunc) Close() error {
	return c.closeFn()
}

func newCloser(closeFn func() error) io.Closer {
	return closerFunc{closeFn: closeFn}
}

var _ Creds = SSHCreds{}

// SSH implementation
type SSHCreds struct {
	sshPrivateKey string
	knownHosts    string
	caPath        string
	insecure      bool
	proxy         string

	knownHostsFile string
}

func NewSSHCreds(sshPrivateKey string, knownHostFile string, caPath string, insecureIgnoreHostKey bool, proxy string) SSHCreds {
	return SSHCreds{
		sshPrivateKey: sshPrivateKey,
		//////////////////////////////////////////////////
		knownHosts: knownHostFile,
		caPath:     caPath,
		insecure:   insecureIgnoreHostKey,
		proxy:      proxy,
	}
}

// GetUserInfo returns empty strings for user info.
// TODO: Implement this method to return the username and email address for the credentials, if they're available.
func (c SSHCreds) GetUserInfo(_ context.Context, _ logr.Logger) (string, string, error) {
	// User info not implemented for SSH creds.
	return "", "", nil
}

type authFilePaths []string

// Remove a list of files that have been created as temp files while creating
// HTTPCreds object above.
func (f authFilePaths) Close() error {
	var retErr error
	for _, path := range f {
		err := os.Remove(path)
		if err != nil {
			log.Errorf("HTTPSCreds.Close(): Could not remove temp file %s: %v", path, err)
			retErr = err
		}
	}
	return retErr
}

type sshPrivateFiles []string

func (f sshPrivateFiles) Close() error {
	var retErr error
	//	for _, path := range f {
	//		err := os.Remove(path)
	//		if err != nil {
	//			log.Errorf("SSHCreds.Close(): Could not remove temp file %s: %v", path, err)
	//			retErr = err
	//		}
	//	}
	return retErr
}

func (c SSHCreds) Environ(_ logr.Logger) (io.Closer, []string, error) {
	// use the SHM temp dir from util, more secure
	file, err := os.CreateTemp(tempDir, "")
	if err != nil {
		return nil, nil, err
	}

	defer func() {
		if err = file.Close(); err != nil {
			log.WithFields(log.Fields{
				SecurityField:    SecurityMedium,
				SecurityCWEField: SecurityCWEMissingReleaseOfFileDescriptor,
			}).Errorf("error closing file %q: %v", file.Name(), err)
		}
	}()

	err = getSSHKnownHostsDataPath(&c)
	if err != nil {
		return nil, nil, err
	}

	sshCloser := sshPrivateFiles{file.Name(), c.knownHostsFile}

	_, err = file.WriteString(c.sshPrivateKey + "\n")
	if err != nil {
		// TODO: reevaluate this
		//nolint:errcheck,gosec
		sshCloser.Close()
		return nil, nil, err
	}

	args := []string{"ssh", "-i", file.Name()}
	var env []string
	if c.caPath != "" {
		env = append(env, "GIT_SSL_CAINFO="+c.caPath)
	}

	args = append(args, "-F", "/dev/null", "-o", "StrictHostKeyChecking=yes", "-o", "IdentityAgent=none", "-o", "UserKnownHostsFile="+c.knownHostsFile)

	// Handle SSH socks5 proxy settings
	proxyEnv := []string{}
	if c.proxy != "" {
		parsedProxyURL, err := url.Parse(c.proxy)
		if err != nil {
			// TODO: revisit
			//nolint:errcheck,gosec
			sshCloser.Close()
			return nil, nil, fmt.Errorf("failed to set environment variables related to socks5 proxy, could not parse proxy URL '%s': %w", c.proxy, err)
		}
		args = append(args, "-o", fmt.Sprintf("ProxyCommand='connect-proxy -S %s:%s -5 %%h %%p'",
			parsedProxyURL.Hostname(),
			parsedProxyURL.Port()))
		if parsedProxyURL.User != nil {
			proxyEnv = append(proxyEnv, "SOCKS5_USER="+parsedProxyURL.User.Username())
			if socks5Passwd, isPasswdSet := parsedProxyURL.User.Password(); isPasswdSet {
				proxyEnv = append(proxyEnv, "SOCKS5_PASSWD="+socks5Passwd)
			}
		}
	}
	env = append(env, []string{"GIT_SSH_COMMAND=" + strings.Join(args, " ")}...)
	env = append(env, proxyEnv...)

	return sshCloser, env, nil
}

func getSSHKnownHostsDataPath(c *SSHCreds) error {

	knownHostsFile, err := os.CreateTemp("", "knownHosts")
	if err != nil {
		return fmt.Errorf("error creating knownHosts file: %w", err)
	}

	_, err = knownHostsFile.Write([]byte(c.knownHosts))
	if err != nil {
		return fmt.Errorf("error writing knownHosts file: %w", err)
	}

	c.knownHostsFile = knownHostsFile.Name()

	return nil
}

const (
	// DefaultPathTLSConfig is the default path where TLS certificates for repositories are located
	DefaultPathTLSConfig = "/app/config/tls"
	// DefaultPathSSHConfig is the default path where SSH known hosts are stored
	DefaultPathSSHConfig = "/app/config/ssh"
	// DefaultSSHKnownHostsName is the Default name for the SSH known hosts file
	// TODO: do we really need this?
	DefaultSSHKnownHostsName = "ssh_known_hosts"
	// DefaultGnuPgHomePath is the Default path to GnuPG home directory
	DefaultGnuPgHomePath = "/app/config/gpg/keys"
	// DefaultAppConfigPath is the Default path to repo server TLS endpoint config
	DefaultAppConfigPath = "/app/config"
	// DefaultPluginSockFilePath is the Default path to cmp server plugin socket file
	DefaultPluginSockFilePath = "/home/argocd/cmp-server/plugins"
	// DefaultPluginConfigFilePath is the Default path to cmp server plugin configuration file
	DefaultPluginConfigFilePath = "/home/argocd/cmp-server/config"
	// PluginConfigFileName is the Plugin Config File is a ConfigManagementPlugin manifest located inside the plugin container
	PluginConfigFileName = "plugin.yaml"
)

// GitHubAppCreds to authenticate as GitHub application
type GitHubAppCreds struct {
	appID          int64
	appInstallId   int64
	privateKey     string
	baseURL        string
	clientCertData string
	clientCertKey  string
	insecure       bool
	proxy          string
	noProxy        string
	store          CredsStore
}

// NewGitHubAppCreds provide github app credentials
func NewGitHubAppCreds(appID int64, appInstallId int64, privateKey string, baseURL string, clientCertData string, clientCertKey string, insecure bool, proxy string, noProxy string, store CredsStore) GenericHTTPSCreds {
	return GitHubAppCreds{appID: appID, appInstallId: appInstallId, privateKey: privateKey, baseURL: baseURL, clientCertData: clientCertData, clientCertKey: clientCertKey, insecure: insecure, proxy: proxy, noProxy: noProxy, store: store}
}

func (g GitHubAppCreds) Environ(logger logr.Logger) (io.Closer, []string, error) {
	token, err := g.getAccessToken(logger)
	if err != nil {
		return NopCloser{}, nil, err
	}
	var env []string
	httpCloser := authFilePaths(make([]string, 0))

	// GIT_SSL_NO_VERIFY is used to tell git not to validate the server's cert at
	// all.
	if g.insecure {
		env = append(env, "GIT_SSL_NO_VERIFY=true")
	}

	// In case the repo is configured for using a TLS client cert, we need to make
	// sure git client will use it. The certificate's key must not be password
	// protected
	// TODO: extract the common code here to a helper
	//nolint:dupl
	if g.HasClientCert() {
		var certFile, keyFile *os.File

		// We need to actually create two temp files, one for storing cert data and
		// another for storing the key. If we fail to create second fail, the first
		// must be removed.
		certFile, err := os.CreateTemp(tempDir, "")
		if err != nil {
			return NopCloser{}, nil, err
		}
		// TODO: revisit
		//nolint:errcheck
		defer certFile.Close()
		keyFile, err = os.CreateTemp(tempDir, "")
		if err != nil {
			removeErr := os.Remove(certFile.Name())
			if removeErr != nil {
				log.Errorf("Could not remove previously created tempfile %s: %v", certFile.Name(), removeErr)
			}
			return NopCloser{}, nil, err
		}
		// TODO: revisit
		//nolint:errcheck
		defer keyFile.Close()

		// We should have both temp files by now
		httpCloser = authFilePaths([]string{certFile.Name(), keyFile.Name()})

		_, err = certFile.WriteString(g.clientCertData)
		if err != nil {
			// TODO: revisit
			//nolint:errcheck,gosec
			httpCloser.Close()
			return NopCloser{}, nil, err
		}
		// GIT_SSL_CERT is the full path to a client certificate to be used
		env = append(env, "GIT_SSL_CERT="+certFile.Name())

		_, err = keyFile.WriteString(g.clientCertKey)
		if err != nil {
			// TODO: revisit
			//nolint:errcheck,gosec
			httpCloser.Close()
			return NopCloser{}, nil, err
		}
		// GIT_SSL_KEY is the full path to a client certificate's key to be used
		env = append(env, "GIT_SSL_KEY="+keyFile.Name())
	}

	env = append(env, fmt.Sprintf("%s=%s", forceBasicAuthHeaderEnv, g.BasicAuthHeader(token)))

	nonce := g.store.Add(githubAccessTokenUsername, token)
	env = append(env, g.store.Environ(nonce)...)
	return newCloser(func() error {
		g.store.Remove(nonce)
		return httpCloser.Close()
	}), env, nil
}

func (g GitHubAppCreds) BasicAuthHeader(token string) string {
	h := "Authorization: Basic "
	t := githubAccessTokenUsername + ":" + token
	h += base64.StdEncoding.EncodeToString([]byte(t))
	return h
}

// GetUserInfo returns the username and email address for the credentials, if they're available.
func (g GitHubAppCreds) GetUserInfo(ctx context.Context, logger logr.Logger) (string, string, error) {
	// We use the apps transport to get the app slug.
	appTransport, err := g.getAppTransport(logger)
	if err != nil {
		return "", "", fmt.Errorf("failed to create GitHub app transport: %w", err)
	}
	appClient := github.NewClient(&http.Client{Transport: appTransport})
	app, _, err := appClient.Apps.Get(ctx, "")
	if err != nil {
		return "", "", fmt.Errorf("failed to get app info: %w", err)
	}

	// Then we use the installation transport to get the installation info.
	appInstallTransport, err := g.getInstallationTransport(logger)
	if err != nil {
		return "", "", fmt.Errorf("failed to get app installation: %w", err)
	}
	httpClient := http.Client{Transport: appInstallTransport}
	client := github.NewClient(&httpClient)

	appLogin := app.GetSlug() + "[bot]"
	user, _, err := client.Users.Get(ctx, appLogin)
	if err != nil {
		return "", "", fmt.Errorf("failed to get app user info: %w", err)
	}
	authorName := user.GetLogin()
	authorEmail := fmt.Sprintf("%d+%s@users.noreply.github.com", user.GetID(), user.GetLogin())
	return authorName, authorEmail, nil
}

// getAccessToken fetches GitHub token using the app id, install id, and private key.
// the token is then cached for re-use.
func (g GitHubAppCreds) getAccessToken(logger logr.Logger) (string, error) {
	// Timeout
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	itr, err := g.getInstallationTransport(logger)
	if err != nil {
		return "", fmt.Errorf("failed to create GitHub app installation transport: %w", err)
	}

	return itr.Token(ctx)
}

// getAppTransport creates a new GitHub transport for the app
func (g GitHubAppCreds) getAppTransport(logger logr.Logger) (*ghinstallation.AppsTransport, error) {
	// GitHub API url
	baseURL := "https://api.github.com"
	if g.baseURL != "" {
		baseURL = strings.TrimSuffix(g.baseURL, "/")
	}

	// Create a new GitHub transport
	c := GetRepoHTTPClient(logger, baseURL, g.insecure, g, g.proxy, g.noProxy)
	itr, err := ghinstallation.NewAppsTransport(c.Transport,
		g.appID,
		[]byte(g.privateKey),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialise GitHub installation transport: %w", err)
	}

	itr.BaseURL = baseURL

	return itr, nil
}

// getInstallationTransport creates a new GitHub transport for the app installation
func (g GitHubAppCreds) getInstallationTransport(logger logr.Logger) (*ghinstallation.Transport, error) {
	// Compute hash of creds for lookup in cache
	h := sha256.New()
	_, err := fmt.Fprintf(h, "%s %d %d %s", g.privateKey, g.appID, g.appInstallId, g.baseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get get SHA256 hash for GitHub app credentials: %w", err)
	}
	key := hex.EncodeToString(h.Sum(nil))

	// Check cache for GitHub transport which helps fetch an API token
	t, found := githubAppTokenCache.Get(key)
	if found {
		itr := t.(*ghinstallation.Transport)
		// This method caches the token and if it's expired retrieves a new one
		return itr, nil
	}

	// GitHub API url
	baseURL := "https://api.github.com"
	if g.baseURL != "" {
		baseURL = strings.TrimSuffix(g.baseURL, "/")
	}

	// Create a new GitHub transport
	c := GetRepoHTTPClient(logger, baseURL, g.insecure, g, g.proxy, g.noProxy)
	itr, err := ghinstallation.New(c.Transport,
		g.appID,
		g.appInstallId,
		[]byte(g.privateKey),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialise GitHub installation transport: %w", err)
	}

	itr.BaseURL = baseURL

	// Add transport to cache
	githubAppTokenCache.Set(key, itr, time.Minute*60)

	return itr, nil
}

func (g GitHubAppCreds) HasClientCert() bool {
	return g.clientCertData != "" && g.clientCertKey != ""
}

func (g GitHubAppCreds) GetClientCertData() string {
	return g.clientCertData
}

func (g GitHubAppCreds) GetClientCertKey() string {
	return g.clientCertKey
}

// GitHub App installation discovery cache and helper

// DiscoverGitHubAppInstallationID discovers the GitHub App installation ID for a given organisation.
// It queries the GitHub API to list all installations for the app and returns the installation ID
// for the matching organisation. Results are cached to avoid redundant API calls.
// An optional HTTP client can be provided for custom transport (e.g., for metrics tracking).
func DiscoverGitHubAppInstallationID(ctx context.Context, appId int64, privateKey, enterpriseBaseURL, org string, httpClient ...*http.Client) (int64, error) {
	domain, err := domainFromBaseURL(enterpriseBaseURL)
	if err != nil {
		return 0, fmt.Errorf("failed to get domain from base URL: %w", err)
	}
	org = strings.ToLower(org)
	// Check cache first
	cacheKey := fmt.Sprintf("%s:%s:%d", strings.ToLower(org), domain, appId)
	if id, found := githubInstallationIdCache.Get(cacheKey); found {
		return id.(int64), nil
	}

	// Use provided HTTP client or default
	var transport http.RoundTripper
	if len(httpClient) > 0 && httpClient[0] != nil && httpClient[0].Transport != nil {
		transport = httpClient[0].Transport
	} else {
		transport = http.DefaultTransport
	}

	// Create GitHub App transport
	rt, err := ghinstallation.NewAppsTransport(transport, appId, []byte(privateKey))
	if err != nil {
		return 0, fmt.Errorf("failed to create GitHub app transport: %w", err)
	}

	if enterpriseBaseURL != "" {
		rt.BaseURL = enterpriseBaseURL
	}

	// Create GitHub client
	var client *github.Client
	clientTransport := &http.Client{Transport: rt}
	if enterpriseBaseURL == "" {
		client = github.NewClient(clientTransport)
	} else {
		client, err = github.NewClient(clientTransport).WithEnterpriseURLs(enterpriseBaseURL, enterpriseBaseURL)
		if err != nil {
			return 0, fmt.Errorf("failed to create GitHub enterprise client: %w", err)
		}
	}

	// List all installations and cache them
	var allInstallations []*github.Installation
	opts := &github.ListOptions{PerPage: 100}

	// Lock for the entire loop to avoid multiple concurrent API calls on startup
	githubInstallationIdCacheMutex.Lock()
	defer githubInstallationIdCacheMutex.Unlock()

	// Check cache again inside the write lock in case another goroutine already fetched it
	if id, found := githubInstallationIdCache.Get(cacheKey); found {
		return id.(int64), nil
	}

	for {
		installations, resp, err := client.Apps.ListInstallations(ctx, opts)
		if err != nil {
			return 0, fmt.Errorf("failed to list installations: %w", err)
		}

		allInstallations = append(allInstallations, installations...)

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Cache all installation IDs
	for _, installation := range allInstallations {
		if installation.Account != nil && installation.Account.Login != nil && installation.ID != nil {
			githubInstallationIdCache.Set(cacheKey, *installation.ID, gocache.DefaultExpiration)
		}
	}

	// Return the installation ID for the requested org
	if id, found := githubInstallationIdCache.Get(cacheKey); found {
		return id.(int64), nil
	}
	return 0, fmt.Errorf("installation not found for org: %s", org)
}

// domainFromBaseURL extracts the host (domain) from the given GitHub base URL.
// Supports HTTP(S), SSH URLs, and git@host:org/repo forms.
// Returns an error if a domain cannot be extracted.
func domainFromBaseURL(baseURL string) (string, error) {
	if baseURL == "" {
		return "github.com", nil
	}

	// --- 1. SSH-style Git URL: git@github.com:org/repo.git ---
	if strings.Contains(baseURL, "@") && strings.Contains(baseURL, ":") && !strings.Contains(baseURL, "://") {
		parts := strings.SplitN(baseURL, "@", 2)
		right := parts[len(parts)-1]             // github.com:org/repo
		host := strings.SplitN(right, ":", 2)[0] // github.com
		if host != "" {
			return host, nil
		}
		return "", fmt.Errorf("failed to extract host from SSH-style URL: %q", baseURL)
	}

	// --- 2. Ensure scheme so url.Parse works ---
	if !strings.HasPrefix(baseURL, "http://") &&
		!strings.HasPrefix(baseURL, "https://") &&
		!strings.HasPrefix(baseURL, "ssh://") {
		baseURL = "https://" + baseURL
	}

	// --- 3. Standard URL parse ---
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL %q: %w", baseURL, err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("URL %q parsed but host is empty", baseURL)
	}

	host := parsed.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host, nil
}

// ExtractOrgFromRepoURL extracts the organisation/owner name from a GitHub repository URL.
// Supports formats:
//   - HTTPS: https://github.com/org/repo.git
//   - SSH: git@github.com:org/repo.git
//   - SSH with port: git@github.com:22/org/repo.git or ssh://git@github.com:22/org/repo.git
func ExtractOrgFromRepoURL(repoURL string) (string, error) {
	if repoURL == "" {
		return "", errors.New("repo URL is empty")
	}

	// Handle edge case: ssh://git@host:org/repo (malformed but used in practice)
	// This format mixes ssh:// prefix with colon notation instead of using a slash.
	// Convert it to git@host:org/repo which git-urls can parse correctly.
	// We distinguish this from the valid ssh://git@host:22/org/repo (with port number).
	if strings.HasPrefix(repoURL, "ssh://git@") {
		remainder := strings.TrimPrefix(repoURL, "ssh://")
		if colonIdx := strings.Index(remainder, ":"); colonIdx != -1 {
			afterColon := remainder[colonIdx+1:]
			slashIdx := strings.Index(afterColon, "/")

			// Check if what follows the colon is a port number
			isPort := false
			if slashIdx > 0 {
				if _, err := strconv.Atoi(afterColon[:slashIdx]); err == nil {
					isPort = true
				}
			}

			// If not a port, it's the malformed format - strip ssh:// prefix
			if !isPort && slashIdx != 0 {
				repoURL = remainder
			}
		}
	}

	// Use git-urls library to parse all Git URL formats
	parsed, err := giturls.Parse(repoURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse repository URL %q: %w", repoURL, err)
	}

	// Clean the path: remove leading/trailing slashes and .git suffix
	path := strings.Trim(parsed.Path, "/")
	path = strings.TrimSuffix(path, ".git")

	if path == "" {
		return "", fmt.Errorf("repository URL %q does not contain a path", repoURL)
	}

	// Extract the first path component (organisation/owner)
	// Path format is typically "org/repo" or "org/repo/subpath"
	if idx := strings.Index(path, "/"); idx > 0 {
		org := path[:idx]
		// Normalise to lowercase for case-insensitive comparison
		return strings.ToLower(org), nil
	}

	// If there's no slash, the entire path might be just the org (unusual but handle it)
	// This would fail validation later, but let's return it
	return "", fmt.Errorf("could not extract organisation from repository URL %q: path %q does not contain org/repo format", repoURL, path)
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

func SetAuth(stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string, creds map[string][]byte) (*Auth, error) {

	var (
		credsX     Creds
		authMethod transport.AuthMethod
	)

	switch stateStoreSpec.AuthMethod {

	case v1alpha1.SSHAuthMethod:

		sshCreds, err := newSSHAuthCreds(stateStoreSpec, creds)
		if err != nil {
			return nil, err
		}

		credsX = NewSSHCreds(string(sshCreds.SSHPrivateKey), string(sshCreds.KnownHosts), "", false, "")

		sshKey, err := tx_ssh.NewPublicKeys(sshCreds.SSHUser, sshCreds.SSHPrivateKey, "")
		if err != nil {
			return nil, fmt.Errorf("error parsing sshKey: %w", err)
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

		credsX = NewHTTPSCreds(
			basicCreds.Username,
			basicCreds.Password,
			"",
			"",
			"",
			true,
			NoopCredsStore{},
			true,
		)

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

		// Git DOES automatically extract the credentials from the URL and
		// convert them to Authorization: Basic header. But in this scenario
		// we need to force basic auth, as Git credentials helpers are disabled
		authMethod = &githttp.BasicAuth{
			Username: githubAccessTokenUsername,
			Password: token,
		}

		appID, err := strconv.Atoi(appCreds.AppID)
		if err != nil {
			return nil, fmt.Errorf("could not convert installation ID to int: %w", err)
		}

		installationID, err := strconv.Atoi(appCreds.InstallationID)
		if err != nil {
			return nil, fmt.Errorf("could not convert installation ID to int: %w", err)
		}

		credsX = NewGitHubAppCreds(
			int64(appID),
			int64(installationID),
			appCreds.PrivateKey,
			"https://api.github.com",
			"",
			"",
			false,
			"",
			"",
			NoopCredsStore{})
	}

	return &Auth{
		AuthMethod: authMethod,
		Creds:      credsX,
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
	// When using a GitHub PAT token with git cli, username is ignored,
	// but it cannot be empty as git cli uses basic auth: username:password.
	// Only default to the GitHub username when the repo looks like GitHub.
	if (!ok || string(username) == "") && strings.Contains(strings.ToLower(stateStoreSpec.URL), "github") {
		username = []byte(githubAccessTokenUsername)
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
		// Currently only the standard API URL is supported
		ApiUrl: "https://api.github.com",
	}, nil
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

// TODO: replace this with interface and mock
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
		return nil, fmt.Errorf("failed to parse RSA private key: %w", err)
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

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			// #nosec G402
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
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

// Security severity logging
const (
	SecurityField = "security"
	// SecurityCWEField is the logs field for the CWE associated with a log line. CWE stands for Common Weakness Enumeration. See https://cwe.mitre.org/
	SecurityCWEField                          = "CWE"
	SecurityCWEMissingReleaseOfFileDescriptor = 775
	SecurityMedium                            = 2 // Could indicate malicious events, but has a high likelihood of being user/system error (i.e. access denied)
)
