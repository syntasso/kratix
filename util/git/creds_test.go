package git_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/util/git"
)

var _ = Describe("SetAuth", func() {
	var spec v1alpha1.GitStateStoreSpec

	BeforeEach(func() {
		spec = v1alpha1.GitStateStoreSpec{
			StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
				Path: "state-store-path",
				SecretRef: &corev1.SecretReference{
					Namespace: "default",
					Name:      "dummy-secret",
				},
			},
			URL:    "https://github.com/syntasso/kratix",
			Branch: "main",
		}
	})

	When("authenticating with basicAuth", func() {
		BeforeEach(func() {
			spec.AuthMethod = v1alpha1.BasicAuthMethod
		})

		It("returns an Auth with HTTPS creds", func() {
			auth, err := git.SetAuth(spec, map[string][]byte{
				"username": []byte("user1"),
				"password": []byte("pw1"),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(auth).NotTo(BeNil())
			Expect(auth.Creds).NotTo(BeNil())
		})

		It("returns an error when password is missing", func() {
			_, err := git.SetAuth(spec, map[string][]byte{
				"username": []byte("user1"),
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("password not found"))
		})
	})

	When("authenticating with SSH", func() {
		BeforeEach(func() {
			spec.AuthMethod = v1alpha1.SSHAuthMethod
			spec.URL = "git@github.com:syntasso/kratix.git"
		})

		It("returns an Auth with SSH creds for a valid key", func() {
			key, err := rsa.GenerateKey(rand.Reader, 1024)
			Expect(err).NotTo(HaveOccurred())

			auth, err := git.SetAuth(spec, sshCredsFromKey(key))
			Expect(err).NotTo(HaveOccurred())
			Expect(auth).NotTo(BeNil())
			Expect(auth.Creds).NotTo(BeNil())
		})

		It("returns an error for an invalid SSH private key", func() {
			_, err := git.SetAuth(spec, map[string][]byte{
				"sshPrivateKey": []byte("not-a-key"),
				"knownHosts":    []byte("github.com ssh-ed25519 AAAA"),
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("ssh private key"))
		})
	})

	When("authenticating with GitHub App", func() {
		var (
			origJWT     func(string, string) (string, error)
			origToken   func(string, string, string) (string, error)
			jwtCalled   bool
			tokenCalled bool
		)

		BeforeEach(func() {
			spec.AuthMethod = v1alpha1.GitHubAppAuthMethod
			origJWT = git.GenerateGitHubAppJWT
			origToken = git.GetGitHubInstallationToken
			git.GenerateGitHubAppJWT = func(appID, pk string) (string, error) {
				jwtCalled = true
				return "jwt-token", nil
			}
			git.GetGitHubInstallationToken = func(apiURL, installationID, jwt string) (string, error) {
				tokenCalled = true
				return "install-token", nil
			}
		})

		AfterEach(func() {
			git.GenerateGitHubAppJWT = origJWT
			git.GetGitHubInstallationToken = origToken
		})

		It("returns an Auth and exchanges JWT for an installation token", func() {
			auth, err := git.SetAuth(spec, map[string][]byte{
				"appID":          []byte("123"),
				"installationID": []byte("456"),
				"privateKey":     []byte("dummy"),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(auth).NotTo(BeNil())
			Expect(jwtCalled).To(BeTrue())
			Expect(tokenCalled).To(BeTrue())
		})

		It("returns an error when JWT generation fails", func() {
			git.GenerateGitHubAppJWT = func(appID, pk string) (string, error) {
				return "", errBoom
			}
			_, err := git.SetAuth(spec, map[string][]byte{
				"appID":          []byte("123"),
				"installationID": []byte("456"),
				"privateKey":     []byte("dummy"),
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to generate GitHub App JWT"))
		})

		It("returns an error when installation token retrieval fails", func() {
			git.GetGitHubInstallationToken = func(apiURL, installationID, jwt string) (string, error) {
				return "", errBoom
			}
			_, err := git.SetAuth(spec, map[string][]byte{
				"appID":          []byte("123"),
				"installationID": []byte("456"),
				"privateKey":     []byte("dummy"),
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to get GitHub installation token"))
		})
	})
})

func sshCredsFromKey(key *rsa.PrivateKey) map[string][]byte {
	privateKeyPEM := pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	var b bytes.Buffer
	Expect(pem.Encode(&b, &privateKeyPEM)).To(Succeed())
	return map[string][]byte{
		"sshPrivateKey": b.Bytes(),
		"knownHosts":    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"),
	}
}
