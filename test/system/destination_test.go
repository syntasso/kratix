package system_test

import (
	"os"
	"strings"
	"time"

	"github.com/syntasso/kratix/test/kubeutils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Destinations", func() {
	Describe("status and events", func() {
		var destinationName string
		BeforeEach(func() {
			destinationName = "worker-3"

			SetDefaultEventuallyTimeout(2 * time.Minute)
			SetDefaultEventuallyPollingInterval(2 * time.Second)
			kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)

			if os.Getenv("LRE") != "true" {
				platform.Kubectl("apply", "-f", "assets/destination/destination-state-store.yaml")
			}

			platform.Kubectl("apply", "-f", "assets/destination/destination-worker-3.yaml")
		})

		AfterEach(func() {
			if os.Getenv("LRE") == "true" {
				// update the underlying state store with the valid secret
				platform.Kubectl("patch", "bucketstatestore", "destination-test-store", "--type=merge", "-p", `{"spec":{"secretRef":{"name":"aws-s3-credentials"}}}`)
			}
			platform.Kubectl("delete", "-f", "assets/destination/destination-worker-3.yaml")
		})

		It("properly set the conditions and events", func() {
			By("showing `Ready` as true", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "destinations", destinationName)
				}).Should(ContainSubstring("True"))
			})

			By("firing the success event", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "destinations", destinationName), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring(`Destination "worker-3" is ready`))
			})

			// set stateStoreRef to a non-existing one
			platform.Kubectl("patch", "destinations", destinationName, "--type=merge", "-p", `{"spec":{"stateStoreRef":{"name":"non-existing"}}}`)

			By("showing `Ready` as False when the State Store is wrong", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "destinations", destinationName)
				}).Should(ContainSubstring("False"))
			})

			By("firing a failure event", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "destinations", destinationName), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring("Failed to write test documents"))
			})

			// restore the ready condition
			platform.Kubectl("apply", "-f", "assets/destination/destination-worker-3.yaml")

			By("showing `Ready` as true", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "destinations", destinationName)
				}).Should(ContainSubstring("True"))
			})

			By("firing the success event", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "destinations", destinationName), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring(`Destination "worker-3" is ready`))
			})

			// update the underlying state store with an invalid secret
			platform.Kubectl("patch", "bucketstatestore", "destination-test-store", "--type=merge", "-p", `{"spec":{"secretRef":{"name":"non-existing"}}}`)
			By("showing `Ready` as False when the State Store is wrong", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "destinations", destinationName)
				}).Should(ContainSubstring("False"))
			})

			By("firing a failure event", func() {
				Eventually(func() string {
					describeOutput := strings.Split(platform.Kubectl("describe", "destinations", destinationName), "\n")
					return describeOutput[len(describeOutput)-2]
				}).Should(ContainSubstring("Failed to write test documents"))
			})
		})
	})
})
