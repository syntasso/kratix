package system_test

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Promise Features", Ordered, func() {
	Describe("spec.workflows.config.pipelineNamespace", func() {
		var (
			kubePublicNs = "kube-public"
		)

		BeforeEach(func() {
			SetDefaultEventuallyTimeout(2 * time.Minute)
			SetDefaultEventuallyPollingInterval(2 * time.Second)
		})

		AfterEach(func() {
			platform.Kubectl("delete", "-f", "assets/promise-features/namespaced-promise.yaml", "--ignore-not-found")
		})

		It("should run all workflows in the specicied namespace", func() {
			By("applying the promise", func() {
				platform.Kubectl("apply", "-f", "assets/promise-features/namespaced-promise.yaml")

				By("running the workflow on the specified namespace", func() {
					promisePipelineLabels := []string{
						"kratix.io/workflow-type=promise",
						"kratix.io/workflow-action=configure",
						"kratix.io/promise-name=namespaced-promise",
						"kratix.io/pipeline-name=promise-configure",
					}

					Eventually(func() string {
						return platform.Kubectl(
							"get", "jobs",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(promisePipelineLabels, ","),
						)
					}).Should(ContainSubstring("kratix-namespaced-promise-promise"))
				})

				var workName string
				By("creating the work in the specified namespace", func() {
					workLabels := []string{
						"kratix.io/pipeline-name=promise-configure",
						"kratix.io/promise-name=namespaced-promise",
						"kratix.io/work-type=promise",
					}

					Eventually(func() string {
						workName = platform.Kubectl(
							"get", "works",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(workLabels, ","),
							"-o=jsonpath='{.items[0].metadata.name}'",
						)
						return workName
					}).Should(ContainSubstring("namespaced-promise-promise"))
				})

				By("creating the workplacement in the specified namespace", func() {
					workName = strings.Trim(workName, "'")
					workplacementLabels := []string{
						"kratix.io/work=" + workName,
						"kratix.io/pipeline-name=promise-configure",
					}
					Eventually(func() string {
						return platform.Kubectl(
							"get", "workplacements",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(workplacementLabels, ","),
						)
					}).Should(ContainSubstring(workName))
				})
			})

			By("applying the resource request", func() {
				platform.Kubectl("apply", "-f", "assets/promise-features/namespaced-resource-request.yaml")

				By("running the workflow on the specified namespace", func() {
					resourcePipelineLabels := []string{
						"kratix.io/workflow-type=resource",
						"kratix.io/workflow-action=configure",
						"kratix.io/promise-name=namespaced-promise",
						"kratix.io/pipeline-name=resource-configure",
					}

					Eventually(func() string {
						return platform.Kubectl(
							"get", "jobs",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(resourcePipelineLabels, ","),
						)
					}).Should(ContainSubstring("kratix-namespaced-promise-resource"))
				})

				var workName string
				By("creating the work in the specified namespace", func() {
					workLabels := []string{
						"kratix.io/pipeline-name=resource-configure",
						"kratix.io/promise-name=namespaced-promise",
						"kratix.io/work-type=resource",
						"kratix.io/resource-name=test-request",
					}

					Eventually(func() string {
						workName = platform.Kubectl(
							"get", "works",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(workLabels, ","),
							"-o=jsonpath='{.items[0].metadata.name}'",
						)
						return strings.Trim(workName, "'")
					}).Should(ContainSubstring("namespaced-promise-test-request"))

				})

				By("creating the workplacement in the specified namespace", func() {
					workName = strings.Trim(workName, "'")
					workplacementLabels := []string{
						"kratix.io/work=" + workName,
						"kratix.io/pipeline-name=resource-configure",
					}

					Eventually(func() string {
						return platform.Kubectl(
							"get", "workplacements",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(workplacementLabels, ","),
						)
					}).Should(ContainSubstring(workName))
				})
			})
		})
	})
})
