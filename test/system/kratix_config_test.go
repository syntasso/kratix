package system_test

import (
	"fmt"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
	"time"
)

var _ = Describe("Kratix Config", func() {
	When("Security Context is set", func() {
		var promiseName, requestName = "configtest", "example-config"
		BeforeEach(func() {
			SetDefaultEventuallyTimeout(time.Minute)
			SetDefaultEventuallyPollingInterval(2 * time.Second)
			kubeutils.SetTimeoutAndInterval(time.Minute, 2*time.Second)

			platform.Kubectl("apply", "-f", "assets/kratix-config/promise.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "promise", promiseName)
			}).Should(ContainSubstring("Available"))
		})

		AfterEach(func() {
			platform.Kubectl("delete", "configtest", "example-config")
			platform.Kubectl("delete", "promise", "configtest")
		})

		It("uses security context from kratix config as default but allows overrides", func() {
			platform.Kubectl("apply", "-f", "assets/kratix-config/resource-request.yaml")

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
					return platform.Kubectl("get", "pods", "--selector", firstPipelineLabels)
				}).Should(ContainSubstring("Completed"))
			})

			By("using the security context defined in the promise", func() {
				podYaml := platform.EventuallyKubectl("get", "pods", "--selector", firstPipelineLabels, "-o=yaml")
				Expect(podYaml).To(ContainSubstring("setInPromise"))
				Expect(podYaml).NotTo(ContainSubstring("setInKratixConfig"))
			})

			By("executing the second pipeline pod", func() {
				Eventually(func() string {
					return platform.EventuallyKubectl("get", "pods", "--selector", secondPipelineLabels)
				}).Should(ContainSubstring("Completed"))
			})

			By("using the security context defined in the kratix config", func() {
				podYaml := platform.EventuallyKubectl("get", "pods", "--selector", secondPipelineLabels, "-o=yaml")
				Expect(podYaml).To(ContainSubstring("setInKratixConfig"))
				Expect(podYaml).NotTo(ContainSubstring("setInPromise"))
			})
		})
	})
})
