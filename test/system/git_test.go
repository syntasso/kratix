package system_test

import (
	"os"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = FDescribe("Git writer with native client", func() {

	var (
		dest           v1alpha1.Destination
		stateStoreSpec v1alpha1.GitStateStoreSpec
		logger         logr.Logger
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
	})

	AfterEach(func() {
	})

	Describe("Repository check-out", func() {
		BeforeEach(func() {

		})

		AfterEach(func() {

		})

		/*
			It("checks out an open git repository but it does not pull the branches nor the code", func() {
				dir, err := os.MkdirTemp("", "test-prefix-*")
				//	defer os.RemoveAll(dir)
				Expect(err).ToNot(HaveOccurred())
				fmt.Printf("temp dir: %v\n", dir)
				client, err := writers.NewGitClient(
					"https://github.com/syntasso/testing-git-writer-public.git",
					dir, writers.NopCreds{}, false, "", "")
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
					"https://github.com/syntasso/testing-git-writer-public.git",
					dir, writers.NopCreds{}, false, "", "")
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

			It("fails to check out a protected git repository due to no credentials provided", func() {
				dir, err := os.MkdirTemp("", "test-prefix-*")
				//	defer os.RemoveAll(dir)
				Expect(err).ToNot(HaveOccurred())
				fmt.Printf("temp dir: %v\n", dir)

				client, err := writers.NewGitClient(
					"https://github.com/syntasso/testing-git-writer-private.git",
					dir, writers.NopCreds{}, false, "", "")

				Expect(err).ToNot(HaveOccurred())

				repo, err := client.Init()
				Expect(err).ToNot(HaveOccurred())
				Expect(repo).ToNot(BeNil())

				err = client.Fetch("main", 0)
				Expect(err).To(HaveOccurred())
			})

			// TODO: remove tmp dirs
			It("checks out a protected git repository and fetches the branches using SSH", func() {

				sshKeyPath := os.Getenv("TEST_SSH_KEY_PATH")
				if sshKeyPath == "" {
					Skip("TEST_SSH_KEY_PATH not set")
				}

				dir, err := os.MkdirTemp("", "test-prefix-*")
				//	defer os.RemoveAll(dir)
				Expect(err).ToNot(HaveOccurred())
				fmt.Printf("temp dir: %v\n", dir)

				sshPrivateKey, err := os.ReadFile(sshKeyPath)
				Expect(err).ToNot(HaveOccurred())

				sshCreds := writers.NewSSHCreds(string(sshPrivateKey), "", false, "")

				client, err := writers.NewGitClient(
					"git@github.com:syntasso/testing-git-writer-private.git",
					dir, sshCreds, true, "", "")

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
		*/

		/*
			It("checks out a protected git repository and fetches the branches using HTTP auth", func() {

				ghPat := os.Getenv("TEST_GH_PAT")
				if ghPat == "" {
					Skip("TEST_GH_PAT not set")
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
				//GitHub supports Authorization: Bearer for api.github.com (REST/GraphQL). The git endpoints on github.com for git fetch/clone typically require Basic (or credential helper / askpass).
				client, err := writers.NewGitClient(writers.GitClientRequest{
					RawRepoURL: "https://github.com/syntasso/testing-git-writer-private.git",
					Root:       "",
					Creds:      httpCreds,
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

				err = os.RemoveAll(client.Root())
				Expect(err).ToNot(HaveOccurred())
			})
		*/

		/*
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
		*/

		/*
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
				Expect(gitWriter.GitServer.Auth).To(Equal(&transporthttp.BasicAuth{
					Username: "user1",
					Password: "pw1",
				}))
				Expect(gitWriter.GitServer.Branch).To(Equal("test"))
				Expect(gitWriter.Author.Email).To(Equal("test@example.com"))
				Expect(gitWriter.Author.Name).To(Equal("a-user"))
				Expect(gitWriter.Path).To(Equal("state-store-path/dst-path"))
			})

		*/

		/*
			It("clones a protected git repository and fetches the branches using HTTP auth", func() {

				ghPat := os.Getenv("TEST_GH_PAT")
				if ghPat == "" {
					Skip("TEST_GH_PAT not set")
				}
				stateStoreSpec.URL = "https://github.com/syntasso/testing-git-writer-private.git"

				creds := map[string][]byte{
					"username": []byte("x-access-token"),
					"password": []byte(ghPat),
				}

				writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, creds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				err = gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not clone a protected git repository due to wrong basic auth creds", func() {
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
		*/
		/*
			It("clones a protected git repository and fetches the branches using SSH auth", func() {

				stateStoreSpec.AuthMethod = "ssh"
				stateStoreSpec.URL = "ssh://git@github.com/syntasso/testing-git-writer-private.git"

				sshDataPath := os.Getenv("KRATIX_SSH_DATA_PATH")
				if sshDataPath == "" {
					Skip("KRATIX_SSH_DATA_PATH not set")
				}

				datax, err := os.ReadFile(fmt.Sprintf("%s/private.key", sshDataPath))
				Expect(err).ToNot(HaveOccurred())

				creds := map[string][]byte{
					"sshPrivateKey": datax,
					"knownHosts":    []byte("github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"),
				}

				writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, creds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				err = gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())
			})

			It("does not clone a protected git repository and fetches the branches using SSH auth", func() {

				stateStoreSpec.AuthMethod = "ssh"
				stateStoreSpec.URL = "ssh://git@github.com/syntasso/testing-git-writer-private.git"

				key, err := rsa.GenerateKey(rand.Reader, 1024)
				Expect(err).NotTo(HaveOccurred())
				creds := writers.GenerateSSHCreds(key)

				writer, err := writers.NewGitWriter(logger, stateStoreSpec, dest.Spec.Path, creds)
				Expect(err).ToNot(HaveOccurred())
				Expect(writer).ToNot(BeNil())

				Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
				gitWriter, ok := writer.(*writers.GitWriter)
				Expect(ok).To(BeTrue())

				err = gitWriter.ValidatePermissions()
				Expect(err).ToNot(HaveOccurred())
			})
		*/

		It("clones a protected git repository and fetches the branches using GitHub App auth", func() {

			// app id 2056912
			// installation id 103412574
			///Users/luigi/syntasso/test-ssh/github-app-testing-git-writer-private.key
			stateStoreSpec.AuthMethod = "githubApp"
			stateStoreSpec.URL = "https://github.com/syntasso/testing-git-writer-private.git"

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
			Expect(err).ToNot(HaveOccurred())
			Expect(writer).ToNot(BeNil())

			Expect(writer).To(BeAssignableToTypeOf(&writers.GitWriter{}))
			gitWriter, ok := writer.(*writers.GitWriter)
			Expect(ok).To(BeTrue())

			err = gitWriter.ValidatePermissions()
			Expect(err).ToNot(HaveOccurred())
		})

	})
})
