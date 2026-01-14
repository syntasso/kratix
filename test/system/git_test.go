package system_test

import (
	"bytes"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TODO: add tests for root location, when it's defined
var _ = FDescribe("Git tests", Ordered, func() {

	logger := GinkgoLogr
	httpPublicRepo := "https://github.com/syntasso/testing-git-writer-public.git"
	httpPrivateRepo := "https://github.com/syntasso/testing-git-writer-private.git"
	sshPrivateRepo := "ssh://git@github.com/syntasso/testing-git-writer-private.git"

	Describe("Git native client tests", func() {

		When("targeting a public repository", func() {

			When("using no auth", func() {
				var (
					client writers.GitClient
					err    error
				)

				It("successfully clones the repository", func() {
					By("initialising a new client", func() {

						client, err = writers.NewGitClient(
							writers.GitClientRequest{
								RawRepoURL: httpPublicRepo,
								Auth:       &writers.Auth{Creds: writers.NopCreds{}},
								Insecure:   false,
							})
						Expect(err).ToNot(HaveOccurred())

						repo, err := client.Init()
						Expect(err).ToNot(HaveOccurred())
						Expect(repo).ToNot(BeNil())
					})

					By("fetching branches", func() {
						err = client.Fetch("main", 0)
						Expect(err).ToNot(HaveOccurred())

						out, err := client.Checkout("main")
						Expect(err).ToNot(HaveOccurred())
						Expect(out).To(BeEmpty())
					})

					By("checking out the code", func() {
						out, err := client.Checkout("main")
						Expect(err).ToNot(HaveOccurred())
						Expect(out).To(BeEmpty())
					})
				})
			})
		})

		When("targeting a private repository", func() {

			When("using HTTP basic auth", func() {

				var (
					client    writers.GitClient
					err       error
					httpCreds writers.GenericHTTPSCreds
				)

				It("sets up authentication", func() {
					// TODO: adapt for Gitlab
					creds := getGithubPATCreds()
					ghPat := string(creds["password"])
					httpCreds = writers.NewHTTPSCreds(
						"x-access-token",         // username
						ghPat,                    // password
						"",                       // bearer token
						"",                       // clientCertData
						"",                       // clientCertKey
						false,                    // insecure
						writers.NoopCredsStore{}, // CredsStore,
						true,                     // forceBasicAuth
					)
				})

				It("successfully clones the repository", func() {
					client, err = writers.NewGitClient(writers.GitClientRequest{
						RawRepoURL: httpPrivateRepo,
						Root:       "",
						Auth:       &writers.Auth{Creds: httpCreds},
						Insecure:   true,
						Proxy:      "",
						NoProxy:    "",
					})
					Expect(err).ToNot(HaveOccurred())

					repo, err := client.Init()
					Expect(err).ToNot(HaveOccurred())
					Expect(repo).ToNot(BeNil())

					err = client.Fetch("main", 0)
					Expect(err).ToNot(HaveOccurred())

					out, err := client.Checkout("main")
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(BeEmpty())
				})

				When("no HTTP credentials provided", func() {

					var (
						client writers.GitClient
						err    error
					)
					It("does not clone the repository", func() {

						client, err = writers.NewGitClient(writers.GitClientRequest{
							RawRepoURL: httpPrivateRepo,
							Root:       "",
							Auth:       &writers.Auth{Creds: writers.NopCreds{}},
							Insecure:   true,
							Proxy:      "",
							NoProxy:    "",
						})
						Expect(err).ToNot(HaveOccurred())
						Expect(client).ToNot(BeNil())

						repo, err := client.Init()
						Expect(err).ToNot(HaveOccurred())
						Expect(repo).ToNot(BeNil())

						err = client.Fetch("main", 0)
						Expect(err).To(HaveOccurred())
					})
				})

				When("wrong HTTP credentials are provided", func() {

					var (
						client writers.GitClient
						err    error
					)
					It("does not clone the repository", func() {

						wrongHttpCreds := writers.NewHTTPSCreds(
							"x-access-token",         // username
							"invalid",                // password
							"",                       // bearer token
							"",                       // clientCertData
							"",                       // clientCertKey
							false,                    // insecure
							writers.NoopCredsStore{}, // CredsStore,
							true,                     // forceBasicAuth
						)
						client, err = writers.NewGitClient(writers.GitClientRequest{
							RawRepoURL: httpPrivateRepo,
							Root:       "",
							Auth:       &writers.Auth{Creds: wrongHttpCreds},
							Insecure:   true,
							Proxy:      "",
							NoProxy:    "",
						})
						Expect(err).ToNot(HaveOccurred())
						Expect(client).ToNot(BeNil())

						repo, err := client.Init()
						Expect(err).ToNot(HaveOccurred())
						Expect(repo).ToNot(BeNil())

						err = client.Fetch("main", 0)
						Expect(err).To(HaveOccurred())
					})
				})
			})

			When("using SSH auth", func() {
				var (
					client         writers.GitClient
					err            error
					sshNativeCreds writers.SSHCreds
				)

				It("sets up authentication", func() {
					githubCreds := getGithubSSHCreds()
					sshPrivateKey := string(githubCreds["sshPrivateKey"])
					sshNativeCreds = writers.NewSSHCreds(sshPrivateKey,
						"",
						false,
						"")
				})

				It("successfully clones the repository", func() {
					client, err = writers.NewGitClient(writers.GitClientRequest{
						RawRepoURL: sshPrivateRepo,
						Auth:       &writers.Auth{Creds: sshNativeCreds},
						Insecure:   false,
						Proxy:      "",
						NoProxy:    "",
					})
					Expect(err).ToNot(HaveOccurred())

					repo, err := client.Init()
					Expect(err).ToNot(HaveOccurred())
					Expect(repo).ToNot(BeNil())

					err = client.Fetch("main", 0)
					Expect(err).ToNot(HaveOccurred())

					out, err := client.Checkout("main")
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(BeEmpty())
				})

			})

		})
	})

	Describe("Git writer tests", func() {
		var workloadName string

		BeforeEach(func() {
			workloadName = "kratix-canary"
		})

		Describe("using SSH auth", func() {

			stateStoreSpec, dest := getStateStoreAndDest("ssh", sshPrivateRepo)

			It("validates the permissions if the credentials are correct", func() {
				sshCreds := getGithubSSHCreds()
				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, sshCreds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				err = gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not clone a protected git repository if the credentials are incorrect", func() {
				sshCreds := getInvalidSSHCreds()
				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, sshCreds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				err = gitWriter.ValidatePermissions()
				Expect(err).To(HaveOccurred())
			})
		})

		Describe("using GitHub App auth", func() {
			var (
				stateStoreSpec *v1alpha1.GitStateStoreSpec
				dest           *v1alpha1.Destination
				gitWriter      *writers.GitWriter
			)

			BeforeEach(func() {
				var err error
				var ok bool
				stateStoreSpec, dest = getStateStoreAndDest("githubApp", httpPrivateRepo)

				githubAppCreds := getGithubAppCreds()
				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, githubAppCreds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok = writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

			})

			It("validates permissions to the repository when credentials are correct", func() {
				err := gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())
			})

			// TODO: should we test the behaviour for when installationID or private key are incorrect as well?
			It("does not instantiate the writer when appID is incorrect", func() {
				creds := getGithubAppCreds()
				creds["appID"] = []byte("1111111")

				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, creds)
				Expect(err).To(HaveOccurred())
				Expect(writer).To(BeNil())
			})
		})

		Describe("using HTTP basic auth", func() {
			var (
				stateStoreSpec *v1alpha1.GitStateStoreSpec
				dest           *v1alpha1.Destination
				httpCreds      map[string][]byte
				gitWriter      *writers.GitWriter
			)

			BeforeEach(func() {
				stateStoreSpec, dest = getStateStoreAndDest("basicAuth", httpPrivateRepo)
				httpCreds = getGithubPATCreds()

				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, httpCreds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				var ok bool
				gitWriter, ok = writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())
			})

			It("successfully adds a new file to a private Git repository", func() {
				resource, err := getTestDataToSave("")
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.UpdateFiles("", workloadName, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.ReadFile(resource.Filepath)
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not add the file and push the branch if the file content is not modified", func() {
				err := gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())

				// Create initial unique resources to save
				desc := fmt.Sprintf("test %d", rand.Int())
				resource, err := getTestDataToSave(desc)
				Expect(err).ToNot(HaveOccurred())
				_, err = gitWriter.UpdateFiles("", workloadName, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())

				// Ensure there are no updates for unchanged data
				resource, err = getTestDataToSave(desc)
				Expect(err).ToNot(HaveOccurred())
				_, err = gitWriter.UpdateFiles("", workloadName, []v1alpha1.Workload{resource}, nil)
				Expect(err).To(HaveOccurred())

				// Ensure it can save new data
				resource, err = getTestDataToSave("")
				Expect(err).ToNot(HaveOccurred())
				_, err = gitWriter.UpdateFiles("", workloadName, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())

				// NOTE: when there's a single file in a dir,
				// `git rm` removes the entire dir
				baseDir := getStateStoreAndDestBaseDir(stateStoreSpec, dest)
				path := filepath.Join(baseDir, resource.Filepath)
				_, err = gitWriter.UpdateFiles("", workloadName, nil, []string{path})
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.ReadFile(path)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, writers.ErrFileNotFound)).To(BeTrue())
			})
		})

	})
})

