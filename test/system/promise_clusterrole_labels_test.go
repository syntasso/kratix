package system_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("Promise Features", Ordered, func() {
	Describe("spec.workflows.config.pipelineNamespace", func() {
		var (
			testNamespace     = "test"
			promiseFile       = "assets/promise-features/namespaced-workflow-promise.yaml"
			generatedResource = "namespacepromise"
		)

		BeforeEach(func() {
			SetDefaultEventuallyTimeout(2 * time.Minute)
			SetDefaultEventuallyPollingInterval(2 * time.Second)
			kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)
		})

		AfterEach(func() {
			platform.Kubectl("delete", "-f", promiseFile, "--ignore-not-found")
		})

		It("should allow an user inheriting premissions through aggregated RBAC", func() {
			By("creating a new namespace", func() {
				platform.Kubectl("create", "namespace", testNamespace)
				By("applying the promise", func() {
					platform.Kubectl("apply", "-f", promiseFile)
				})
				By("creating a new serviceaccount", func() {
					platform.Kubectl("create", "serviceaccount", "-n", testNamespace, "test-service-account")
				})
				By("creating a clusterrolebinding", func() {
					platform.Kubectl("create", "clusterrolebinding", "test-admin", "--clusterrole=admin", "--user=system:serviceaccount:"+testNamespace+":test-service-account")
				})
				var response string
				By("checking user cannot list dynamically generated objects", func() {
					Eventually(func() string {
						response = platform.KubectlAllowFail(
							"auth",
							"can-i",
							"get",
							generatedResource,
							"as",
							"system:serviceaccount:test:test-service-account")
						return response
					}).Should(ContainSubstring("no"))
				})
				By("Patching the cluster role", func() {
					platform.Kubectl("create", "clusterrole", "admin", "-p", "{\"aggregationRule\":{\"clusterRoleSelectors\":[{\"matchLabels\":{\"rbac.authorization.k8s.io/aggregate-to-admin\":\"true\"}}, {\"matchLabels\":{\"rbac.authorization.k8s.io/aggregate-to-kratix-for-test\":\"true\"}}]}}")
				})
				By("checking user can now list dynamically generated objects", func() {
					Eventually(func() string {
						response = platform.KubectlAllowFail(
							"can-i",
							"get",
							generatedResource,
							"as",
							"system:serviceaccount:test:test-service-account")
						return response
					}).Should(ContainSubstring("yes"))
				})
			})
		})
	})
})
