package git

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	tx_ssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-logr/logr"

	"github.com/syntasso/kratix/api/v1alpha1"
)

const (
	// The x-access-token username is actually just a convention - it's not validated by GitHub.
	// GitHub ignores the username field entirely when a valid token is in the password field.
	// You could use any string (or even empty string) as the username, but x-access-token
	// is recommended for clarity.
	githubAccessTokenUsername = "x-access-token"

	forceBasicAuthHeaderEnv = "KRATIX_GIT_AUTH_HEADER"
	// #nosec G101
	bearerAuthHeaderEnv = "KRATIX_GIT_BEARER_AUTH_HEADER"

	SecurityField = "security"
	// SecurityCWEField is the logs field for the CWE associated with a log line. CWE stands for Common Weakness Enumeration. See https://cwe.mitre.org/
	SecurityCWEField                          = "CWE"
	SecurityCWEMissingReleaseOfFileDescriptor = 775
	SecurityMedium                            = 2 // Could indicate malicious events, but has a high likelihood of being user/system error (i.e. access denied)
)

type Auth struct {
	transport.AuthMethod
	Creds
}

// Define an interface that describes what types can use this function.
type ClientCertProvider interface {
	GetClientCertData() string
	GetClientCertKey() string
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

// Nop implementation.
type NopCloser struct{}

func (c NopCloser) Close() error {
	return nil
}

var _ Creds = NopCreds{}

type NopCreds struct{}

func (c NopCreds) Environ(_ logr.Logger) (io.Closer, []string, error) {
	return NopCloser{}, nil, nil
}

// GetUserInfo returns empty strings for user info.
func (c NopCreds) GetUserInfo(_ context.Context, _ logr.Logger) (name string, email string, err error) {
	return "", "", nil
}

var _ io.Closer = NopCloser{}

// GitHub App installation discovery cache and helper.

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
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		return "", fmt.Errorf("could not split host and port: %w", err)
	}
	host = h

	return host, nil
}

func SetAuth(stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string, creds map[string][]byte) (*Auth, error) {

	var (
		authCreds  Creds
		authMethod transport.AuthMethod
	)

	switch stateStoreSpec.AuthMethod {
	case v1alpha1.SSHAuthMethod:

		sshCreds, err := newSSHAuthCreds(stateStoreSpec, creds)
		if err != nil {
			return nil, err
		}

		authCreds = NewSSHCreds(string(sshCreds.SSHPrivateKey), string(sshCreds.KnownHosts), "", false, "")

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

		authCreds = NewHTTPSCreds(
			basicCreds.Username,
			basicCreds.Password,
			"",
			"",
			"",
			false,
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

		authCreds = NewGitHubAppCreds(
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
		Creds:      authCreds,
	}, nil
}
