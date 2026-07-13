package system_test

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

const (
	suspendPromiseName       = "workflow-suspend"
	suspendResource          = "assets/workflow-control/resource-request.yaml"
	suspendResourceName      = "suspend-test"
	suspendCRDPlural         = "workflowsuspends"
	suspendConfigMap         = "assets/workflow-control/configmap.yaml"
	suspendPromiseDeleteGate = "workflow-suspend-promise-delete-gate"

	retryPromiseName        = "workflow-retry"
	retryResourceGate       = "workflow-retry-resource-gate"
	retryResourceDeleteGate = "workflow-retry-delete-gate"
	retryPromiseDeleteGate  = "workflow-retry-promise-delete-gate"
)

var _ = Describe("Workflow Control", func() {
	dependentCM := "workflow-retry-test"

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)
	})

	When("the file has 'retryAfter' set", func() {
		BeforeEach(func() {
			// clear all retry gates in case a previous run was interrupted
			platform.EventuallyKubectlDelete("cm", dependentCM, "-n", "kratix-platform-system", "--ignore-not-found")
			platform.Kubectl("delete", "cm", retryResourceGate, retryResourceDeleteGate, "-n", "default", "--ignore-not-found")
			platform.Kubectl("delete", "cm", retryPromiseDeleteGate, "-n", "kratix-platform-system", "--ignore-not-found")
		})

		AfterEach(func() {
			// open all gates so teardown is never blocked by a retrying
			// configure or delete pipeline
			platform.KubectlAllowFail("create", "cm", retryResourceGate, "-n", "default")
			platform.KubectlAllowFail("create", "cm", retryResourceDeleteGate, "-n", "default")
			platform.KubectlAllowFail("create", "cm", retryPromiseDeleteGate, "-n", "kratix-platform-system")
			platform.EventuallyKubectlDelete("cm", dependentCM, "-n", "kratix-platform-system", "--ignore-not-found")
			platform.EventuallyKubectlDelete("promise", retryPromiseName, "--ignore-not-found")
			platform.Kubectl("delete", "cm", retryResourceGate, "-n", "default", "--ignore-not-found")
			platform.Kubectl("delete", "cm", retryResourceDeleteGate, "-n", "default", "--ignore-not-found")
			platform.Kubectl("delete", "cm", retryPromiseDeleteGate, "-n", "kratix-platform-system", "--ignore-not-found")
		})

		It("works", func() {
			By("retrying the Promise pipeline after the interval", func() {
				platform.Kubectl("apply", "-f", "assets/workflow-control/promise-retry.yaml")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-retry"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, messageJSONPath("pipe-retry"))).To(Equal("configmap workflow-retry-test not found"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, nextRetryAtJSONPath("pipe-retry"))).NotTo(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, attemptsJSONPath("pipe-retry"))).To(Equal("1"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())
				Expect(jobCountForPromisePipeline(retryPromiseName, "pipe-2")).To(Equal(0))

				Eventually(func() int {
					return jobCountForPromisePipeline(retryPromiseName, "pipe-retry")
				}).Should(Equal(2))

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-retry"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, attemptsJSONPath("pipe-retry"))).To(Equal("2"))
				}).Should(Succeed())
			})

			By("not retrying if retryAfter is not configured", func() {
				platform.Kubectl("create", "-n", "kratix-platform-system", "cm", dependentCM)

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-retry"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-2"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, nextRetryAtJSONPath("pipe-retry"))).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, attemptsJSONPath("pipe-retry"))).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName)).To(ContainSubstring("Available"))
				}).Should(Succeed())
			})

			By("retrying a resource request pipeline after the interval", func() {
				platform.Kubectl("apply", "-f", "assets/workflow-control/resource-request-retry.yaml")

				Eventually(func(g Gomega) {
					g.Expect(jobCountForResourcePipeline(retryPromiseName, "resource-pipe-retry")).To(Equal(1))
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", phaseJSONPath("resource-pipe-retry"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", nextRetryAtJSONPath("resource-pipe-retry"))).NotTo(BeEmpty())
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", attemptsJSONPath("resource-pipe-retry"))).To(Equal("1"))
				}).Should(Succeed())

				Eventually(func(g Gomega) {
					g.Expect(jobCountForResourcePipeline(retryPromiseName, "resource-pipe-retry")).To(Equal(2))
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", attemptsJSONPath("resource-pipe-retry"))).To(Equal("2"))
				}).Should(Succeed())
			})

			By("completing the resource configure once its gate configmap exists", func() {
				platform.Kubectl("create", "cm", retryResourceGate, "-n", "default")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", phaseJSONPath("resource-pipe-retry"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(BeEmpty())
				}).Should(Succeed())
			})

			By("retrying the resource delete pipeline after the interval", func() {
				platform.Kubectl("delete", "-f", "assets/workflow-control/resource-request-retry.yaml", "--wait=false")

				Eventually(func(g Gomega) {
					g.Expect(jobCountForWorkflow("resource", retryPromiseName, "resource-delete-retry-pipe", "delete")).To(Equal(1))
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", phaseJSONPath("resource-delete-retry-pipe"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", messageJSONPath("resource-delete-retry-pipe"))).To(Equal("waiting for delete gate configmap"))
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", nextRetryAtJSONPath("resource-delete-retry-pipe"))).NotTo(BeEmpty())
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", attemptsJSONPath("resource-delete-retry-pipe"))).To(Equal("1"))
				}).Should(Succeed())

				By("reporting only the delete pipeline in the workflow status", func() {
					Expect(platform.Kubectl("get", "workflowretries", "retry-test",
						`-o=jsonpath={.status.kratix.workflows.pipelines[*].name}`)).To(Equal("resource-delete-retry-pipe"))
				})

				By("setting the DeleteWorkflowCompleted condition while retrying", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.Kubectl("get", "workflowretries", "retry-test",
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].status}`)).To(Equal("False"))
						g.Expect(platform.Kubectl("get", "workflowretries", "retry-test",
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].reason}`)).To(Equal("DeleteWorkflowSuspended"))
					}).Should(Succeed())
				})

				By("keep on retrying", func() {
					Eventually(func(g Gomega) {
						g.Expect(jobCountForWorkflow("resource", retryPromiseName, "resource-delete-retry-pipe", "delete")).To(Equal(2))
						g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", attemptsJSONPath("resource-delete-retry-pipe"))).To(Equal("2"))
					}).Should(Succeed())
				})
			})

			By("completing deletion once the delete gate configmap exists", func() {
				platform.Kubectl("create", "cm", retryResourceDeleteGate, "-n", "default")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", "--ignore-not-found")).To(BeEmpty())
				}).Should(Succeed())
			})

			By("retrying the promise delete pipeline after the interval", func() {
				platform.Kubectl("delete", "promise", retryPromiseName, "--wait=false")

				Eventually(func(g Gomega) {
					g.Expect(jobCountForWorkflow("promise", retryPromiseName, "promise-delete-retry-pipe", "delete")).To(Equal(1))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("promise-delete-retry-pipe"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, messageJSONPath("promise-delete-retry-pipe"))).To(Equal("waiting for promise delete gate configmap"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, nextRetryAtJSONPath("promise-delete-retry-pipe"))).NotTo(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, attemptsJSONPath("promise-delete-retry-pipe"))).To(Equal("1"))
				}).Should(Succeed())

				By("setting the DeleteWorkflowCompleted condition while retrying", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.Kubectl("get", "promise", retryPromiseName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].status}`)).To(Equal("False"))
						g.Expect(platform.Kubectl("get", "promise", retryPromiseName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].reason}`)).To(Equal("DeleteWorkflowSuspended"))
					}).Should(Succeed())
				})

				By("keep on retrying", func() {
					Eventually(func(g Gomega) {
						g.Expect(jobCountForWorkflow("promise", retryPromiseName, "promise-delete-retry-pipe", "delete")).To(Equal(2))
						g.Expect(platform.Kubectl("get", "promise", retryPromiseName, attemptsJSONPath("promise-delete-retry-pipe"))).To(Equal("2"))
					}).Should(Succeed())
				})
			})

			By("completing promise deletion once its gate configmap exists", func() {
				platform.Kubectl("create", "cm", retryPromiseDeleteGate, "-n", "kratix-platform-system")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, "--ignore-not-found")).To(BeEmpty())
				}).Should(Succeed())
			})
		})

	})

	When("pipelines are suspended by the workflow control file", func() {
		BeforeEach(func() {
			// clear all suspend gates in case a previous run was interrupted
			platform.Kubectl("delete", "-f", suspendConfigMap, "--ignore-not-found")
			platform.Kubectl("delete", "cm", suspendPromiseDeleteGate, "-n", "kratix-platform-system", "--ignore-not-found")
		})

		AfterEach(func() {
			// remove all suspend gates and labels so teardown is never
			// blocked by a suspended delete pipeline
			platform.Kubectl("delete", "-f", suspendConfigMap, "--ignore-not-found")
			platform.Kubectl("delete", "cm", suspendPromiseDeleteGate, "-n", "kratix-platform-system", "--ignore-not-found")
			platform.KubectlAllowFail("label", suspendCRDPlural, suspendResourceName, "kratix.io/workflow-suspended-")
			platform.KubectlAllowFail("label", "promise", suspendPromiseName, "kratix.io/workflow-suspended-")
			platform.EventuallyKubectlDelete("promise", suspendPromiseName, "--ignore-not-found")
		})

		It("works", func() {
			By("suspending the correct pipeline", func() {
				platform.Kubectl("apply", "-f", "assets/workflow-control/promise.yaml")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-1"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, messageJSONPath("pipe-1"))).To(Equal("waiting for approval"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-2"))).To(Equal("Pending"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())

				Consistently(func() int {
					return jobCountForPromisePipeline(suspendPromiseName, "pipe-2")
				}, 10*time.Second).Should(Equal(0))
			})

			By("removing the suspend label and resuming from the suspended pipeline", func() {
				pipe0Count := jobCountForPromisePipeline(suspendPromiseName, "pipe-0")
				pipe1Count := jobCountForPromisePipeline(suspendPromiseName, "pipe-1")
				pipe2Count := jobCountForPromisePipeline(suspendPromiseName, "pipe-2")

				platform.Kubectl("label", "promise", suspendPromiseName, "kratix.io/workflow-suspended-")

				Eventually(func() int {
					return jobCountForPromisePipeline(suspendPromiseName, "pipe-1")
				}).Should(Equal(pipe1Count + 1))
				Consistently(func() int {
					return jobCountForPromisePipeline(suspendPromiseName, "pipe-0")
				}, 10*time.Second).Should(Equal(pipe0Count))
				Consistently(func() int {
					return jobCountForPromisePipeline(suspendPromiseName, "pipe-2")
				}, 10*time.Second).Should(Equal(pipe2Count))

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-1"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, messageJSONPath("pipe-1"))).To(Equal("waiting for approval"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-2"))).To(Equal("Pending"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())
			})

			By("adding the manual reconciliation label and restarting from the beginning", func() {
				pipe0Count := jobCountForPromisePipeline(suspendPromiseName, "pipe-0")
				pipe1Count := jobCountForPromisePipeline(suspendPromiseName, "pipe-1")
				pipe2Count := jobCountForPromisePipeline(suspendPromiseName, "pipe-2")

				platform.Kubectl("label", "promise", suspendPromiseName, "kratix.io/manual-reconciliation=true", "--overwrite")

				Eventually(func() int {
					return jobCountForPromisePipeline(suspendPromiseName, "pipe-0")
				}).Should(Equal(pipe0Count + 1))
				Eventually(func() int {
					return jobCountForPromisePipeline(suspendPromiseName, "pipe-1")
				}).Should(Equal(pipe1Count + 1))
				Consistently(func() int {
					return jobCountForPromisePipeline(suspendPromiseName, "pipe-2")
				}, 10*time.Second).Should(Equal(pipe2Count))

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-1"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, messageJSONPath("pipe-1"))).To(Equal("waiting for approval"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-2"))).To(Equal("Pending"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())
			})

			By("updating the promise spec and restarting from the beginning", func() {
				pipe0Count := jobCountForPromisePipeline(suspendPromiseName, "pipe-0")
				pipe1Count := jobCountForPromisePipeline(suspendPromiseName, "pipe-1")
				pipe2Count := jobCountForPromisePipeline(suspendPromiseName, "pipe-2")

				platform.Kubectl("apply", "-f", "assets/workflow-control/promise-updated.yaml")

				Eventually(func() int {
					return jobCountForPromisePipeline(suspendPromiseName, "pipe-0")
				}).Should(Equal(pipe0Count + 1))
				Eventually(func() int {
					return jobCountForPromisePipeline(suspendPromiseName, "pipe-1")
				}).Should(Equal(pipe1Count + 1))
				Eventually(func() int {
					return jobCountForPromisePipeline(suspendPromiseName, "pipe-2")
				}).Should(Equal(pipe2Count + 1))

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-1"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("pipe-2"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, messageJSONPath("pipe-1"))).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, `-o=jsonpath={.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].status}`)).To(Equal("True"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, `-o=jsonpath={.status.kratix.workflows.suspendedGeneration}`)).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, "-o", "yaml")).NotTo(ContainSubstring("kratix.io/workflow-suspended"))
				}).Should(Succeed())
			})

			By("suspending the resource pipeline", func() {
				platform.Kubectl("apply", "-f", suspendResource)

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, phaseJSONPath("resource-pipe-0"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, messageJSONPath("resource-pipe-0"))).To(Equal("waiting for configmap"))
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())

				Consistently(func() int {
					return workCountForResourcePipeline()
				}, 5*time.Second).Should(Equal(0))
			})

			By("unsuspending the pipeline through the label", func() {
				resourceJobCountBefore := jobCountForResourcePipeline(suspendPromiseName, "resource-pipe-0")

				platform.Kubectl("apply", "-f", suspendConfigMap)
				platform.Kubectl("label", suspendCRDPlural, suspendResourceName, "kratix.io/workflow-suspended-")

				Eventually(func() int {
					return jobCountForResourcePipeline(suspendPromiseName, "resource-pipe-0")
				}).Should(Equal(resourceJobCountBefore + 1))
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, phaseJSONPath("resource-pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, messageJSONPath("resource-pipe-0"))).To(BeEmpty())
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, `-o=jsonpath={.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].status}`)).To(Equal("True"))
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, `-o=jsonpath={.status.kratix.workflows.suspendedGeneration}`)).To(BeEmpty())
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(BeEmpty())
				}).Should(Succeed())

				Eventually(func() int {
					return workCountForResourcePipeline()
				}).Should(Equal(1))
			})

			By("suspending the resource delete pipeline when the resource is deleted", func() {
				platform.Kubectl("delete", "-f", suspendResource, "--wait=false")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, phaseJSONPath("resource-delete-pipe"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, messageJSONPath("resource-delete-pipe"))).To(Equal("waiting for delete approval"))
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())

				By("setting the DeleteWorkflowCompleted condition to reflect the wait is on delete", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].status}`)).To(Equal("False"))
						g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].reason}`)).To(Equal("DeleteWorkflowSuspended"))
					}).Should(Succeed())
				})

				By("preserving Works while the delete pipeline is suspended", func() {
					Consistently(func() int {
						return workCountForResourcePipeline()
					}, 5*time.Second).Should(Equal(1))
				})
			})

			By("resuming resource deletion after the gate is removed and suspend label cleared", func() {
				platform.Kubectl("delete", "-f", suspendConfigMap)
				platform.Kubectl("label", suspendCRDPlural, suspendResourceName, "kratix.io/workflow-suspended-")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, "--ignore-not-found")).To(BeEmpty())
				}).Should(Succeed())
			})

			By("suspending the promise delete pipeline when the promise is deleted", func() {
				platform.Kubectl("create", "cm", suspendPromiseDeleteGate, "-n", "kratix-platform-system")
				platform.Kubectl("delete", "promise", suspendPromiseName, "--wait=false")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, phaseJSONPath("promise-delete-pipe"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, messageJSONPath("promise-delete-pipe"))).To(Equal("waiting for promise delete approval"))
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())

				Consistently(func() int {
					return jobCountForWorkflow("promise", suspendPromiseName, "promise-delete-pipe", "delete")
				}, 10*time.Second).Should(Equal(1))

				By("setting the DeleteWorkflowCompleted condition to reflect the wait is on delete", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.Kubectl("get", "promise", suspendPromiseName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].status}`)).To(Equal("False"))
						g.Expect(platform.Kubectl("get", "promise", suspendPromiseName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].reason}`)).To(Equal("DeleteWorkflowSuspended"))
					}).Should(Succeed())
				})
			})

			By("resuming promise deletion after the gate is removed and suspend label cleared", func() {
				platform.Kubectl("delete", "cm", suspendPromiseDeleteGate, "-n", "kratix-platform-system")
				platform.Kubectl("label", "promise", suspendPromiseName, "kratix.io/workflow-suspended-")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, "--ignore-not-found")).To(BeEmpty())
				}).Should(Succeed())
			})
		})

	})

})

