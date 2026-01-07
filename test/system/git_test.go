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

		It("checks out an open git repository", func() {

			dir := os.TempDir()
			fmt.Printf("temp dir: %v\n", dir)

			client, err := writers.NewClientExt(
				"https://github.com/syntasso/helm-charts.git",
				dir, writers.NopCreds{}, false, false, "", "")
			Expect(err).ToNot(HaveOccurred())

			err = client.Init()
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
