package git

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/go-logr/logr"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v69/github"
	gocache "github.com/patrickmn/go-cache"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
)

var (
	// In memory cache for storing github APP api token credentials
	githubAppTokenCache *gocache.Cache

	// installationIdCache caches installation IDs for organisations to avoid redundant API calls.
	githubInstallationIdCache      *gocache.Cache
	githubInstallationIdCacheMutex sync.RWMutex // For bulk API call coordination
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

// TODO: merge with above
type githubAppCreds struct {
	AppID          string
	InstallationID string
	PrivateKey     string
	ApiUrl         string
}

// NewGitHubAppCreds provide github app credentials
func NewGitHubAppCreds(appID int64, appInstallId int64, privateKey string, baseURL string, clientCertData string, clientCertKey string, insecure bool, proxy string, noProxy string, store CredsStore) GenericHTTPSCreds {
	return GitHubAppCreds{appID: appID, appInstallId: appInstallId, privateKey: privateKey, baseURL: baseURL, clientCertData: clientCertData, clientCertKey: clientCertKey, insecure: insecure, proxy: proxy, noProxy: noProxy, store: store}
}

// Now make the function generic
func processClientCert[T ClientCertProvider](creds T, env *[]string, httpCloser *authFilePaths, logger logr.Logger) error {
	var certFile, keyFile *os.File

	certFile, err := os.CreateTemp(TempDir, "")
	if err != nil {
		return fmt.Errorf("could not create temporary dir: %w", err)
	}
	defer func() {
		if err := certFile.Close(); err != nil {
			logging.Error(logger, err, "could not close cert file", "path", certFile.Name())
		}
	}()

	keyFile, err = os.CreateTemp(TempDir, "")
	if err != nil {
		removeErr := os.Remove(certFile.Name())
		if removeErr != nil {
			logging.Error(logger, removeErr, "could not remove previously created temp file", "path", certFile.Name())
		}
		return err
	}
	defer func() {
		if err := keyFile.Close(); err != nil {
			logging.Error(logger, err, "could not close key file", "path", keyFile.Name())
		}
	}()

	httpCloser.paths = []string{certFile.Name(), keyFile.Name()}

	_, err = certFile.WriteString(creds.GetClientCertData())
	if err != nil {
		if closeErr := httpCloser.Close(); closeErr != nil {
			logging.Error(logger, closeErr, "could not close http closer")
		}
		return err
	}
	*env = append(*env, "GIT_SSL_CERT="+certFile.Name())

	_, err = keyFile.WriteString(creds.GetClientCertKey())
	if err != nil {
		if closeErr := httpCloser.Close(); closeErr != nil {
			logging.Error(logger, closeErr, "could not close http closer")
		}
		return err
	}
	*env = append(*env, "GIT_SSL_KEY="+keyFile.Name())

	return nil
}

func (g GitHubAppCreds) Environ(logger logr.Logger) (io.Closer, []string, error) {
	token, err := g.getAccessToken(logger)
	if err != nil {
		return NopCloser{}, nil, err
	}
	var env []string
	httpCloser := authFilePaths{paths: make([]string, 0)}

	// GIT_SSL_NO_VERIFY is used to tell git not to validate the server's cert at all.
	if g.insecure {
		env = append(env, "GIT_SSL_NO_VERIFY=true")
	}

	// In case the repo is configured for using a TLS client cert, we need to make
	// sure git client will use it. The certificate's key must not be password
	// protected.
	if g.HasClientCert() {
		err := processClientCert(g, &env, &httpCloser, logger)
		if err != nil {
			return NopCloser{}, nil, fmt.Errorf("could not process client certificate: %w", err)
		}
	}

	env = append(env, fmt.Sprintf("%s=%s", forceBasicAuthHeaderEnv, g.BasicAuthHeader(token)))

	nonce := g.store.Add(githubAccessTokenUsername, token)
	env = append(env, g.store.Environ(nonce)...)
	return NewCloser(func() error {
		g.store.Remove(nonce)
		if err := httpCloser.Close(); err != nil {
			logging.Error(logger, err, "could not remove temp file")
			return err
		}
		return nil
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
	c := getRepoHTTPClient(logger, baseURL, g.insecure, g, g.proxy, g.noProxy)
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
		itr, _ := t.(*ghinstallation.Transport)
		// This method caches the token and if it's expired retrieves a new one
		return itr, nil
	}

	// GitHub API url
	baseURL := "https://api.github.com"
	if g.baseURL != "" {
		baseURL = strings.TrimSuffix(g.baseURL, "/")
	}

	// Create a new GitHub transport
	c := getRepoHTTPClient(logger, baseURL, g.insecure, g, g.proxy, g.noProxy)
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
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
				MinVersion:         tls.VersionTLS12,
			},
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

type Closer interface {
	Close() error
}

type inlineCloser struct {
	close func() error
}

func (c *inlineCloser) Close() error {
	return c.close()
}

func NewCloser(closeFn func() error) Closer {
	return &inlineCloser{close: closeFn}
}
