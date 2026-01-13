package system_test

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
)

//GitHub supports Authorization: Bearer for api.github.com (REST/GraphQL). The git endpoints on github.com for git fetch/clone typically require Basic (or credential helper / askpass).

const (
	httpPublicRepo  = "https://github.com/syntasso/testing-git-writer-public.git"
	httpPrivateRepo = "https://github.com/syntasso/testing-git-writer-private.git"

	sshPublicRepo  = "ssh://git@github.com/syntasso/testing-git-writer-public.git"
	sshPrivateRepo = "ssh://git@github.com/syntasso/testing-git-writer-private.git"
)

var (
	ghPat                   string
	githubAppPrivateKey     string
	githubAppPrivateKeyData []byte
	httpCreds               map[string][]byte
	logger                  logr.Logger
	runGitHubAppAuthTests   bool
	runHttpBasicAuthTests   bool
	runSshTests             bool
	sshCreds                map[string][]byte
	sshPrivateKey           []byte

	canaryWorkload      = "kratix-canary"
	resourcesDir        = "resources"
	canaryConfigMapPath = "kratix-canary-configmap.yaml"
	githubAppCreds      map[string][]byte
)

func setGitTestsEnv() {

	logger = ctrl.Log.WithName("setup")
	os.Setenv("GIT_TERMINAL_PROMPT", "0")
	os.Setenv("GIT_ASKPASS", "echo")

	sshDataPath := os.Getenv("KRATIX_SSH_DATA_PATH")
	if sshDataPath != "" {
		runSshTests = true
	}
	ghPat = os.Getenv("TEST_GH_PAT")
	if ghPat != "" {
		runHttpBasicAuthTests = true
	}

	var err error
	sshPrivateKey, err = os.ReadFile(fmt.Sprintf("%s/private.key", sshDataPath))
	Expect(err).ToNot(HaveOccurred())

	httpCreds = map[string][]byte{
		"username": []byte("x-access-token"),
		"password": []byte(ghPat),
	}

	sshCreds = map[string][]byte{
		"sshPrivateKey": sshPrivateKey,
		"knownHosts":    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"),
	}

	githubAppPrivateKey = os.Getenv("KRATIX_GITHUB_APP_PRIVATE_KEY")
	githubAppPrivateKeyData, err = os.ReadFile(githubAppPrivateKey)
	Expect(err).ToNot(HaveOccurred())
	if githubAppPrivateKey != "" {
		runGitHubAppAuthTests = true
	}

	githubAppCreds = map[string][]byte{
		// TODO: convert to env vars
		"appID":          []byte("2625348"),
		"installationID": []byte("103412574"),
		"privateKey":     githubAppPrivateKeyData,
	}
}

func newStateStoreAndDest(authType, repo string) (*v1alpha1.GitStateStoreSpec, *v1alpha1.Destination) {

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

// TODO: add test for root, when it's defined

var _ = FDescribe("Git tests", func() {

	setGitTestsEnv()

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

					if !runHttpBasicAuthTests {
						Skip("HTTP basic auth tests not enabled")
					}

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

					out, err = client.Checkout("main")
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
					client   writers.GitClient
					err      error
					sshCreds writers.SSHCreds
				)

				It("sets up authentication", func() {
					sshCreds = writers.NewSSHCreds(string(sshPrivateKey), "", false, "")
				})

				It("successfully clones the repository", func() {

					if !runSshTests {
						Skip("SSH tests not enabled")
					}

					client, err = writers.NewGitClient(writers.GitClientRequest{
						RawRepoURL: sshPrivateRepo,
						Auth:       &writers.Auth{Creds: sshCreds},
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

					out, err = client.Checkout("main")
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(BeEmpty())
				})

			})

		})
	})

	Describe("Git writer tests", func() {

		Describe("using SSH auth", func() {

			stateStoreSpec, dest := newStateStoreAndDest("ssh", sshPrivateRepo)

			It("validates the permissions if the credentials are correct", func() {
				if !runSshTests {
					Skip("SSH tests not enabled")
				}

				sshCreds := map[string][]byte{
					"sshPrivateKey": sshPrivateKey,
					"knownHosts":    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"),
				}

				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, sshCreds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				err = gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())
			})

			// TODO: it looks like the test above
			It("does not clone a protected git repository if the credentials are incorrect", func() {

				if !runSshTests {
					Skip("SSH tests not enabled")
				}

				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, sshCreds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				err = gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())
			})
		})

		Describe("using GitHub App auth", func() {

			stateStoreSpec, dest := newStateStoreAndDest("githubApp", httpPrivateRepo)

			It("validates permissions to the repository when credentials are correct", func() {
				if !runGitHubAppAuthTests {
					Skip("GitHub App auth tests not enabled")
				}

				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, githubAppCreds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				err = gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())
			})

			// TODO: should we test the behaviour for when installationID or private key are incorrect as well?
			It("does not instantiate the writer when appID is incorrect", func() {
				if !runGitHubAppAuthTests {
					Skip("GitHub App auth tests not enabled")
				}

				stateStoreSpec, dest := newStateStoreAndDest("githubApp", httpPrivateRepo)

				creds := githubAppCreds
				creds["appID"] = []byte("1111111")

				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, githubAppCreds)
				Expect(err).To(HaveOccurred())
				Expect(writer).To(BeNil())
			})
		})

		Describe("using HTTP basic auth", func() {
			It("successfully adds a new file to a private Git repository", func() {
				if !runHttpBasicAuthTests {
					Skip("HTTP basic auth tests not enabled")
				}
				stateStoreSpec, dest := newStateStoreAndDest("basicAuth", httpPrivateRepo)

				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, httpCreds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				resource, err := getResourcePathWithExample(gitWriter, "")
				Expect(err).ToNot(HaveOccurred())

				_, err = writer.UpdateFiles("", canaryWorkload, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())

				_, err = writer.ReadFile(resource.Filepath)
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not add the file and push the branch if the file content is not modified", func() {
				if !runHttpBasicAuthTests {
					Skip("HTTP basic auth tests not enabled")
				}
				stateStoreSpec, dest := newStateStoreAndDest("basicAuth", httpPrivateRepo)

				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, httpCreds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				err = gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())

				desc := fmt.Sprintf("test %d", rand.Int())
				resource, err := getResourcePathWithExample(gitWriter, desc)
				Expect(err).ToNot(HaveOccurred())
				_, err = writer.UpdateFiles("", canaryWorkload, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())

				resource, err = getResourcePathWithExample(gitWriter, desc)
				Expect(err).ToNot(HaveOccurred())
				fmt.Println("kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk")
				out, err := writer.UpdateFiles("", canaryWorkload, []v1alpha1.Workload{resource}, nil)
				fmt.Printf("OOOOOOOOOOOOOUUUUUUU: %v\n", out)
				Expect(err).To(HaveOccurred())

				// restore
				resource, err = getResourcePathWithExample(gitWriter, "")
				Expect(err).ToNot(HaveOccurred())
				_, err = writer.UpdateFiles("", canaryWorkload, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())
			})
		})

	})
})

func getResourcePathWithExample(writer writers.StateStoreWriter, content string) (v1alpha1.Workload, error) {

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
