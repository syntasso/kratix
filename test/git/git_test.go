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
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/util/git"
)

/*
NOTE: To run these tests configure the following environment variables:

TEST_GIT_WRITER_GITHUB_APP_ID
TEST_GIT_WRITER_GITHUB_APP_INSTALLATION_ID
# base64 encoded
TEST_GIT_WRITER_GITHUB_APP_PRIVATE_KEY
TEST_GIT_WRITER_GITHUB_HTTP_PAT
# base64 encoded
TEST_GIT_WRITER_GITHUB_SSH_PRIVATE_KEY

*/

// NOTE: These tests need to run in Serial mode as they use the same Git repository to store and verify data
var _ = Describe("Git tests", Serial, func() {

	logger := zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)).WithName("git-writer")
	httpPublicRepo := "https://github.com/syntasso/testing-git-writer-public.git"
	httpPrivateRepo := "https://github.com/syntasso/testing-git-writer-private.git"
	sshPrivateRepo := "ssh://git@github.com/syntasso/testing-git-writer-private.git"

	Describe("Git native client tests", func() {

		When("targeting a public repository", func() {

			When("using no auth", func() {
				var client git.Client

				It("successfully clones the repository", func() {
					By("initialising a new client", func() {

						var err error
						client, err = git.NewGitClient(
							git.GitClientRequest{
								RawRepoURL: httpPublicRepo,
								Auth:       &git.Auth{Creds: git.NopCreds{}},
								Insecure:   false,
							})
						Expect(err).ToNot(HaveOccurred())

						repo, err := client.Init()
						Expect(err).ToNot(HaveOccurred())
						Expect(repo).ToNot(BeNil())
					})

					By("fetching branches", func() {
						err := client.Fetch("main", 0)
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

				httpCreds := git.NewHTTPSCreds(
					"x-access-token",                        // username
					string(getGithubPATCreds()["password"]), // password
					"",                                      // bearer token
					"",                                      // clientCertData
					"",                                      // clientCertKey
					false,                                   // insecure
					git.NoopCredsStore{},                    // CredsStore,
					true,                                    // forceBasicAuth
				)

				It("successfully clones the repository", func() {
					client, err := git.NewGitClient(git.GitClientRequest{
						RawRepoURL: httpPrivateRepo,
						Root:       "",
						Auth:       &git.Auth{Creds: httpCreds},
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

				It("successfully pushes to repository", func() {
					client, err := git.NewGitClient(git.GitClientRequest{
						RawRepoURL: httpPrivateRepo,
						Root:       "",
						Auth:       &git.Auth{Creds: httpCreds},
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

					path := filepath.Join(client.Root(), "test.txt")
					file, err := os.Create(path)
					Expect(err).ToNot(HaveOccurred())
					// Defensive: if the file exists as a leftover from a previous test, the commit will still go
					randomContent := fmt.Sprintf("random-%d\n", rand.Int())
					_, err = file.WriteString(randomContent)
					Expect(err).ToNot(HaveOccurred())
					err = file.Close()
					Expect(err).ToNot(HaveOccurred())

					_, err = client.CommitAndPush(
						"main", "TEST: test", "test-user", "test-user@syntasso.io")
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(BeEmpty())

					// remove the test file
					err = os.Remove(path)
					Expect(err).ToNot(HaveOccurred())

					_, err = client.CommitAndPush(
						"main", "TEST: test", "test-user", "test-user@syntasso.io")
					Expect(err).ToNot(HaveOccurred())
					Expect(out).To(BeEmpty())
				})

				When("multiple clients are pushing to the same repo", func() {
					var (
						clientOne git.Client
						clientTwo git.Client
						pathOne   string
						pathTwo   string
						err       error
					)
					BeforeEach(func() {
						clientRoot1 := fmt.Sprintf("client-1-%d", rand.Int())
						clientRoot2 := fmt.Sprintf("client-2-%d", rand.Int())
						clientOne, err = git.NewGitClient(git.GitClientRequest{
							RawRepoURL: httpPrivateRepo,
							Root:       clientRoot1,
							Auth:       &git.Auth{Creds: httpCreds},
							Insecure:   true,
							Proxy:      "",
							NoProxy:    "",
						})
						Expect(err).ToNot(HaveOccurred())

						repoOne, err := clientOne.Init()
						Expect(err).ToNot(HaveOccurred())
						Expect(repoOne).ToNot(BeNil())

						err = clientOne.Fetch("main", 0)
						Expect(err).ToNot(HaveOccurred())

						out, err := clientOne.Checkout("main")
						Expect(err).ToNot(HaveOccurred())
						Expect(out).To(BeEmpty())

						// a second client clones, modifies and pushes to the same repo
						clientTwo, err = git.NewGitClient(git.GitClientRequest{
							RawRepoURL: httpPrivateRepo,
							Root:       clientRoot2,
							Auth:       &git.Auth{Creds: httpCreds},
							Insecure:   true,
							Proxy:      "",
							NoProxy:    "",
						})
						Expect(err).ToNot(HaveOccurred())

						repoTwo, err := clientTwo.Init()
						Expect(err).ToNot(HaveOccurred())
						Expect(repoTwo).ToNot(BeNil())

						err = clientTwo.Fetch("main", 0)
						Expect(err).ToNot(HaveOccurred())

						out, err = clientTwo.Checkout("main")
						Expect(err).ToNot(HaveOccurred())
						Expect(out).To(BeEmpty())

						pathOne = filepath.Join(clientOne.Root(), "test-client-1.txt")
						pathTwo = filepath.Join(clientTwo.Root(), "test-client-2.txt")
					})

					AfterEach(func() {
						var err error

						_, err = clientOne.Checkout("main")
						Expect(err).ToNot(HaveOccurred())

						_, err = clientTwo.Checkout("main")
						Expect(err).ToNot(HaveOccurred())

						// remove the test files
						err = os.Remove(pathOne)
						Expect(err).ToNot(HaveOccurred())
						err = os.Remove(pathTwo)
						Expect(err).ToNot(HaveOccurred())

						// Intentionally ignoring the errors as we are forcing the contents in the remote repo to be
						// reset
						clientOne.CommitAndPush(
							"main", "TEST: remove test file test-client-1.txt", "test-user", "test-user@syntasso.io")
						clientTwo.CommitAndPush(
							"main", "TEST: remove test file test-client-2.txt", "test-user", "test-user@syntasso.io")
					})

					It("has both clients able to commit to the repo without conflict", func() {
						By("creating a sample file for client 1", func() {
							fileOne, err := os.Create(pathOne)
							Expect(err).ToNot(HaveOccurred())
							err = fileOne.Close()
							Expect(err).ToNot(HaveOccurred())
						})

						By("creating a sample file for client 2", func() {
							fileTwo, err := os.Create(pathTwo)
							Expect(err).ToNot(HaveOccurred())
							err = fileTwo.Close()
							Expect(err).ToNot(HaveOccurred())
						})

						By("adding a non-conflicting file through the second client to bump the tip of the repository", func() {
							_, err := clientTwo.CommitAndPush(
								"main", "TEST: add test-client-2.txt", "test-user", "test-user@syntasso.io")
							Expect(err).ToNot(HaveOccurred())
						})

						By("being able to add another non-conflicting file through the first client even tough it was behind in git history", func() {
							_, err := clientOne.CommitAndPush(
								"main", "TEST: add test-client-1.txt", "test-user", "test-user@syntasso.io")
							Expect(err).ToNot(HaveOccurred())
						})
					})
				})

				When("no HTTP credentials provided", func() {

					It("does not clone the repository", func() {

						client, err := git.NewGitClient(git.GitClientRequest{
							RawRepoURL: httpPrivateRepo,
							Root:       "",
							Auth:       &git.Auth{Creds: git.NopCreds{}},
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

					It("does not clone the repository", func() {

						wrongHttpCreds := git.NewHTTPSCreds(
							"x-access-token",     // username
							"invalid",            // password
							"",                   // bearer token
							"",                   // clientCertData
							"",                   // clientCertKey
							false,                // insecure
							git.NoopCredsStore{}, // CredsStore,
							true,                 // forceBasicAuth
						)
						client, err := git.NewGitClient(git.GitClientRequest{
							RawRepoURL: httpPrivateRepo,
							Root:       "",
							Auth:       &git.Auth{Creds: wrongHttpCreds},
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
					client         git.Client
					err            error
					sshNativeCreds git.SSHCreds
				)

				githubCreds := getGithubSSHCreds()
				sshNativeCreds = git.NewSSHCreds(
					string(githubCreds["sshPrivateKey"]),
					string(githubCreds["knownHosts"]),
					"",
					false,
					"")

				It("successfully clones the repository", func() {
					client, err = git.NewGitClient(git.GitClientRequest{
						RawRepoURL: sshPrivateRepo,
						Auth:       &git.Auth{Creds: sshNativeCreds},
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
			)

			BeforeEach(func() {
				stateStoreSpec, dest = getStateStoreAndDest("githubApp", httpPrivateRepo)
			})

			It("validates permissions to the repository when credentials are correct", func() {
				githubAppCreds := getGithubAppCreds()
				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, githubAppCreds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				err = gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not instantiate the writer when appID is incorrect", func() {
				creds := getGithubAppCreds()
				creds["appID"] = []byte("1111111")

				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, creds)
				Expect(err).To(HaveOccurred())
				Expect(writer).To(BeNil())
			})

			It("does not instantiate the writer when installationID is incorrect", func() {
				creds := getGithubAppCreds()
				creds["installationID"] = []byte("1111111")

				writer, err := writers.NewGitWriter(logger, *stateStoreSpec, dest.Spec.Path, creds)
				Expect(err).To(HaveOccurred())
				Expect(writer).To(BeNil())
			})

			It("does not instantiate the writer when privateKey is missing", func() {
				creds := getGithubAppCreds()
				delete(creds, "privateKey")

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

				baseDir := getStateStoreAndDestBaseDir(stateStoreSpec, dest)
				path := filepath.Join(baseDir, resource.Filepath)
				_, err = gitWriter.ReadFile(path)
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not add the file and push the branch if the file content is not modified", func() {

				desc := fmt.Sprintf("test %d", rand.Int())
				resource, err := getTestDataToSave(desc)
				Expect(err).ToNot(HaveOccurred())

				// NOTE: when there's a single file in a dir,
				// `git rm` removes the entire dir
				baseDir := getStateStoreAndDestBaseDir(stateStoreSpec, dest)
				path := filepath.Join(baseDir, resource.Filepath)

				err = gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())

				// Create initial unique resources to save
				Expect(err).ToNot(HaveOccurred())
				_, err = gitWriter.UpdateFiles("", workloadName, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.ReadFile(path)
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

				_, err = gitWriter.UpdateFiles("", workloadName, nil, []string{path})
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.ReadFile(path)
				Expect(err).To(HaveOccurred())
				Expect(errors.Is(err, writers.ErrFileNotFound)).To(BeTrue())

				// restore deleted files
				resource, err = getTestDataToSave("")
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.UpdateFiles("", workloadName, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.ReadFile(path)
				Expect(err).ToNot(HaveOccurred())
			})

			It("successfully adds a new file to a private Git repository in a subdir", func() {
				resource, err := getTestDataToSave("")
				Expect(err).ToNot(HaveOccurred())

				baseDir := getStateStoreAndDestBaseDir(stateStoreSpec, dest)
				path := filepath.Join(baseDir, "test-subdir", resource.Filepath)

				_, err = gitWriter.UpdateFiles("test-subdir", workloadName, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.ReadFile(path)
				Expect(err).ToNot(HaveOccurred())
			})

			It("successfully adds and deletes a new file to a private Git repository in a subdir", func() {
				resource, err := getTestDataToSave("")
				Expect(err).ToNot(HaveOccurred())

				baseDir := getStateStoreAndDestBaseDir(stateStoreSpec, dest)
				path := filepath.Join(baseDir, "test-subdir", resource.Filepath)

				_, err = gitWriter.UpdateFiles("test-subdir", workloadName, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.ReadFile(path)
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.UpdateFiles("test-subdir", workloadName, nil, []string{path})
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.ReadFile(path)
				Expect(err).To(HaveOccurred())

				// restore the file
				_, err = gitWriter.UpdateFiles("test-subdir", workloadName, []v1alpha1.Workload{resource}, nil)
				Expect(err).ToNot(HaveOccurred())

				_, err = gitWriter.ReadFile(path)
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})
})

func getGithubAppCreds() map[string][]byte {
	envGithubAppAppID := os.Getenv("TEST_GIT_WRITER_GITHUB_APP_ID")
	envGithubAppInstallationId := os.Getenv("TEST_GIT_WRITER_GITHUB_APP_INSTALLATION_ID")
	return map[string][]byte{

		"appID":          []byte(envGithubAppAppID),
		"installationID": []byte(envGithubAppInstallationId),
		"privateKey":     getGithubAppPrivateKey(),
	}
}

func getGithubAppPrivateKey() []byte {
	envGithubAppPrivateKey := os.Getenv("TEST_GIT_WRITER_GITHUB_APP_PRIVATE_KEY")
	githubAppPrivateKey, err := base64.StdEncoding.DecodeString(envGithubAppPrivateKey)
	Expect(err).ToNot(HaveOccurred())
	return githubAppPrivateKey
}

func getGithubPATCreds() map[string][]byte {
	ghPat := os.Getenv("TEST_GIT_WRITER_GITHUB_HTTP_PAT")
	return map[string][]byte{
		"username": []byte("x-access-token"),
		"password": []byte(ghPat),
	}
}

func getGithubSSHCreds() map[string][]byte {
	envGithubSSHPrivateKey := os.Getenv("TEST_GIT_WRITER_GITHUB_SSH_PRIVATE_KEY")
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
				SecretRef: &v1.SecretReference{
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

func AddSpaces(s string) string {
	return strings.Join(strings.Split(s, ""), " ")
}
