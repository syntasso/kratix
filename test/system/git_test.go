package system_test

import (
	"fmt"
	"os"

	gogit_http "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogit_ssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
)

//GitHub supports Authorization: Bearer for api.github.com (REST/GraphQL). The git endpoints on github.com for git fetch/clone typically require Basic (or credential helper / askpass).

var (
	dest                  v1alpha1.Destination
	stateStoreSpec        v1alpha1.GitStateStoreSpec
	logger                logr.Logger
	runSshTests           bool
	sshCreds              map[string][]byte
	sshPrivateKey         []byte
	ghPat                 string
	runHttpBasicAuthTests bool
	httpCreds             map[string][]byte
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

	stateStoreSpec = v1alpha1.GitStateStoreSpec{
		StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
			Path: "state-store-path",
			SecretRef: &corev1.SecretReference{
				Namespace: "default",
				Name:      "dummy-secret",
			},
		},
		AuthMethod: "basicAuth",
		URL:        "https://github.com/syntasso/testing-git-writer-public.git",
		Branch:     "main",
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
}

var _ = FDescribe("Git writer with native client", func() {

	BeforeEach(func() {
	})

	AfterEach(func() {
	})

	Describe("Repository check-out", func() {
		BeforeEach(func() {})

		AfterEach(func() {})

		It("checks out an open git repository but it does not pull the branches nor the code", func() {
			dir, err := os.MkdirTemp("", "test-prefix-*")
			//	defer os.RemoveAll(dir)
			Expect(err).ToNot(HaveOccurred())
			fmt.Printf("temp dir: %v\n", dir)
			client, err := writers.NewGitClient(
				writers.GitClientRequest{
					RawRepoURL: "https://github.com/syntasso/testing-git-writer-public.git",
					Root:       dir,
					Auth:       &writers.Authx{Creds: writers.NopCreds{}},
					Insecure:   false,
				})
			Expect(err).ToNot(HaveOccurred())

			repo, err := client.Init()
			Expect(err).ToNot(HaveOccurred())
			Expect(repo).ToNot(BeNil())
		})

		It("checks out an open git repository and fetches the branches", func() {
			dir, err := os.MkdirTemp("", "test-prefix-*")
			//	defer os.RemoveAll(dir)
			Expect(err).ToNot(HaveOccurred())
			fmt.Printf("temp dir: %v\n", dir)

			client, err := writers.NewGitClient(
				writers.GitClientRequest{
					RawRepoURL: "https://github.com/syntasso/testing-git-writer-public.git",
					Root:       dir,
					Auth:       &writers.Authx{Creds: writers.NopCreds{}},
					Insecure:   false,
				})
			Expect(err).ToNot(HaveOccurred())
			Expect(client).ToNot(BeNil())
			repo, err := client.Init()
			Expect(err).ToNot(HaveOccurred())
			Expect(repo).ToNot(BeNil())

			err = client.Fetch("main", 0)
			Expect(err).ToNot(HaveOccurred())

			out, err := client.Checkout("main")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(BeEmpty())
		})

		It("fails to check out a protected git repository due to no credentials provided", func() {
			dir, err := os.MkdirTemp("", "test-prefix-*")
			//	defer os.RemoveAll(dir)
			Expect(err).ToNot(HaveOccurred())
			fmt.Printf("temp dir: %v\n", dir)

			client, err := writers.NewGitClient(
				writers.GitClientRequest{
					RawRepoURL: "https://github.com/syntasso/testing-git-writer-private.git",
					Root:       dir,
					Auth:       &writers.Authx{Creds: writers.NopCreds{}},
					Insecure:   false,
				})
			Expect(err).ToNot(HaveOccurred())
			Expect(client).ToNot(BeNil())

			repo, err := client.Init()
			Expect(err).ToNot(HaveOccurred())
			Expect(repo).ToNot(BeNil())

			err = client.Fetch("main", 0)
			Expect(err).To(HaveOccurred())
		})

		// TODO: remove tmp dirs
		It("checks out a protected git repository and fetches the branches using SSH", func() {

			if !runSshTests {
				Skip("SSH tests not enabled")
			}

			dir, err := os.MkdirTemp("", "test-prefix-*")
			//	defer os.RemoveAll(dir)
			Expect(err).ToNot(HaveOccurred())
			fmt.Printf("temp dir: %v\n", dir)

			sshCreds := writers.NewSSHCreds(string(sshPrivateKey), "", false, "")

			client, err := writers.NewGitClient(writers.GitClientRequest{
				RawRepoURL: "ssh://git@github.com/syntasso/testing-git-writer-private.git",
				Root:       dir,
				Auth:       &writers.Authx{Creds: sshCreds},
				Insecure:   false,
				Proxy:      "",
				NoProxy:    "",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(client).ToNot(BeNil())

			repo, err := client.Init()
			Expect(err).ToNot(HaveOccurred())
			Expect(repo).ToNot(BeNil())

			err = client.Fetch("main", 0)
			Expect(err).ToNot(HaveOccurred())

			out, err := client.Checkout("main")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(BeEmpty())
		})

		It("checks out a protected git repository and fetches the branches using HTTP auth", func() {

			if !runHttpBasicAuthTests {
				Skip("HTTP basic auth tests not enabled")
			}

			httpCreds := writers.NewHTTPSCreds(
				"x-access-token",         // username
				ghPat,                    // password
				"",                       // bearer token
				"",                       // clientCertData
				"",                       // clientCertKey
				false,                    // insecure
				writers.NoopCredsStore{}, // CredsStore,
				true,                     // forceBasicAuth
			)
			client, err := writers.NewGitClient(writers.GitClientRequest{
				RawRepoURL: "https://github.com/syntasso/testing-git-writer-private.git",
				Root:       "",
				Auth:       &writers.Authx{Creds: httpCreds},
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
			Expect(err).ToNot(HaveOccurred())

			out, err := client.Checkout("main")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(BeEmpty())

			err = os.RemoveAll(client.Root())
			Expect(err).ToNot(HaveOccurred())
		})

		It("returns a valid GitWriter using SSH auth", func() {
			stateStoreSpec.AuthMethod = "ssh"
			stateStoreSpec.URL = "ssh://git@github.com/syntasso/testing-git-writer-public.git"

			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, sshCreds)
			Expect(err).NotTo(HaveOccurred())
			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())
			Expect(gitWriter.GitServer.URL).To(Equal("ssh://git@github.com/syntasso/testing-git-writer-public.git"))
			Expect(gitWriter.GitServer.Auth.(*gogit_ssh.PublicKeys).User).To(Equal("git"))
			publicKey, ok := gitWriter.GitServer.Auth.(*gogit_ssh.PublicKeys)
			Expect(ok).To(BeTrue())
			Expect(publicKey).NotTo(BeNil())
			Expect(gitWriter.GitServer.Branch).To(Equal("main"))
			Expect(gitWriter.Author.Email).To(Equal("test@example.com"))
			Expect(gitWriter.Author.Name).To(Equal("a-user"))
		})

		It("returns a valid GitWriter using HTTP basic auth", func() {
			stateStoreSpec.AuthMethod = "basicAuth"
			stateStoreSpec.URL = "https://github.com/syntasso/testing-git-writer-public.git"

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
			Expect(gitWriter.GitServer.URL).To(Equal("https://github.com/syntasso/testing-git-writer-public.git"))
			Expect(gitWriter.GitServer.Auth).To(Equal(&gogit_http.BasicAuth{
				Username: "user1",
				Password: "pw1",
			}))
			Expect(gitWriter.GitServer.Branch).To(Equal("main"))
			Expect(gitWriter.Author.Email).To(Equal("test@example.com"))
			Expect(gitWriter.Author.Name).To(Equal("a-user"))
			Expect(gitWriter.Path).To(Equal("state-store-path/dst-path"))
		})

		It("clones a protected git repository and fetches the branches using HTTP basic auth", func() {

			if !runHttpBasicAuthTests {
				Skip("HTTP basic auth tests not enabled")
			}

			stateStoreSpec.URL = "https://github.com/syntasso/testing-git-writer-private.git"

			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, httpCreds)
			Expect(err).ToNot(HaveOccurred())
			Expect(writer).ToNot(BeNil())

			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())

			err = gitWriter.ValidatePermissions()
			Expect(err).ToNot(HaveOccurred())
		})

		It("does not clone a protected git repository due to wrong HTTP basic auth creds", func() {
			stateStoreSpec.URL = "https://github.com/syntasso/testing-git-writer-private.git"
			creds := map[string][]byte{
				"username": []byte(""),
				"password": []byte("invalid"),
			}

			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, creds)
			Expect(err).ToNot(HaveOccurred())
			Expect(writer).ToNot(BeNil())

			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())

			err = gitWriter.ValidatePermissions()
			Expect(err).To(HaveOccurred())
		})

		It("clones a protected git repository and fetches the branches using SSH auth", func() {
			if !runSshTests {
				Skip("SSH tests not enabled")
			}
			stateStoreSpec.AuthMethod = "ssh"
			stateStoreSpec.URL = "ssh://git@github.com/syntasso/testing-git-writer-private.git"

			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, sshCreds)
			Expect(err).ToNot(HaveOccurred())
			Expect(writer).ToNot(BeNil())

			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())

			err = gitWriter.ValidatePermissions()
			Expect(err).ToNot(HaveOccurred())
		})

		It("does not clone a protected git repository and fetches the branches using SSH auth", func() {

			if !runSshTests {
				Skip("SSH tests not enabled")
			}

			stateStoreSpec.AuthMethod = "ssh"
			stateStoreSpec.URL = "ssh://git@github.com/syntasso/testing-git-writer-private.git"

			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, sshCreds)
			Expect(err).ToNot(HaveOccurred())
			Expect(writer).ToNot(BeNil())

			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())

			err = gitWriter.ValidatePermissions()
			Expect(err).ToNot(HaveOccurred())
		})

		It("clones a protected git repository and fetches the branches using GitHub App auth", func() {

			// app id 2056912
			// installation id 103412574
			///Users/luigi/syntasso/test-ssh/github-app-testing-git-writer-private.key
			stateStoreSpec.AuthMethod = "githubApp"
			stateStoreSpec.URL = "https://github.com/syntasso/testing-git-writer-private"

			githubAppPrivateKey := os.Getenv("KRATIX_GITHUB_APP_PRIVATE_KEY")
			if githubAppPrivateKey == "" {
				Skip("KRATIX_GITHUB_APP_PRIVATE_KEY not set")
			}

			datax, err := os.ReadFile(githubAppPrivateKey)
			Expect(err).ToNot(HaveOccurred())

			creds := map[string][]byte{
				// TODO: convert to env vars
				"appID":          []byte("2625348"),
				"installationID": []byte("103412574"),
				"privateKey":     datax,
			}

			writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, creds)
			if writer == nil {
				fmt.Println("xxxxxxxxxxxxxxxxxxxxxxxxxxxxvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvVVV111111111111111111")
			}
			Expect(err).ToNot(HaveOccurred())
			Expect(writer).ToNot(BeNil())
			/*
				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				if writer == nil {
					fmt.Println("xxxxxxxxxxxxxxxxxxxxxxxxxxxxvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvVVV111111111111111111.........")
				}
				gitWriter, ok := writer.(*writers.GitWriter)
				if gitWriter == nil {
					fmt.Println("xxxxxxxxxxxxxxxxxxxxxxxxxxxxvvvvvvvvvvvvvvvvvvvvvvvvvvvvvvVVV111111111111111111--------")
				}
				Expect(ok).To(BeTrue())
			*/

			//err = gitWriter.ValidatePermissions()
			err = writer.ValidatePermissions()
			Expect(err).ToNot(HaveOccurred())
		})

	})
})
