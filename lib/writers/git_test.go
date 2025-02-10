package writers_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"log"

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
		writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest, creds)
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
			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest, creds)
			Expect(err).NotTo(HaveOccurred())
			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
			Expect(gitWriter.Path).To(HavePrefix("test"))

			stateStoreSpec.Path = ""
		})

		By("removing the leading slash when Destination.Path is defined", func() {
			dest.Spec.Path = "/dst-test"
			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest, creds)
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
			privateKeyPEM := pem.Block{
				Type:  "RSA PRIVATE KEY",
				Bytes: x509.MarshalPKCS1PrivateKey(key),
			}
			var b bytes.Buffer
			if err := pem.Encode(&b, &privateKeyPEM); err != nil {
				log.Fatalf("Failed to write private key to buffer: %v", err)
			}

			creds := map[string][]byte{
				"sshPrivateKey": b.Bytes(),
				"knownHosts":    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"),
			}

			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest, creds)
			Expect(err).NotTo(HaveOccurred())
			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
			Expect(gitWriter.GitServer.URL).To(Equal("https://github.com/syntasso/kratix"))
			publicKey, ok := gitWriter.GitServer.Auth.(*ssh.PublicKeys)
			Expect(ok).To(BeTrue())
			Expect(publicKey).NotTo(BeNil())
			Expect(gitWriter.GitServer.Branch).To(Equal("test"))
			Expect(gitWriter.Author.Email).To(Equal("test@example.com"))
			Expect(gitWriter.Author.Name).To(Equal("a-user"))
		})
	})
})
