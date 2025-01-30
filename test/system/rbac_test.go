package system_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
	"strings"
	"time"
)

var _ = Describe("Workflow-defined RBAC", Label("rbac"), Serial, func() {
	kratixSystemNs := "-n=kratix-platform-system"
	promiseWorkflowLabels := strings.Join([]string{
		"kratix.io/pipeline-name=rbac-pro",
		"kratix.io/pipeline-namespace=kratix-platform-system",
		"kratix.io/promise-name=rbac-promise",
	}, ",")
	resourceWorkflowLabels := strings.Join([]string{
		"kratix.io/pipeline-name=rbac-res",
		"kratix.io/pipeline-namespace=default",
		"kratix.io/promise-name=rbac-promise",
	}, ",")

	namespaceLabel := ",kratix.io/resource-namespace=default"
	kratixSystemNamespaceLabel := ",kratix.io/resource-namespace=kratix-platform-system"
	allNamespacesLabel := ",kratix.io/resource-namespace=kratix_all_namespaces"

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(time.Minute, 2*time.Second)

		platform.Kubectl("apply", "-f", "assets/rbac/resources.yaml")
		platform.Kubectl("apply", "-f", "assets/rbac/promise.yaml")
	})

	It("enables the workflows to access resources", func() {
		By("successfully completing the promise workflows", func() {
			Eventually(func() string { return platform.Kubectl("get", "promises", "rbac-promise") }).Should(ContainSubstring("Available"))
			Eventually(func() string {
				return platform.Kubectl("get", "promises", "rbac-promise", "-o=jsonpath='{.status.conditions}'")
			}).Should(ContainSubstring("PipelinesExecutedSuccessfully"))
		})

		var promiseRoleName, promiseRoleBindingName, promiseNamespacedCRName, promiseAllNamespacesCRName string
		By("creating the rbac objects", func() {
			promiseRoleName = strings.TrimSuffix(platform.Kubectl("get", "role", "-l", promiseWorkflowLabels, "-o=name", kratixSystemNs), "\n")
			promiseRoleBindingName = strings.TrimSuffix(platform.Kubectl("get", "rolebinding", "-l", promiseWorkflowLabels, "-o=name", kratixSystemNs), "\n")
			promiseNamespacedCRName = strings.TrimSuffix(platform.Kubectl("get", "clusterrole", "-l", promiseWorkflowLabels+namespaceLabel, "-o=name"), "\n")
			promiseAllNamespacesCRName = strings.TrimSuffix(platform.Kubectl("get", "clusterrole", "-l", promiseWorkflowLabels+allNamespacesLabel, "-o=name"), "\n")

			Expect(promiseRoleName).To(ContainSubstring("rbac-promise-promise-configure-rbac-pro"), "role not found")
			Expect(promiseRoleBindingName).To(ContainSubstring("rbac-promise-promise-configure-rbac-pro"), "role binding not found")
			Expect(promiseNamespacedCRName).To(ContainSubstring("rbac-promise-promise-configure-rbac-pro-default"), "namespaced clusterrole not found")
			Expect(promiseAllNamespacesCRName).To(ContainSubstring("rbac-promise-promise-configure-rbac-pro-kratix-all-names"), "all-namespaces clusterrole not found")
		})

		By("not recreating those objects on promise updates", func() {
			creationTimestampJsonPath := `-o=jsonpath='{.metadata.creationTimestamp}'`
			roleCreationTimestamp := platform.Kubectl("get", promiseRoleName, creationTimestampJsonPath, kratixSystemNs)
			roleBindingCreationTimestamp := platform.Kubectl("get", promiseRoleBindingName, creationTimestampJsonPath, kratixSystemNs)
			namespacedCRCreationTimestamp := platform.Kubectl("get", promiseNamespacedCRName, creationTimestampJsonPath)
			allNamespacesCRCreationTimestamp := platform.Kubectl("get", promiseAllNamespacesCRName, creationTimestampJsonPath)

			By("forcing a promise update", func() {
				platform.Kubectl("label", "promise", "rbac-promise", "kratix.io/manual-reconciliation=true")
				jsonpath := `-o=jsonpath='{.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].lastTransitionTime}'`
				lastPipelineExecution := platform.Kubectl("get", "promise", "rbac-promise", jsonpath)
				Eventually(func() string {
					return platform.Kubectl("get", "promises", "rbac-promise", "-o=jsonpath='{.status.lastAvailableTime}'")
				}).ShouldNot(Equal(lastPipelineExecution))
			})

			By("checking the rbac objects were not recreated", func() {
				Expect(platform.Kubectl("get", promiseRoleName, creationTimestampJsonPath, kratixSystemNs)).To(Equal(roleCreationTimestamp))
				Expect(platform.Kubectl("get", promiseRoleBindingName, creationTimestampJsonPath, kratixSystemNs)).To(Equal(roleBindingCreationTimestamp))
				Expect(platform.Kubectl("get", promiseNamespacedCRName, creationTimestampJsonPath)).To(Equal(namespacedCRCreationTimestamp))
				Expect(platform.Kubectl("get", promiseAllNamespacesCRName, creationTimestampJsonPath)).To(Equal(allNamespacesCRCreationTimestamp))
			})
		})

		var resRoleName, resRoleBindingName, resNamespacedCRName, resAllNamespacesCRName string
		By("sucessfully completing the resource workflows", func() {
			platform.Kubectl("apply", "-f", "assets/rbac/resource-request.yaml")

			Eventually(func() string {
				return platform.Kubectl("get", "rbacbundle", "rbac-resource-request")
			}).Should(ContainSubstring("Resource requested"))

			Eventually(func() string {
				return platform.Kubectl("get", "rbacbundle", "rbac-resource-request", "-o=jsonpath='{.status.conditions}'")
			}).Should(ContainSubstring("PipelinesExecutedSuccessfully"))
		})

		By("creating the rbac objects for the resource workflow", func() {
			resRoleName = platform.Kubectl("get", "role", "-l", resourceWorkflowLabels, "-o=name")
			resRoleBindingName = platform.Kubectl("get", "rolebinding", "-l", resourceWorkflowLabels, "-o=name")
			resNamespacedCRName = platform.Kubectl("get", "clusterrole", "-l", resourceWorkflowLabels+kratixSystemNamespaceLabel, "-o=name")
			resAllNamespacesCRName = platform.Kubectl("get", "clusterrole", "-l", resourceWorkflowLabels+allNamespacesLabel, "-o=name")

			Expect(resRoleName).To(ContainSubstring("rbac-promise-resource-configure-rbac-res"), "role not found")
			Expect(resRoleBindingName).To(ContainSubstring("rbac-promise-resource-configure-rbac-res"), "role binding not found")
			Expect(resNamespacedCRName).To(ContainSubstring("rbac-promise-resource-configure-rbac-res-kratix-plat"), "namespaced clusterrole not found")
			Expect(resAllNamespacesCRName).To(ContainSubstring("rbac-promise-resource-configure-rbac-res-kratix-all"), "all-namespaces clusterrole not found")
		})

		By("keeping the rbac objects for the resource workflow when the resource is deleted", func() {
			platform.EventuallyKubectlDelete("rbacbundle", "rbac-resource-request")

			resRoleName = platform.Kubectl("get", "role", "-l", resourceWorkflowLabels, "-o=name")
			resRoleBindingName = platform.Kubectl("get", "rolebinding", "-l", resourceWorkflowLabels, "-o=name")
			resNamespacedCRName = platform.Kubectl("get", "clusterrole", "-l", resourceWorkflowLabels+kratixSystemNamespaceLabel, "-o=name")
			resAllNamespacesCRName = platform.Kubectl("get", "clusterrole", "-l", resourceWorkflowLabels+allNamespacesLabel, "-o=name")

			Expect(resRoleName).To(ContainSubstring("rbac-promise-resource-configure-rbac-res"), "role was deleted")
			Expect(resRoleBindingName).To(ContainSubstring("rbac-promise-resource-configure-rbac-res"), "role binding was deleted")
			Expect(resNamespacedCRName).To(ContainSubstring("rbac-promise-resource-configure-rbac-res-kratix-plat"), "namespaced clusterrole was deleted")
			Expect(resAllNamespacesCRName).To(ContainSubstring("rbac-promise-resource-configure-rbac-res-kratix-all"), "all-namespaces clusterrole was deleted")
		})

		By("deleting all the rbac objects for the promise workflow when the promise is deleted", func() {
			platform.EventuallyKubectlDelete("promise", "rbac-promise")

			Eventually(func(g Gomega) {
				g.Expect(platform.Kubectl("get", "role", "-l", promiseWorkflowLabels, "-o=name", kratixSystemNs)).To(BeEmpty())
				g.Expect(platform.Kubectl("get", "rolebinding", "-l", promiseWorkflowLabels, "-o=name", kratixSystemNs)).To(BeEmpty())
				g.Expect(platform.Kubectl("get", "clusterrole", "-l", promiseWorkflowLabels+kratixSystemNamespaceLabel, "-o=name")).To(BeEmpty())
				g.Expect(platform.Kubectl("get", "clusterrole", "-l", promiseWorkflowLabels+allNamespacesLabel, "-o=name")).To(BeEmpty())

				g.Expect(platform.Kubectl("get", "role", "-l", resourceWorkflowLabels, "-o=name")).To(BeEmpty())
				g.Expect(platform.Kubectl("get", "rolebinding", "-l", resourceWorkflowLabels, "-o=name")).To(BeEmpty())
				g.Expect(platform.Kubectl("get", "clusterrole", "-l", resourceWorkflowLabels+kratixSystemNamespaceLabel, "-o=name")).To(BeEmpty())
				g.Expect(platform.Kubectl("get", "clusterrole", "-l", resourceWorkflowLabels+allNamespacesLabel, "-o=name")).To(BeEmpty())
			}).Should(Succeed(), "RBAC objects were not cleaned up after promise deletion")
		})
	})
})
