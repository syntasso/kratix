package system_test

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("Promise Features", Ordered, func() {
	Describe("spec.workflows.config.pipelineNamespace", func() {
		var (
			kubePublicNs        = "kube-public"
			promiseFile         = "assets/promise-features/namespaced-workflow-promise.yaml"
			resourceRequestFile = "assets/promise-features/namespaced-workflow-resource-request.yaml"
			promiseName         = "namespaced-promise"
			resourceRequestName = "test-request"
		)

		BeforeEach(func() {
			SetDefaultEventuallyTimeout(2 * time.Minute)
			SetDefaultEventuallyPollingInterval(2 * time.Second)
			kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)
		})

		AfterEach(func() {
			platform.Kubectl("delete", "-f", promiseFile, "--ignore-not-found")
		})

		It("should run all workflows in the specified namespace", func() {
			promisePipelineLabels := []string{
				"kratix.io/workflow-type=promise",
				"kratix.io/workflow-action=configure",
				"kratix.io/promise-name=namespaced-promise",
				"kratix.io/pipeline-name=promise-configure",
			}

			promiseWorkLabels := []string{
				"kratix.io/pipeline-name=promise-configure",
				"kratix.io/promise-name=namespaced-promise",
				"kratix.io/work-type=promise",
			}

			By("applying the promise", func() {
				platform.Kubectl("apply", "-f", promiseFile)

				By("running the workflow on the specified namespace", func() {
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
					Eventually(func() string {
						workName = platform.KubectlAllowFail(
							"get", "works",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(promiseWorkLabels, ","),
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

				By("updating status correctly", func() {
					promiseArgs := []string{"-n", "default", "get", "promise", promiseName}
					workflowCompletedCondition := `.status.conditions[?(@.type=="ConfigureWorkflowCompleted")]`
					reconciledCondition := `.status.conditions[?(@.type=="Reconciled")]`
					worksSucceededCondition := `.status.conditions[?(@.type=="WorksSucceeded")]`

					Eventually(func(g Gomega) {
						g.Expect(
							platform.Kubectl(append(promiseArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, workflowCompletedCondition))...),
						).To(ContainSubstring("True"))
						g.Expect(
							platform.Kubectl(append(promiseArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, worksSucceededCondition))...),
						).To(ContainSubstring("True"))
						g.Expect(
							platform.Kubectl(append(promiseArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, reconciledCondition))...),
						).To(ContainSubstring("True"))
					}).Should(Succeed())
				})
			})

			By("applying the resource request", func() {
				resourcePipelineLabels := []string{
					"kratix.io/workflow-type=resource",
					"kratix.io/workflow-action=configure",
					"kratix.io/promise-name=namespaced-promise",
					"kratix.io/pipeline-name=resource-configure",
				}
				resourceWorkLabels := []string{
					"kratix.io/pipeline-name=resource-configure",
					"kratix.io/promise-name=namespaced-promise",
					"kratix.io/work-type=resource",
					"kratix.io/resource-name=test-request",
				}

				platform.Kubectl("apply", "-f", resourceRequestFile)

				By("running the workflow on the specified namespace", func() {
					Eventually(func() string {
						return platform.Kubectl(
							"get", "jobs",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(resourcePipelineLabels, ","),
						)
					}).Should(ContainSubstring("kratix-namespaced-promise-test-request"))
				})

				var workName string
				By("creating the work in the specified namespace", func() {
					Eventually(func() string {
						workName = platform.KubectlAllowFail(
							"get", "works",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(resourceWorkLabels, ","),
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

				By("updating resource status correctly", func() {
					rrArgs := []string{"-n", "default", "get", "namespacepromises", resourceRequestName}
					workflowCompletedCondition := `.status.conditions[?(@.type=="ConfigureWorkflowCompleted")]`
					worksSucceededCondition := `.status.conditions[?(@.type=="WorksSucceeded")]`
					reconciledCondition := `.status.conditions[?(@.type=="Reconciled")]`

					Eventually(func(g Gomega) {
						g.Expect(
							platform.Kubectl(append(rrArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, workflowCompletedCondition))...),
						).To(ContainSubstring("True"))
						g.Expect(
							platform.Kubectl(append(rrArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, worksSucceededCondition))...),
						).To(ContainSubstring("True"))
						g.Expect(
							platform.Kubectl(append(rrArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, reconciledCondition))...),
						).To(ContainSubstring("True"))
					}).Should(Succeed())
				})

				By("cleaning up all resources at deletion", func() {
					platform.EventuallyKubectlDelete("-f", resourceRequestFile)
					Eventually(func() string {
						return platform.Kubectl(
							"get", "works",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(resourceWorkLabels, ","),
						)
					}).Should(ContainSubstring("No resources found in %s namespace", kubePublicNs))

					Eventually(func() string {
						return platform.Kubectl(
							"get", "jobs",
							"--namespace", kubePublicNs,
							"--selector", strings.Join(resourcePipelineLabels, ","),
						)
					}).Should(ContainSubstring("No resources found in %s namespace", kubePublicNs))
				})
			})

			By("cleaning up the promise", func() {
				platform.Kubectl("delete", "-f", promiseFile, "--ignore-not-found")

				Eventually(func() string {
					return platform.Kubectl(
						"get", "jobs",
						"--namespace", kubePublicNs,
						"--selector", strings.Join(promisePipelineLabels, ","),
					)
				}).Should(ContainSubstring("No resources found in %s namespace", kubePublicNs))

				Eventually(func() string {
					return platform.Kubectl(
						"get", "works",
						"--namespace", kubePublicNs,
						"--selector", strings.Join(promiseWorkLabels, ","),
					)
				}).Should(ContainSubstring("No resources found in %s namespace", kubePublicNs))
			})
		})
	})
})
