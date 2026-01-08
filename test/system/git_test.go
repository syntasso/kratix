package system_test

import (
	"fmt"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/lib/writers"
)

var _ = FDescribe("Git writer with native client", func() {

	BeforeEach(func() {

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

		It("checks out a protected git repository and fetches the branches using HTTP auth", func() {

			ghPat := os.Getenv("TEST_GH_PAT")
			if ghPat == "" {
				Skip("TEST_GH_PAT not set")
			}

			dir, err := os.MkdirTemp("", "test-prefix-*")
			//	defer os.RemoveAll(dir)
			Expect(err).ToNot(HaveOccurred())
			fmt.Printf("temp dir: %v\n", dir)

			httpCreds := writers.NewHTTPSCreds(
				"", // username
				"", // password
				ghPat,
				"",                       // clientCertData
				"",                       // clientCertKey
				false,                    // insecure
				writers.NoopCredsStore{}, // CredsStore,
				true,                     // forceBasicAuth
			)

			//GitHub supports Authorization: Bearer for api.github.com (REST/GraphQL). The git endpoints on github.com for git fetch/clone typically require Basic (or credential helper / askpass).

			client, err := writers.NewGitClient(
				"https://github.com/syntasso/testing-git-writer-private.git",
				dir, httpCreds, true, "", "")

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
