package system_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PromiseRelease", func() {
	Describe("sourceRef.type == http", func() {
		BeforeEach(func() {
			platform.kubectl("apply", "-f", "assets/promise-release/deployment.yaml")
			platform.kubectl("wait", "-n", "kratix-platform-system", "deployments", "kratix-promise-release-test-hoster", "--for=condition=Available")
		})

		AfterEach(func() {
			platform.kubectl("delete", "-f", "assets/promise-release/deployment.yaml")
		})

		It("can fetch and manage the promise from the specified url", func() {
			Eventually(func() string {
				return platform.kubectl("apply", "-f", "assets/promise-release/promise-release.yaml")
			}, 30*time.Second, 1*time.Second).Should(ContainSubstring("promiserelease.platform.kratix.io/secure created"))

			By("pulling the Promise from plain http endpoints", func() {
				platform.eventuallyKubectl("get", "promiserelease", "insecure")
				platform.eventuallyKubectl("get", "promise", "insecurepro")
			})

			By("pulling the Promise from http endpoints with authorization", func() {
				platform.eventuallyKubectl("get", "promiserelease", "secure")
				platform.eventuallyKubectl("get", "promise", "securepro")
			})

			By("deleting all resources created by the PromiseRelease on deletion", func() {
				platform.eventuallyKubectlDelete("promiserelease", "insecure")
				Eventually(func(g Gomega) {
					g.Expect(platform.kubectl("get", "promise")).ShouldNot(ContainSubstring("insecurepro"))
					g.Expect(platform.kubectl("get", "promiserelease")).ShouldNot(ContainSubstring("insecure"))
				}).Should(Succeed())

				platform.eventuallyKubectlDelete("promiserelease", "secure")
				Eventually(func(g Gomega) {
					g.Expect(platform.kubectl("get", "promise")).ShouldNot(ContainSubstring("securepro"))
					g.Expect(platform.kubectl("get", "promiserelease")).ShouldNot(ContainSubstring("secure"))
				}).Should(Succeed())
			})
		})
	})
})
