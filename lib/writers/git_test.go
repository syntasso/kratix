package writers_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log"
	"net/http"
	"net/http/httptest"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/util/git"
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
				SecretRef: &corev1.SecretReference{
					Namespace: "default",
					Name:      "dummy-secret",
				},
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
			stateStoreSpec.URL = "test-user@test.ghe.com:test-org/test-state-store.git"
			key, err := rsa.GenerateKey(rand.Reader, 1024)
			Expect(err).NotTo(HaveOccurred())

			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, generateSSHCreds(key))
			Expect(err).NotTo(HaveOccurred())
			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
			Expect(gitWriter.GitServer.URL).To(Equal("test-user@test.ghe.com:test-org/test-state-store.git"))
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
		})
	})

	Context("authenticate with GitHub App", func() {
		var (
			jwtCalled, tokenCalled         bool
			origGenerateGitHubAppJWT       func(string, string) (string, error)
			origGetGitHubInstallationToken func(string, string, string) (string, error)
		)
		BeforeEach(func() {
			jwtCalled = false
			tokenCalled = false
			origGenerateGitHubAppJWT = git.GenerateGitHubAppJWT
			origGetGitHubInstallationToken = git.GetGitHubInstallationToken
			git.GenerateGitHubAppJWT = func(appID, pk string) (string, error) {
				jwtCalled = true
				return "jwt", nil
			}
			git.GetGitHubInstallationToken = func(apiURL, installationID, jwt string) (string, error) {
				tokenCalled = true
				return "token", nil
			}
		})
		AfterEach(func() {
			git.GenerateGitHubAppJWT = origGenerateGitHubAppJWT
			git.GetGitHubInstallationToken = origGetGitHubInstallationToken
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
			Expect(jwtCalled).To(BeTrue())
			Expect(tokenCalled).To(BeTrue())
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
			invalidCreds := map[string][]byte{
				"username": []byte("invalid"),
				"password": []byte("invalid"),
			}
			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, invalidCreds)
			Expect(err).NotTo(HaveOccurred())
			var ok bool
			gitWriter, ok = writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())

			err = gitWriter.ValidatePermissions()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Or(
				ContainSubstring("failed to set up local directory with repo"),
				ContainSubstring("authentication"),
				ContainSubstring("permission"),
				ContainSubstring("authorization"),
			))
		})
	})

	Describe("generateGitHubAppJWT", func() {
		It("returns a signed JWT with valid RSA key and appID", func() {
			pemStr := generatePEMFromPKCS1RSAKey()
			jwt, err := git.GenerateGitHubAppJWT("12345", pemStr)
			Expect(err).NotTo(HaveOccurred())
			Expect(jwt).NotTo(BeEmpty())
		})

		It("parses RSA private key in PKCS#8 format", func() {
			pemStr := generatePEMFromPKCS8RSAKey()
			jwt, err := git.GenerateGitHubAppJWT("12345", pemStr)
			Expect(err).NotTo(HaveOccurred())
			Expect(jwt).NotTo(BeEmpty())
		})

		It("returns error for invalid PEM", func() {
			jwt, err := git.GenerateGitHubAppJWT("12345", "not-a-pem")
			Expect(err).To(HaveOccurred())
			Expect(jwt).To(BeEmpty())
		})

		It("returns error for non-RSA PEM", func() {
			block := pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("bad")}
			pemBytes := pem.EncodeToMemory(&block)
			jwt, err := git.GenerateGitHubAppJWT("12345", string(pemBytes))
			Expect(err).To(HaveOccurred())
			Expect(jwt).To(BeEmpty())
		})

		It("returns error for non-RSA PKCS#8 key", func() {
			pemStr := generatePEMFromPKCS8ECKey()
			jwt, err := git.GenerateGitHubAppJWT("12345", pemStr)
			Expect(err).To(HaveOccurred())
			Expect(jwt).To(BeEmpty())
		})
	})

	Describe("getGitHubInstallationToken", func() {
		var server *httptest.Server

		AfterEach(func() {
			if server != nil {
				server.Close()
			}
		})

		It("returns token on 201 response", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Method).To(Equal(http.MethodPost))
				Expect(r.Header.Get("Authorization")).To(Equal("Bearer jwt"))
				Expect(r.Header.Get("Accept")).To(Equal("application/vnd.github+json"))

				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]string{"token": "abc123"})
			}))

			tok, err := git.GetGitHubInstallationToken(server.URL, "123", "jwt")
			Expect(err).NotTo(HaveOccurred())
			Expect(tok).To(Equal("abc123"))
		})

		It("returns error on non-201 response", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "bad creds"})
			}))

			tok, err := git.GetGitHubInstallationToken(server.URL, "123", "jwt")
			Expect(err).To(HaveOccurred())
			Expect(tok).To(BeEmpty())
			Expect(err.Error()).To(ContainSubstring("bad creds"))
		})

		It("returns error if token is empty", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]string{"token": ""})
			}))

			tok, err := git.GetGitHubInstallationToken(server.URL, "123", "jwt")
			Expect(err).To(HaveOccurred())
			Expect(tok).To(BeEmpty())
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

func generatePEMFromPKCS1RSAKey() string {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	privDER := x509.MarshalPKCS1PrivateKey(key)
	block := pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER}
	return string(pem.EncodeToMemory(&block))
}

func generatePEMFromPKCS8RSAKey() string {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	privDER, _ := x509.MarshalPKCS8PrivateKey(key)
	block := pem.Block{Type: "PRIVATE KEY", Bytes: privDER}
	return string(pem.EncodeToMemory(&block))
}

func generatePEMFromPKCS8ECKey() string {
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	privDER, _ := x509.MarshalPKCS8PrivateKey(ecKey)
	block := pem.Block{Type: "PRIVATE KEY", Bytes: privDER}
	return string(pem.EncodeToMemory(&block))
}