func getGithubAppCreds() map[string][]byte {
	envGithubAppAppID := os.Getenv("TEST_GIT_WRITER_SSH_GITHUB_APP_APPID")
	envGithubAppInstallationId := os.Getenv("TEST_GIT_WRITER_SSH_GITHUB_INSTALLATIONID")
	return map[string][]byte{

		"appID":          []byte(envGithubAppAppID),
		"installationID": []byte(envGithubAppInstallationId),
		"privateKey":     getGithubAppPrivateKey(),
	}
}

func getGithubAppPrivateKey() []byte {
	envGithubAppPrivateKey := os.Getenv("TEST_GIT_WRITER_SSH_GITHUB_APP_PRIVATE_KEY")
	githubAppPrivateKey, err := base64.StdEncoding.DecodeString(envGithubAppPrivateKey)
	Expect(err).ToNot(HaveOccurred())
	return githubAppPrivateKey
}

func getGithubPATCreds() map[string][]byte {
	ghPat := os.Getenv("TEST_GIT_WRITER_HTTP_GITHUB_PAT")
	return map[string][]byte{
		"username": []byte("x-access-token"),
		"password": []byte(ghPat),
	}
}

func getGithubSSHCreds() map[string][]byte {
	envGithubSSHPrivateKey := os.Getenv("TEST_GIT_WRITER_SSH_GITHUB_PRIVATE_KEY")
	githubSSHPrivateKey, err := base64.StdEncoding.DecodeString(envGithubSSHPrivateKey)
	Expect(err).ToNot(HaveOccurred())
	return map[string][]byte{
		"sshPrivateKey": githubSSHPrivateKey,
		"knownHosts":    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"),
	}
}

