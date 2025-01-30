package system_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("Compound Promise", Label("compound-promise"), Serial, func() {
	BeforeEach(func() {
		SetDefaultEventuallyTimeout(2 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)

		platform.Kubectl("apply", "-f", "assets/compound-promise/promise.yaml")
	})

	AfterEach(func() {
		platform.EventuallyKubectlDelete("promise", "compound-promise")
		platform.EventuallyKubectlDelete("promise", "sub-promise")
	})

	When("installing a compound promise with `requiredPromises`", func() {
		It("correctly sets the Compound Promise availability", func() {
			By("marking the Promise as Unavailable until the its requirements are installed", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "promise", "compound-promise")
				}).Should(ContainSubstring("Unavailable"))
			})

			By("allowing resource requests to be created, but marking then as pending", func() {
				platform.Kubectl("apply", "-f", "assets/compound-promise/resource-request.yaml")
				Eventually(func() string {
					return platform.Kubectl("get", "compound", "example-compound-rr")
				}).Should(ContainSubstring("Pending"))
			})

			By("marking the Promise as Available once its requirements are installed", func() {
				subPromiseName := "sub-promise"
				platform.Kubectl("apply", "-f", "assets/compound-promise/sub-promise.yaml")

				Eventually(func() string {
					return platform.Kubectl("get", "promise")
				}).Should(ContainSubstring(subPromiseName))
				Eventually(func() string {
					return platform.Kubectl("get", "promise", "compound-promise")
				}).Should(ContainSubstring("Available"))
			})

			By("fulfilling the Resource Request", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "compound", "example-compound-rr")
				}, time.Minute*2, time.Second).Should(ContainSubstring("Resource requested"))
			})

			By("marking the Promise as Unavailable when the required promise is deleted", func() {
				platform.EventuallyKubectlDelete("promise", "sub-promise")

				Eventually(func() string {
					return platform.Kubectl("get", "promise", "compound-promise")
				}).Should(ContainSubstring("Unavailable"))
			})
		})
	})

	When("the compound promise is deleted", func() {
		It("does not delete the sub-promises", func() {
			platform.Kubectl("apply", "-f", "assets/compound-promise/sub-promise.yaml")
			platform.EventuallyKubectlDelete("promise", "compound-promise")

			Eventually(func() string {
				return platform.Kubectl("get", "promises")
			}).Should(SatisfyAll(
				ContainSubstring("sub-promise"),
				Not(ContainSubstring("compound-promise")),
			))
		})
	})
})
