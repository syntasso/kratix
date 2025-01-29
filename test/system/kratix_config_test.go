package system_test

import (
	"fmt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"time"
)

var _ = Describe("Kratix Config", func() {
	When("Security Context is set", func() {
		var promiseName, requestName = "configtest", "example-config"
		BeforeEach(func() {
			SetDefaultEventuallyTimeout(30 * time.Second)
			SetDefaultEventuallyPollingInterval(time.Second)

			platform.kubectl("apply", "-f", "assets/kratix-config/promise.yaml")
			Eventually(func() string {
				return platform.kubectl("get", "promise", promiseName)
			}).Should(ContainSubstring("Available"))
		})

		AfterEach(func() {
			platform.kubectl("delete", "configtest", "example-config")
			platform.kubectl("delete", "promise", "configtest")
		})

		It("uses security context from kratix config as default but allows overrides", func() {
			platform.kubectl("apply", "-f", "assets/kratix-config/resource-request.yaml")

			firstPipelineLabels := fmt.Sprintf(
				"kratix.io/promise-name=%s,kratix.io/resource-name=%s,kratix.io/pipeline-name=%s",
				promiseName,
				requestName,
				"resource-pipeline0",
			)
			secondPipelineLabels := fmt.Sprintf(
				"kratix.io/promise-name=%s,kratix.io/resource-name=%s,kratix.io/pipeline-name=%s",
				promiseName,
				requestName,
				"resource-pipeline1",
			)

			By("executing the first pipeline pod", func() {
				Eventually(func() string {
					return platform.kubectl("get", "pods", "--selector", firstPipelineLabels)
				}).Should(ContainSubstring("Completed"))
			})

			By("using the security context defined in the promise", func() {
				podYaml := platform.eventuallyKubectl("get", "pods", "--selector", firstPipelineLabels, "-o=yaml")
				Expect(podYaml).To(ContainSubstring("setInPromise"))
				Expect(podYaml).NotTo(ContainSubstring("setInKratixConfig"))
			})

			By("executing the second pipeline pod", func() {
				Eventually(func() string {
					return platform.eventuallyKubectl("get", "pods", "--selector", secondPipelineLabels)
				}).Should(ContainSubstring("Completed"))
			})

			By("using the security context defined in the kratix config", func() {
				podYaml := platform.eventuallyKubectl("get", "pods", "--selector", secondPipelineLabels, "-o=yaml")
				Expect(podYaml).To(ContainSubstring("setInKratixConfig"))
				Expect(podYaml).NotTo(ContainSubstring("setInPromise"))
			})
		})
	})
})
