package git

import (
	"context"
	"crypto/fips140"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-logr/logr"
	"golang.org/x/crypto/ssh"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
)

var _ Creds = SSHCreds{}

// SSH implementation
type SSHCreds struct {
	caPath         string
	insecure       bool
	knownHosts     string
	knownHostsFile string
	proxy          string
	sshPrivateKey  string
}

type sshAuthCreds struct {
	SSHPrivateKey []byte
	KnownHosts    []byte
	SSHUser       string
}

func NewSSHCreds(sshPrivateKey string, knownHostFile string, caPath string, insecureIgnoreHostKey bool, proxy string) SSHCreds {
	return SSHCreds{
		sshPrivateKey: sshPrivateKey,
		knownHosts:    knownHostFile,
		caPath:        caPath,
		insecure:      insecureIgnoreHostKey,
		proxy:         proxy,
	}
}

// GetUserInfo returns empty strings for user info.
// TODO: Implement this method to return the username and email address for the credentials, if they're available.
func (c SSHCreds) GetUserInfo(_ context.Context, _ logr.Logger) (string, string, error) {
	// User info not implemented for SSH creds.
	return "", "", nil
}

type sshPrivateFiles struct {
	paths []string
}

func (f sshPrivateFiles) Close() error {
	var retErr error
	for _, path := range f.paths {
		err := os.Remove(path)
		if err != nil {
			retErr = fmt.Errorf("could not remove temp file %s: %w", path, err)
		}
	}
	return retErr
}

func (c SSHCreds) Environ(logger logr.Logger) (io.Closer, []string, error) {
	// use the SHM temp dir from util, more secure
	file, err := os.CreateTemp(TempDir, "")
	if err != nil {
		return nil, nil, err
	}

	defer func() {
		if err = file.Close(); err != nil {
			logging.Error(
				logger,
				err,
				"error closing file",
				"file",
				file.Name(),
				SecurityField,
				SecurityMedium,
				SecurityCWEField,
				SecurityCWEMissingReleaseOfFileDescriptor,
			)
		}
	}()

	err = getSSHKnownHostsDataPath(&c)
	if err != nil {
		return nil, nil, err
	}

	sshCloser := sshPrivateFiles{
		paths: []string{file.Name(), c.knownHostsFile},
	}

	_, err = file.WriteString(c.sshPrivateKey + "\n")
	if err != nil {
		closeErr := sshCloser.Close()
		if closeErr != nil {
			logging.Error(logger, closeErr, "could not close SSH private key file")
		}
		return nil, nil, fmt.Errorf("could not write SSH private key: %w", err)
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
			if closeErr := sshCloser.Close(); closeErr != nil {
				logging.Error(logger, closeErr, "could not close SSH closer")
			}
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

	return NewCloser(func() error {
		if err := sshCloser.Close(); err != nil {
			logging.Error(logger, err, "could not remove temp file")
			return err
		}
		return nil
	}), env, nil
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

var (
	TempDir string
)

func init() {
	fileInfo, err := os.Stat("/dev/shm")
	if err == nil && fileInfo.IsDir() {
		TempDir = "/dev/shm"
	}
}
