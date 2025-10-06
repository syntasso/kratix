package writers_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("NewGitWriter", func() {
	var (
		logger         logr.Logger
		dest           v1alpha1.Destination
		stateStoreSpec v1alpha1.GitStateStoreSpec
	)

	BeforeEach(func() {
		logger = ctrl.Log.WithName("setup")
		stateStoreSpec = v1alpha1.GitStateStoreSpec{
			StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
				Path: "state-store-path",
			},
			AuthMethod: "basicAuth",
			URL:        "https://github.com/syntasso/kratix",
			Branch:     "test",
			GitAuthor: v1alpha1.GitAuthor{
				Email: "test@example.com",
				Name:  "a-user",
			},
		}

		dest = v1alpha1.Destination{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "test",
			},
			Spec: v1alpha1.DestinationSpec{
				Path: "dst-path/",
			},
		}

	})

	It("returns a valid GitWriter", func() {
		creds := map[string][]byte{
			"username": []byte("user1"),
			"password": []byte("pw1"),
		}
		stateStoreSpec.GitAuthor = v1alpha1.GitAuthor{
			Email: "test@example.com",
			Name:  "a-user",
		}
		writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, creds)
		Expect(err).NotTo(HaveOccurred())
		Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
		gitWriter, ok := writer.(*writers.GitWriter)
		Expect(ok).To(BeTrue())
		Expect(gitWriter.GitServer.URL).To(Equal("https://github.com/syntasso/kratix"))
		Expect(gitWriter.GitServer.Auth).To(Equal(&http.BasicAuth{
			Username: "user1",
			Password: "pw1",
		}))
		Expect(gitWriter.GitServer.Branch).To(Equal("test"))
		Expect(gitWriter.Author.Email).To(Equal("test@example.com"))
		Expect(gitWriter.Author.Name).To(Equal("a-user"))
		Expect(gitWriter.Path).To(Equal("state-store-path/dst-path"))
	})

	It("removes leading slash from the Path", func() {
		creds := map[string][]byte{
			"username": []byte("user1"),
			"password": []byte("pw1"),
		}
		By("removing the leading slash when StateStore.Path is defined", func() {
			stateStoreSpec.Path = "/test"
			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, creds)
			Expect(err).NotTo(HaveOccurred())
			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
			Expect(gitWriter.Path).To(HavePrefix("test"))

			stateStoreSpec.Path = ""
		})

		By("removing the leading slash when Destination.Path is defined", func() {
			dest.Spec.Path = "/dst-test"
			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, creds)
			Expect(err).NotTo(HaveOccurred())
			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
			Expect(gitWriter.Path).To(HavePrefix("dst-test"))
		})
	})

	Context("authenticate with SSH", func() {
		It("returns a valid GitWriter", func() {
			stateStoreSpec.AuthMethod = "ssh"
			key, err := rsa.GenerateKey(rand.Reader, 1024)
			Expect(err).NotTo(HaveOccurred())

			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, generateSSHCreds(key))
			Expect(err).NotTo(HaveOccurred())
			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
			Expect(gitWriter.GitServer.URL).To(Equal("https://github.com/syntasso/kratix"))
			Expect(gitWriter.GitServer.Auth.(*ssh.PublicKeys).User).To(Equal("git"))
			publicKey, ok := gitWriter.GitServer.Auth.(*ssh.PublicKeys)
			Expect(ok).To(BeTrue())
			Expect(publicKey).NotTo(BeNil())
			Expect(gitWriter.GitServer.Branch).To(Equal("test"))
			Expect(gitWriter.Author.Email).To(Equal("test@example.com"))
			Expect(gitWriter.Author.Name).To(Equal("a-user"))
		})

		It("set ssh user according to the state store url", func() {
			stateStoreSpec.URL = "test-user@test.ghe.com:test-org/test-state-store.git"
			stateStoreSpec.AuthMethod = "ssh"
			key, err := rsa.GenerateKey(rand.Reader, 1024)
			Expect(err).NotTo(HaveOccurred())

			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, generateSSHCreds(key))
			Expect(err).NotTo(HaveOccurred())
			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
			Expect(gitWriter.GitServer.URL).To(Equal("test-user@test.ghe.com:test-org/test-state-store.git"))
			Expect(gitWriter.GitServer.Auth.(*ssh.PublicKeys).User).To(Equal("test-user"))
			publicKey, ok := gitWriter.GitServer.Auth.(*ssh.PublicKeys)
			Expect(ok).To(BeTrue())
			Expect(publicKey).NotTo(BeNil())
		})
	})

	Context("authenticate with GitHub App", func() {
		var (
			jwtCalled, tokenCalled, autoRefreshCalled bool
		)
		BeforeEach(func() {
			jwtCalled = false
			tokenCalled = false
			autoRefreshCalled = false
			writers.GenerateGitHubAppJWT = func(appID, pk string) (string, error) {
				jwtCalled = true
				return "jwt", nil
			}
			writers.GetGitHubInstallationTokenWithExpiry = func(installationID, jwt string) (string, time.Time, error) {
				tokenCalled = true
				return "token", time.Now().Add(time.Hour), nil
			}
			writers.StartGitHubTokenAutoRefresh = func(log logr.Logger, ba *http.BasicAuth, appID, installationID, pk string, exp time.Time) {
				autoRefreshCalled = true
			}
		})

		It("returns a valid GitWriter", func() {
			stateStoreSpec.AuthMethod = "githubApp"
			creds := map[string][]byte{
				"appID":          []byte("123"),
				"installationID": []byte("456"),
				"privateKey":     []byte("dummy"),
			}
			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, creds)
			Expect(err).NotTo(HaveOccurred())
			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter := writer.(*writers.GitWriter)
			Expect(gitWriter.GitServer.Auth.(*http.BasicAuth).Username).To(Equal("x-access-token"))
			Expect(gitWriter.GitServer.Auth.(*http.BasicAuth).Password).To(Equal("token"))
			Expect(jwtCalled).To(BeTrue())
			Expect(tokenCalled).To(BeTrue())
			Expect(autoRefreshCalled).To(BeTrue())
		})
	})

	Describe("ValidatePermissions", func() {
		var (
			gitWriter *writers.GitWriter
			creds     map[string][]byte
		)

		BeforeEach(func() {
			creds = map[string][]byte{
				"username": []byte("user1"),
				"password": []byte("pw1"),
			}

			var err error
			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, creds)
			Expect(err).NotTo(HaveOccurred())
			var ok bool
			gitWriter, ok = writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
		})

		It("returns an error when authentication fails", func() {
			// Set invalid credentials
			gitWriter.GitServer.Auth = &http.BasicAuth{
				Username: "invalid",
				Password: "invalid",
			}

			err := gitWriter.ValidatePermissions()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Or(
				ContainSubstring("failed to set up local directory with repo"),
				ContainSubstring("authentication"),
				ContainSubstring("permission"),
				ContainSubstring("authorization"),
			))
		})
	})
})

func generateSSHCreds(key *rsa.PrivateKey) map[string][]byte {
	privateKeyPEM := pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	var b bytes.Buffer
	if err := pem.Encode(&b, &privateKeyPEM); err != nil {
		log.Fatalf("Failed to write private key to buffer: %v", err)
	}

	return map[string][]byte{
		"sshPrivateKey": b.Bytes(),
		"knownHosts":    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"),
	}
}