func phaseJSONPath(pipelineName string) string {
	return fmt.Sprintf(`-o=jsonpath={.status.kratix.workflows.pipelines[?(@.name=="%s")].phase}`, pipelineName)
}

func messageJSONPath(pipelineName string) string {
	return fmt.Sprintf(`-o=jsonpath={.status.kratix.workflows.pipelines[?(@.name=="%s")].message}`, pipelineName)
}

func nextRetryAtJSONPath(pipelineName string) string {
	return fmt.Sprintf(`-o=jsonpath={.status.kratix.workflows.pipelines[?(@.name=="%s")].nextRetryAt}`, pipelineName)
}

func attemptsJSONPath(pipelineName string) string {
	return fmt.Sprintf(`-o=jsonpath={.status.kratix.workflows.pipelines[?(@.name=="%s")].attempts}`, pipelineName)
}

func jobCountForPromisePipeline(promiseName, pipelineName string) int {
	return jobCountForWorkflow("promise", promiseName, pipelineName, "configure")
}

func jobCountForResourcePipeline(promiseName, pipelineName string) int {
	return jobCountForWorkflow("resource", promiseName, pipelineName, "configure")
}

func jobCountForWorkflow(workflowType, promiseName, pipelineName, action string) int {
	ns := "kratix-platform-system"
	if workflowType == "resource" {
		ns = "default"
	}
	selector := strings.Join([]string{
		"kratix.io/promise-name=" + promiseName,
		"kratix.io/workflow-type=" + workflowType,
		"kratix.io/workflow-action=" + action,
		"kratix.io/pipeline-name=" + pipelineName,
	}, ",")
	output := platform.Kubectl(
		"get", "jobs",
		"-n", ns,
		"-l", selector,
		"-o=go-template={{len .items}}",
	)
	count, err := strconv.Atoi(strings.TrimSpace(output))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return count
}

func workCountForResourcePipeline() int {
	selector := strings.Join([]string{
		"kratix.io/promise-name=" + suspendPromiseName,
		"kratix.io/work-type=resource",
		"kratix.io/resource-name=" + suspendResourceName,
		"kratix.io/pipeline-name=resource-pipe-0",
	}, ",")
	output := platform.Kubectl(
		"get", "works",
		"-n", "default",
		"-l", selector,
		"-o=go-template={{len .items}}",
	)
	count, err := strconv.Atoi(strings.TrimSpace(output))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return count
}
