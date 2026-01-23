package git_writer_test

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"

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
var _ = Describe("Git tests", func() {
	logger := zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)).WithName("git-writer")
	httpPrivateRepo := "https://github.com/syntasso/testing-git-writer-private.git"
	httpPublicRepo := "https://github.com/syntasso/testing-git-writer-public.git"
	sshPrivateRepo := "ssh://git@github.com/syntasso/testing-git-writer-private.git"

	var basicAuthCreds *git.Auth
	var branch string

	BeforeEach(func() {
		branch = "main"
		basicAuthCreds = genBasicAuthCreds()
	})

	Describe("Git native client tests", func() {
		DescribeTable("the commands with different authentication methods", func(repoURL string, genAuthFunc func() *git.Auth) {
			var client writers.GitExecutor
			var err error
			gitClientRequest := git.GitClientRequest{
				RawRepoURL: repoURL,
				Auth:       genAuthFunc(),
				Insecure:   true,
				Log:        logger,
			}

			By("initialising a new client", func() {
				client, err = git.NewGitClient(gitClientRequest)
				Expect(err).ToNot(HaveOccurred())
			})

			By("cloning the repository", func() {
				repoDir, err := client.Clone(branch)
				Expect(err).ToNot(HaveOccurred())
				Expect(repoDir).ToNot(BeEmpty())
				Expect(client.Root()).To(Equal(repoDir))
			})

			randomID := rand.Int()
			testDirectory, testFiles := createTestAssets(client.Root(), randomID)

			By("being able to commit files to the repository", func() {
				By("adding the new file to the repository", func() {
					_, err = client.Add(filepath.Join(client.Root(), testDirectory))
					Expect(err).ToNot(HaveOccurred())

					Expect(client.HasChanges()).To(BeTrue())

					_, err = client.CommitAndPush(
						branch, "TEST: test", "test-user", "test-user@syntasso.io")
					Expect(err).ToNot(HaveOccurred())
				})

				By("validating the files were pushed successfully", func() {
					validateOnRemoteRepo(gitClientRequest, branch, testFiles, randomID, false)
				})
			})

			By("being able to remove the files from the repository", func() {
				err = client.RemoveDirectory(filepath.Join(client.Root(), testDirectory))
				Expect(err).ToNot(HaveOccurred())

				_, err = client.CommitAndPush(branch, "TEST: remove files", "test-user", "test-user@syntasso.io")
				Expect(err).ToNot(HaveOccurred())

				By("validating the files were removed successfully", func() {
					validateOnRemoteRepo(gitClientRequest, branch, testFiles, randomID, true)
				})
			})

			By("cleaning up the local filesystem", func() {
				os.RemoveAll(client.Root())
			})
		},
			Entry("basic auth", httpPrivateRepo, genBasicAuthCreds),
			Entry("github app auth", httpPrivateRepo, genGithubAppCreds),
			Entry("ssh auth", sshPrivateRepo, genSSHCreds),
		)

		When("the repository is public", func() {
			It("can clone without authentication", func() {
				httpPublicRepo := "https://github.com/syntasso/testing-git-writer-public.git"
				client, err := git.NewGitClient(git.GitClientRequest{
					RawRepoURL: httpPublicRepo,
					Auth:       &git.Auth{Creds: git.NopCreds{}},
					Insecure:   true,
				})
				Expect(err).ToNot(HaveOccurred())

				_, err = client.Clone(branch)
				defer os.RemoveAll(client.Root())
				Expect(err).ToNot(HaveOccurred())
				Expect(client.Root()).ToNot(BeEmpty())

			})
		})

		When("the branch does not exist", func() {
			It("fails to clone the repository", func() {
				client, err := git.NewGitClient(git.GitClientRequest{
					RawRepoURL: httpPublicRepo,
					Auth:       &git.Auth{Creds: git.NopCreds{}},
					Insecure:   true,
				})
				Expect(err).ToNot(HaveOccurred())

				_, err = client.Clone("fake-invalid-branch")
				defer os.RemoveAll(client.Root())
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(
					ContainSubstring("couldn't find remote ref fake-invalid-branch")))
			})

			It("fails to clone the repository", func() {
				client, err := git.NewGitClient(git.GitClientRequest{
					RawRepoURL: httpPrivateRepo,
					Auth:       basicAuthCreds,
					Insecure:   true,
				})
				Expect(err).ToNot(HaveOccurred())

				_, err = client.Clone(branch)
				defer os.RemoveAll(client.Root())
				Expect(err).ToNot(HaveOccurred())

				testDir, _ := createTestAssets(client.Root(), rand.Int())
				_, err = client.Add(filepath.Join(client.Root(), testDir))
				Expect(err).ToNot(HaveOccurred())

				_, err = client.CommitAndPush("invalid-fake-branch", "TEST: test", "test-user", "test-user@syntasso.io")
				Expect(err).To(HaveOccurred())
				Expect(err).To(MatchError(
					ContainSubstring("src refspec invalid-fake-branch does not match any")))
			})
		})

		When("cannot clone the repository due to authentication errors", func() {
			It("returns an error", func() {
				client, err := git.NewGitClient(git.GitClientRequest{
					RawRepoURL: httpPrivateRepo,
					Auth:       &git.Auth{Creds: git.NopCreds{}},
					Insecure:   true,
				})
				Expect(err).ToNot(HaveOccurred())

				_, err = client.Clone(branch)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Repository not found"))
			})
		})

		When("the remote HEAD has changed", func() {
			var gitClientRequest git.GitClientRequest
			BeforeEach(func() {
				gitClientRequest = git.GitClientRequest{
					RawRepoURL: httpPrivateRepo,
					Auth:       basicAuthCreds,
					Insecure:   true,
					Log:        logger,
				}
			})

			It("can still commit and push to the repository when there are no conflicts", func() {
				clientOne, err := git.NewGitClient(gitClientRequest)
				Expect(err).ToNot(HaveOccurred())

				clientTwo, err := git.NewGitClient(gitClientRequest)
				Expect(err).ToNot(HaveOccurred())

				var pathOne, pathTwo string
				By("cloning to two different paths", func() {
					var err error
					pathOne, err = clientOne.Clone("main")
					Expect(err).ToNot(HaveOccurred())

					pathTwo, err = clientTwo.Clone("main")
					Expect(err).ToNot(HaveOccurred())
				})

				var testFilesOne, testFilesTwo []string
				var testDirOne, testDirTwo string
				randomIDOne := rand.Int()
				randomIDTwo := rand.Int()
				By("creating test assets", func() {
					testDirOne, testFilesOne = createTestAssets(pathOne, randomIDOne)
					testDirTwo, testFilesTwo = createTestAssets(pathTwo, randomIDTwo)
				})

				By("pushing the files to the repository", func() {
					_, err := clientOne.Add(filepath.Join(clientOne.Root(), testDirOne))
					Expect(err).ToNot(HaveOccurred())

					_, err = clientOne.CommitAndPush(
						"main", "TEST: test", "test-user", "test-user@syntasso.io")
					Expect(err).ToNot(HaveOccurred())

					_, err = clientTwo.Add(filepath.Join(clientTwo.Root(), testDirTwo))
					Expect(err).ToNot(HaveOccurred())

					_, err = clientTwo.CommitAndPush(
						"main", "TEST: test", "test-user", "test-user@syntasso.io")
					Expect(err).ToNot(HaveOccurred())
				})

				By("validating the files were pushed successfully", func() {
					validateOnRemoteRepo(gitClientRequest, branch, testFilesOne, randomIDOne, false)
					validateOnRemoteRepo(gitClientRequest, branch, testFilesTwo, randomIDTwo, false)
				})

				By("removing the files from the repository", func() {
					cleanUpRepo(clientOne, branch, testDirOne)
					cleanUpRepo(clientTwo, branch, testDirTwo)
				})

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

func getStateStore(authType, repo string) *v1alpha1.GitStateStoreSpec {
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
	}
}

func createTestAssets(root string, randomID int) (string, []string) {
	GinkgoHelper()

	randomDir := fmt.Sprintf("random-%d", randomID)
	files := []string{
		filepath.Join(randomDir, "test.txt"),
		filepath.Join(randomDir, "another-test.txt"),
		filepath.Join(randomDir, "third-test.txt"),
	}

	Expect(os.MkdirAll(filepath.Join(root, randomDir), 0755)).To(Succeed())

	for _, path := range files {
		file, err := os.Create(filepath.Join(root, path))
		Expect(err).ToNot(HaveOccurred())
		randomContent := fmt.Sprintf("random-%d\n", randomID)
		_, err = file.WriteString(randomContent)
		Expect(err).ToNot(HaveOccurred())
		Expect(file.Close()).To(Succeed())
	}

	return randomDir, files
}

func validateOnRemoteRepo(
	gitClientRequest git.GitClientRequest,
	branch string,
	testFiles []string,
	randomID int,
	testingDelete bool) {
	GinkgoHelper()

	client, err := git.NewGitClient(gitClientRequest)
	Expect(err).ToNot(HaveOccurred())

	repo, err := client.Clone(branch)
	Expect(err).ToNot(HaveOccurred())
	// defer os.RemoveAll(repo)

	for _, file := range testFiles {
		filePath := filepath.Join(repo, file)
		if testingDelete {
			Expect(filePath).ToNot(BeAnExistingFile())
		} else {
			Expect(filePath).To(BeAnExistingFile())
			content, err := os.ReadFile(filePath)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(content)).To(ContainSubstring(fmt.Sprintf("random-%d", randomID)))
		}
	}
}

func cleanUpRepo(client writers.GitExecutor, branch string, testDir string) {
	err := client.RemoveDirectory(filepath.Join(client.Root(), testDir))
	Expect(err).ToNot(HaveOccurred())

	_, err = client.CommitAndPush(branch, "TEST: remove files", "test-user", "test-user@syntasso.io")
	Expect(err).ToNot(HaveOccurred())

	os.RemoveAll(filepath.Join(client.Root()))
}

func genBasicAuthCreds() *git.Auth {
	return &git.Auth{
		Creds: git.NewHTTPSCreds(
			"x-access-token",
			string(getGithubPATCreds()["password"]),
			"",
			"",
			"",
			false,
			git.NoopCredsStore{},
			true,
		),
	}
}

func genSSHCreds() *git.Auth {
	return &git.Auth{
		Creds: git.NewSSHCreds(
			string(getGithubSSHCreds()["sshPrivateKey"]),
			string(getGithubSSHCreds()["knownHosts"]),
			"",
			false,
			"",
		),
	}
}

func genGithubAppCreds() *git.Auth {
	stateStoreSpec := getStateStore("githubApp", "https://github.com/syntasso/testing-git-writer-private.git")
	githubAppAuthCreds, err := git.SetAuth(
		*stateStoreSpec,
		getGithubAppCreds(),
	)
	Expect(err).ToNot(HaveOccurred())
	return githubAppAuthCreds
}
