package git

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	log "github.com/sirupsen/logrus"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
)

type GenericHTTPSCreds interface {
	HasClientCert() bool
	GetClientCertData() string
	GetClientCertKey() string
	Creds
}

type basicAuthCreds struct {
	Username string
	Password string
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
	if creds.HasClientCert() {
		err := processClientCert(creds, &env, &httpCloser)
		if err != nil {
			return NopCloser{}, nil, fmt.Errorf("could not process client certificate: %w", err)
		}
	}

	// If at least password is set, we will set KRATIX_BASIC_AUTH_HEADER to
	// hold the HTTP authorization header, so auth mechanism negotiation is
	// skipped. This is insecure, but some environments may need it.
	if creds.password != "" && creds.forceBasicAuth {
		env = append(env, fmt.Sprintf("%s=%s", forceBasicAuthHeaderEnv, creds.BasicAuthHeader()))
	} else if creds.bearerToken != "" {
		// If bearer token is set, we will set KRATIX_BEARER_AUTH_HEADER to	hold the HTTP authorization header
		env = append(env, fmt.Sprintf("%s=%s", bearerAuthHeaderEnv, creds.BearerAuthHeader()))
	}
	nonce := creds.store.Add(
		FirstNonEmpty(creds.username, githubAccessTokenUsername), creds.password)
	env = append(env, creds.store.Environ(nonce)...)

	return NewCloser(func() error {
		creds.store.Remove(nonce)
		return httpCloser.Close()
	}), env, nil
}

func FirstNonEmpty(args ...string) string {
	for _, value := range args {
		if len(value) > 0 {
			return value
		}
	}
	return ""
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

// Returns a HTTP client object suitable for go-git to use using the following
// pattern:
//   - If insecure is true, always returns a client with certificate verification
//     turned off.
//   - If one or more custom certificates are stored for the repository, returns
//     a client with those certificates in the list of root CAs used to verify
//     the server's certificate.
//   - Otherwise (and on non-fatal errors), a default HTTP client is returned.
func getRepoHTTPClient(logger logr.Logger, repoURL string, insecure bool, creds Creds, proxyURL string, noProxy string) *http.Client {
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
