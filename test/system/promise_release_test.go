package system_test

import (
	"github.com/syntasso/kratix/test/kubeutils"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PromiseRelease", func() {
	Describe("sourceRef.type == http", func() {
		BeforeEach(func() {
			SetDefaultEventuallyTimeout(2 * time.Minute)
			SetDefaultEventuallyPollingInterval(2 * time.Second)
			kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)

			platform.Kubectl("apply", "-f", "assets/promise-release/deployment.yaml")
			platform.Kubectl("wait", "-n", "kratix-platform-system", "deployments", "kratix-promise-release-test-hoster", "--for=condition=Available")
		})

		AfterEach(func() {
			platform.Kubectl("delete", "-f", "assets/promise-release/deployment.yaml")
		})

		It("can fetch and manage the promise from the specified url", func() {
			Eventually(func() string {
				return platform.Kubectl("apply", "-f", "assets/promise-release/promise-release.yaml")
			}).Should(ContainSubstring("promiserelease.platform.kratix.io/secure created"))

			By("pulling the Promise from plain http endpoints", func() {
				platform.EventuallyKubectl("get", "promiserelease", "insecure")
				platform.EventuallyKubectl("get", "promise", "insecurepro")
			})

			By("pulling the Promise from http endpoints with authorization", func() {
				platform.EventuallyKubectl("get", "promiserelease", "secure")
				platform.EventuallyKubectl("get", "promise", "securepro")
			})

			By("deleting all resources created by the PromiseRelease on deletion", func() {
				platform.EventuallyKubectlDelete("promiserelease", "insecure")
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise")).ShouldNot(ContainSubstring("insecurepro"))
					g.Expect(platform.Kubectl("get", "promiserelease")).ShouldNot(ContainSubstring("insecure"))
				}).Should(Succeed())

				platform.EventuallyKubectlDelete("promiserelease", "secure")
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise")).ShouldNot(ContainSubstring("securepro"))
					g.Expect(platform.Kubectl("get", "promiserelease")).ShouldNot(ContainSubstring("secure"))
				}).Should(Succeed())
			})
		})
	})
})