func getTestDataToSave(content string) (v1alpha1.Workload, error) {
	resourcesDir := "resources"
	canaryConfigMapPath := "kratix-canary-configmap.yaml"
	if content == "" {
		content = fmt.Sprintf("this confirms your infrastructure is reading from Kratix state stores (%d)", rand.Int())
	}
	kratixConfigMap := &v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kratix-info",
			Namespace: "kratix-worker-system",
		},
		Data: map[string]string{
			"test": content,
		},
	}
	nsBytes, err := yaml.Marshal(kratixConfigMap)
	Expect(err).ToNot(HaveOccurred())

	resource := v1alpha1.Workload{
		Filepath: filepath.Join(resourcesDir, canaryConfigMapPath),
		Content:  string(nsBytes),
	}

	return resource, err
}

func getInvalidSSHCreds() map[string][]byte {
	key, err := rsa.GenerateKey(cryptorand.Reader, 1024)
	Expect(err).NotTo(HaveOccurred())

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

func getStateStoreAndDestBaseDir(store *v1alpha1.GitStateStoreSpec, dest *v1alpha1.Destination) string {
	return fmt.Sprintf("%s/%s", store.Path, dest.Spec.Path)
}

func getStateStoreAndDest(authType, repo string) (*v1alpha1.GitStateStoreSpec, *v1alpha1.Destination) {

	return &v1alpha1.GitStateStoreSpec{
			StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
				Path: "state-store-path",
				SecretRef: &corev1.SecretReference{
					Namespace: "default",
					Name:      "dummy-secret",
				},
			},
			AuthMethod: authType,
			URL:        repo,
			Branch:     "main",
			GitAuthor: v1alpha1.GitAuthor{
				Email: "test@example.com",
				Name:  "a-user",
			},
		}, &v1alpha1.Destination{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default",
				Name:      "test",
			},
			Spec: v1alpha1.DestinationSpec{
				Path: fmt.Sprintf("%s-dst-path/", authType),
			},
		}
}
