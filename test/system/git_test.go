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
					"https://github.com/syntasso/helm-charts.git",
					dir, writers.NopCreds{},  false, "", "")
				Expect(err).ToNot(HaveOccurred())

				err = client.Init()
				Expect(err).ToNot(HaveOccurred())
			})
		*/

		It("checks out an open git repository and fetches the branches", func() {
			dir, err := os.MkdirTemp("", "test-prefix-*")
			//	defer os.RemoveAll(dir)
			Expect(err).ToNot(HaveOccurred())
			fmt.Printf("temp dir: %v\n", dir)

			client, err := writers.NewGitClient(
				"https://github.com/syntasso/helm-charts.git",
				dir, writers.NopCreds{}, false, "", "")
			Expect(err).ToNot(HaveOccurred())

			err = client.Init()
			Expect(err).ToNot(HaveOccurred())

			err = client.Fetch("main", 0)
			Expect(err).ToNot(HaveOccurred())

			out, err := client.Checkout("main")
			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(BeEmpty())
		})
	})
})
