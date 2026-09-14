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

// The configure-time and delete-time behaviours are covered by separate specs
// with their own promise copies (…-del assets) so they can run on different
// ginkgo procs instead of as one long serial phase chain.
const (
	suspendPromiseName       = "workflow-suspend"
	suspendPromiseDeleteGate = "workflow-suspend-promise-delete-gate"

	suspendDelPromiseName       = "workflow-suspend-del"
	suspendDelResource          = "assets/workflow-control/resource-request-suspend-del.yaml"
	suspendDelResourceName      = "suspend-test-del"
	suspendDelCRDPlural         = "workflowsuspenddels"
	suspendDelConfigMap         = "assets/workflow-control/configmap-suspend-del.yaml"
	suspendDelPromiseDeleteGate = "workflow-suspend-del-promise-delete-gate"

	retryPromiseName  = "workflow-retry"
	retryResourceGate = "workflow-retry-resource-gate"
	retryDependentCM  = "workflow-retry-test"

	retryDelPromiseName        = "wf-retry-del"
	retryDelResourceDeleteGate = "workflow-retry-del-delete-gate"
	retryDelPromiseDeleteGate  = "workflow-retry-del-promise-delete-gate"
)

var _ = Describe("Workflow Control", func() {
	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)
	})

	When("the file has 'retryAfter' set", func() {
		BeforeEach(func() {
			// clear all retry gates in case a previous run was interrupted
			platform.EventuallyKubectlDelete("cm", retryDependentCM, "-n", "kratix-platform-system", "--ignore-not-found")
			platform.Kubectl("delete", "cm", retryResourceGate, "-n", "default", "--ignore-not-found")
		})

		AfterEach(func() {
			// open the gates so teardown is never blocked by a retrying pipeline
			platform.KubectlAllowFail("create", "cm", retryResourceGate, "-n", "default")
			platform.KubectlAllowFail("create", "cm", "workflow-retry-delete-gate", "-n", "default")
			platform.KubectlAllowFail("create", "cm", "workflow-retry-promise-delete-gate", "-n", "kratix-platform-system")
			platform.EventuallyKubectlDelete("cm", retryDependentCM, "-n", "kratix-platform-system", "--ignore-not-found")
			platform.EventuallyKubectlDelete("promise", retryPromiseName, "--ignore-not-found")
			platform.Kubectl("delete", "cm", retryResourceGate, "-n", "default", "--ignore-not-found")
			platform.Kubectl("delete", "cm", "workflow-retry-delete-gate", "-n", "default", "--ignore-not-found")
			platform.Kubectl("delete", "cm", "workflow-retry-promise-delete-gate", "-n", "kratix-platform-system", "--ignore-not-found")
		})

		It("retries the configure pipelines after the interval", func() {
			By("retrying the Promise pipeline after the interval", func() {
				platform.Kubectl("apply", "-f", "assets/workflow-control/promise-retry.yaml")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-0"))).To(Equal("Succeeded"))
					state := pipelineState("promise", retryPromiseName, "pipe-retry")
					g.Expect(state.phase).To(Equal("Suspended"))
					g.Expect(state.message).To(Equal("configmap workflow-retry-test not found"))
					g.Expect(state.nextRetryAt).NotTo(BeEmpty())
					expectAttemptsAtLeast(g, state, 1)
					g.Expect(state.suspendedLabel).To(Equal("true"))
				}).Should(Succeed())
				Expect(jobCountForPromisePipeline(retryPromiseName, "pipe-2")).To(Equal(0))

				Eventually(func() int {
					return jobCountForPromisePipeline(retryPromiseName, "pipe-retry")
				}).Should(BeNumerically(">=", 2))

				Eventually(func(g Gomega) {
					state := pipelineState("promise", retryPromiseName, "pipe-retry")
					g.Expect(state.phase).To(Equal("Suspended"))
					expectAttemptsAtLeast(g, state, 2)
				}).Should(Succeed())
			})

			By("not retrying if retryAfter is not configured", func() {
				platform.Kubectl("create", "-n", "kratix-platform-system", "cm", retryDependentCM)

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
					g.Expect(jobCountForResourcePipeline(retryPromiseName, "resource-pipe-retry")).To(BeNumerically(">=", 1))
					state := pipelineState("workflowretries", "retry-test", "resource-pipe-retry")
					g.Expect(state.phase).To(Equal("Suspended"))
					g.Expect(state.nextRetryAt).NotTo(BeEmpty())
					expectAttemptsAtLeast(g, state, 1)
				}).Should(Succeed())

				Eventually(func(g Gomega) {
					g.Expect(jobCountForResourcePipeline(retryPromiseName, "resource-pipe-retry")).To(BeNumerically(">=", 2))
					state := pipelineState("workflowretries", "retry-test", "resource-pipe-retry")
					expectAttemptsAtLeast(g, state, 2)
				}).Should(Succeed())
			})

			By("completing the resource configure once its gate configmap exists", func() {
				platform.Kubectl("create", "cm", retryResourceGate, "-n", "default")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", phaseJSONPath("resource-pipe-retry"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "workflowretries", "retry-test", `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(BeEmpty())
				}).Should(Succeed())
			})
		})
	})

	When("the delete pipelines have 'retryAfter' set", func() {
		BeforeEach(func() {
			// clear any leftover delete gates
			platform.Kubectl("delete", "cm", retryDelResourceDeleteGate, "-n", "default", "--ignore-not-found")
			platform.Kubectl("delete", "cm", retryDelPromiseDeleteGate, "-n", "kratix-platform-system", "--ignore-not-found")

			platform.Kubectl("apply", "-f", "assets/workflow-control/promise-retry-del.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "promise", retryDelPromiseName)
			}).Should(ContainSubstring("Available"))

			platform.Kubectl("apply", "-f", "assets/workflow-control/resource-request-retry-del.yaml")
			Eventually(func(g Gomega) {
				g.Expect(platform.Kubectl("get", "workflowretrydels", "retry-test-del", phaseJSONPath("resource-pipe-retry"))).To(Equal("Succeeded"))
			}).Should(Succeed())
		})

		AfterEach(func() {
			platform.KubectlAllowFail("create", "cm", retryDelResourceDeleteGate, "-n", "default")
			platform.KubectlAllowFail("create", "cm", retryDelPromiseDeleteGate, "-n", "kratix-platform-system")
			platform.EventuallyKubectlDelete("promise", retryDelPromiseName, "--ignore-not-found")
			platform.Kubectl("delete", "cm", retryDelResourceDeleteGate, "-n", "default", "--ignore-not-found")
			platform.Kubectl("delete", "cm", retryDelPromiseDeleteGate, "-n", "kratix-platform-system", "--ignore-not-found")
		})

		It("retries the delete pipelines after the interval", func() {
			By("retrying the resource delete pipeline after the interval", func() {
				platform.Kubectl("delete", "-f", "assets/workflow-control/resource-request-retry-del.yaml", "--wait=false")

				Eventually(func(g Gomega) {
					g.Expect(jobCountForWorkflow("resource", retryDelPromiseName, "resource-delete-retry-pipe", "delete")).To(BeNumerically(">=", 1))
					state := pipelineState("workflowretrydels", "retry-test-del", "resource-delete-retry-pipe")
					g.Expect(state.phase).To(Equal("Suspended"))
					g.Expect(state.message).To(Equal("waiting for delete gate configmap"))
					g.Expect(state.nextRetryAt).NotTo(BeEmpty())
					expectAttemptsAtLeast(g, state, 1)
				}).Should(Succeed())

				By("reporting only the delete pipeline in the workflow status", func() {
					Expect(platform.Kubectl("get", "workflowretrydels", "retry-test-del",
						`-o=jsonpath={.status.kratix.workflows.delete.pipelines[*].name}`)).To(Equal("resource-delete-retry-pipe"))
				})

				By("setting the DeleteWorkflowCompleted condition while retrying", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.Kubectl("get", "workflowretrydels", "retry-test-del",
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].status}`)).To(Equal("False"))
						g.Expect(platform.Kubectl("get", "workflowretrydels", "retry-test-del",
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].reason}`)).To(Equal("DeleteWorkflowSuspended"))
					}).Should(Succeed())
				})

				By("keep on retrying", func() {
					Eventually(func(g Gomega) {
						g.Expect(jobCountForWorkflow("resource", retryDelPromiseName, "resource-delete-retry-pipe", "delete")).To(BeNumerically(">=", 2))
						state := pipelineState("workflowretrydels", "retry-test-del", "resource-delete-retry-pipe")
						expectAttemptsAtLeast(g, state, 2)
					}).Should(Succeed())
				})
			})

			By("completing deletion once the delete gate configmap exists", func() {
				platform.Kubectl("create", "cm", retryDelResourceDeleteGate, "-n", "default")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "workflowretrydels", "retry-test-del", "--ignore-not-found")).To(BeEmpty())
				}).Should(Succeed())
			})

			By("retrying the promise delete pipeline after the interval", func() {
				platform.Kubectl("delete", "promise", retryDelPromiseName, "--wait=false")

				Eventually(func(g Gomega) {
					g.Expect(jobCountForWorkflow("promise", retryDelPromiseName, "promise-delete-retry-pipe", "delete")).To(BeNumerically(">=", 1))
					state := pipelineState("promise", retryDelPromiseName, "promise-delete-retry-pipe")
					g.Expect(state.phase).To(Equal("Suspended"))
					g.Expect(state.message).To(Equal("waiting for promise delete gate configmap"))
					g.Expect(state.nextRetryAt).NotTo(BeEmpty())
					expectAttemptsAtLeast(g, state, 1)
				}).Should(Succeed())

				By("setting the DeleteWorkflowCompleted condition while retrying", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.Kubectl("get", "promise", retryDelPromiseName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].status}`)).To(Equal("False"))
						g.Expect(platform.Kubectl("get", "promise", retryDelPromiseName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].reason}`)).To(Equal("DeleteWorkflowSuspended"))
					}).Should(Succeed())
				})

				By("keep on retrying", func() {
					Eventually(func(g Gomega) {
						g.Expect(jobCountForWorkflow("promise", retryDelPromiseName, "promise-delete-retry-pipe", "delete")).To(BeNumerically(">=", 2))
						state := pipelineState("promise", retryDelPromiseName, "promise-delete-retry-pipe")
						expectAttemptsAtLeast(g, state, 2)
					}).Should(Succeed())
				})
			})

			By("completing promise deletion once its gate configmap exists", func() {
				platform.Kubectl("create", "cm", retryDelPromiseDeleteGate, "-n", "kratix-platform-system")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", retryDelPromiseName, "--ignore-not-found")).To(BeEmpty())
				}).Should(Succeed())
			})
		})
	})

	When("pipelines are suspended by the workflow control file", func() {
		BeforeEach(func() {
			// clear all suspend gates in case a previous run was interrupted
			platform.Kubectl("delete", "-f", "assets/workflow-control/configmap.yaml", "--ignore-not-found")
			platform.Kubectl("delete", "cm", suspendPromiseDeleteGate, "-n", "kratix-platform-system", "--ignore-not-found")
		})

		AfterEach(func() {
			platform.Kubectl("delete", "-f", "assets/workflow-control/configmap.yaml", "--ignore-not-found")
			platform.Kubectl("delete", "cm", suspendPromiseDeleteGate, "-n", "kratix-platform-system", "--ignore-not-found")
			platform.KubectlAllowFail("label", "promise", suspendPromiseName, "kratix.io/workflow-suspended-")
			platform.EventuallyKubectlDelete("promise", suspendPromiseName, "--ignore-not-found")
		})

		It("suspends and resumes the configure pipelines", func() {
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
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, `-o=jsonpath={.status.kratix.workflows.configure.suspendedGeneration}`)).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", suspendPromiseName, "-o", "yaml")).NotTo(ContainSubstring("kratix.io/workflow-suspended"))
				}).Should(Succeed())
			})
		})
	})

	When("resource and delete pipelines are suspended by the workflow control file", func() {
		BeforeEach(func() {
			// clear all suspend gates in case a previous run was interrupted
			platform.Kubectl("delete", "-f", suspendDelConfigMap, "--ignore-not-found")
			platform.Kubectl("delete", "cm", suspendDelPromiseDeleteGate, "-n", "kratix-platform-system", "--ignore-not-found")

			// This promise copy has no configure-time suspensions, so it installs clean.
			platform.Kubectl("apply", "-f", "assets/workflow-control/promise-suspend-del.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "promise", suspendDelPromiseName)
			}).Should(ContainSubstring("Available"))
		})

		AfterEach(func() {
			// remove all suspend gates and labels so teardown is never
			// blocked by a suspended delete pipeline
			platform.Kubectl("delete", "-f", suspendDelConfigMap, "--ignore-not-found")
			platform.Kubectl("delete", "cm", suspendDelPromiseDeleteGate, "-n", "kratix-platform-system", "--ignore-not-found")
			platform.KubectlAllowFail("label", suspendDelCRDPlural, suspendDelResourceName, "kratix.io/workflow-suspended-")
			platform.KubectlAllowFail("label", "promise", suspendDelPromiseName, "kratix.io/workflow-suspended-")
			platform.EventuallyKubectlDelete("promise", suspendDelPromiseName, "--ignore-not-found")
		})

		It("suspends and resumes the resource and delete pipelines", func() {
			By("suspending the resource pipeline", func() {
				platform.Kubectl("apply", "-f", suspendDelResource)

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, phaseJSONPath("resource-pipe-0"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, messageJSONPath("resource-pipe-0"))).To(Equal("waiting for configmap"))
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())

				Consistently(func() int {
					return workCountForResourcePipeline(suspendDelPromiseName, suspendDelResourceName)
				}, 5*time.Second).Should(Equal(0))
			})

			By("unsuspending the pipeline through the label", func() {
				resourceJobCountBefore := jobCountForResourcePipeline(suspendDelPromiseName, "resource-pipe-0")

				platform.Kubectl("apply", "-f", suspendDelConfigMap)
				platform.Kubectl("label", suspendDelCRDPlural, suspendDelResourceName, "kratix.io/workflow-suspended-")

				Eventually(func() int {
					return jobCountForResourcePipeline(suspendDelPromiseName, "resource-pipe-0")
				}).Should(Equal(resourceJobCountBefore + 1))
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, phaseJSONPath("resource-pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, messageJSONPath("resource-pipe-0"))).To(BeEmpty())
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, `-o=jsonpath={.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].status}`)).To(Equal("True"))
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, `-o=jsonpath={.status.kratix.workflows.configure.suspendedGeneration}`)).To(BeEmpty())
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(BeEmpty())
				}).Should(Succeed())

				Eventually(func() int {
					return workCountForResourcePipeline(suspendDelPromiseName, suspendDelResourceName)
				}).Should(Equal(1))
			})

			By("suspending the resource delete pipeline when the resource is deleted", func() {
				platform.Kubectl("delete", "-f", suspendDelResource, "--wait=false")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, phaseJSONPath("resource-delete-pipe"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, messageJSONPath("resource-delete-pipe"))).To(Equal("waiting for delete approval"))
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())

				By("setting the DeleteWorkflowCompleted condition to reflect the wait is on delete", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].status}`)).To(Equal("False"))
						g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].reason}`)).To(Equal("DeleteWorkflowSuspended"))
					}).Should(Succeed())
				})

				By("preserving Works while the delete pipeline is suspended", func() {
					Consistently(func() int {
						return workCountForResourcePipeline(suspendDelPromiseName, suspendDelResourceName)
					}, 5*time.Second).Should(Equal(1))
				})
			})

			By("resuming resource deletion after the gate is removed and suspend label cleared", func() {
				platform.Kubectl("delete", "-f", suspendDelConfigMap)
				platform.Kubectl("label", suspendDelCRDPlural, suspendDelResourceName, "kratix.io/workflow-suspended-")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", suspendDelCRDPlural, suspendDelResourceName, "--ignore-not-found")).To(BeEmpty())
				}).Should(Succeed())
			})

			By("suspending the promise delete pipeline when the promise is deleted", func() {
				platform.Kubectl("create", "cm", suspendDelPromiseDeleteGate, "-n", "kratix-platform-system")
				platform.Kubectl("delete", "promise", suspendDelPromiseName, "--wait=false")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", suspendDelPromiseName, phaseJSONPath("promise-delete-pipe"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", suspendDelPromiseName, messageJSONPath("promise-delete-pipe"))).To(Equal("waiting for promise delete approval"))
					g.Expect(platform.Kubectl("get", "promise", suspendDelPromiseName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())

				Consistently(func() int {
					return jobCountForWorkflow("promise", suspendDelPromiseName, "promise-delete-pipe", "delete")
				}, 10*time.Second).Should(Equal(1))

				By("setting the DeleteWorkflowCompleted condition to reflect the wait is on delete", func() {
					Eventually(func(g Gomega) {
						g.Expect(platform.Kubectl("get", "promise", suspendDelPromiseName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].status}`)).To(Equal("False"))
						g.Expect(platform.Kubectl("get", "promise", suspendDelPromiseName,
							`-o=jsonpath={.status.conditions[?(@.type=="DeleteWorkflowCompleted")].reason}`)).To(Equal("DeleteWorkflowSuspended"))
					}).Should(Succeed())
				})
			})

			By("resuming promise deletion after the gate is removed and suspend label cleared", func() {
				platform.Kubectl("delete", "cm", suspendDelPromiseDeleteGate, "-n", "kratix-platform-system")
				platform.Kubectl("label", "promise", suspendDelPromiseName, "kratix.io/workflow-suspended-")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", suspendDelPromiseName, "--ignore-not-found")).To(BeEmpty())
				}).Should(Succeed())
			})
		})
	})
})

func phaseJSONPath(pipelineName string) string {
	return fmt.Sprintf(`-o=jsonpath={.status.kratix.workflows.*.pipelines[?(@.name=="%s")].phase}`, pipelineName)
}

func messageJSONPath(pipelineName string) string {
	return fmt.Sprintf(`-o=jsonpath={.status.kratix.workflows.*.pipelines[?(@.name=="%s")].message}`, pipelineName)
}

func nextRetryAtJSONPath(pipelineName string) string {
	return fmt.Sprintf(`-o=jsonpath={.status.kratix.workflows.*.pipelines[?(@.name=="%s")].nextRetryAt}`, pipelineName)
}

func attemptsJSONPath(pipelineName string) string {
	return fmt.Sprintf(`-o=jsonpath={.status.kratix.workflows.*.pipelines[?(@.name=="%s")].attempts}`, pipelineName)
}

type workflowPipelineState struct {
	suspendedLabel string
	phase          string
	message        string
	nextRetryAt    string
	attempts       string
}

func pipelineState(kind, name, pipelineName string) workflowPipelineState {
	jsonpath := fmt.Sprintf(
		`-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`+
			`{"|"}{range .status.kratix.workflows.*.pipelines[?(@.name=="%s")]}{.phase}{"|"}{.message}{"|"}{.nextRetryAt}{"|"}{.attempts}{end}`,
		pipelineName)
	parts := strings.Split(strings.TrimSpace(platform.Kubectl("get", kind, name, jsonpath)), "|")
	for len(parts) < 5 {
		parts = append(parts, "")
	}
	return workflowPipelineState{
		suspendedLabel: parts[0],
		phase:          parts[1],
		message:        parts[2],
		nextRetryAt:    parts[3],
		attempts:       parts[4],
	}
}

func expectAttemptsAtLeast(g Gomega, state workflowPipelineState, n int) {
	attempts, err := strconv.Atoi(state.attempts)
	g.Expect(err).NotTo(HaveOccurred(), "attempts %q is not a number", state.attempts)
	g.Expect(attempts).To(BeNumerically(">=", n))
}

func jobCountForPromisePipeline(promiseName, pipelineName string) int {
	return jobCountForWorkflow("promise", promiseName, pipelineName, "configure")
}

func jobCountForResourcePipeline(promiseName, pipelineName string) int {
	return jobCountForWorkflow("resource", promiseName, pipelineName, "configure")
}

func jobCountForWorkflow(workflowType, promiseName, pipelineName, action string) int {
	output := platform.Kubectl(
		"get", "jobs",
		"-n", workflowJobNamespace(workflowType),
		"-l", workflowJobSelector(workflowType, promiseName, pipelineName, action),
		"-o=go-template={{len .items}}",
	)
	count, err := strconv.Atoi(strings.TrimSpace(output))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return count
}

// jobNamesForResourcePipeline is the pruning-resilient alternative to counting
// jobs: numberOfJobsToKeep caps the count, and a failed run now prunes too, so a
// re-run is only reliably visible as a job name that was not there before.
func jobNamesForResourcePipeline(promiseName, pipelineName string) []string {
	output := platform.Kubectl(
		"get", "jobs",
		"-n", workflowJobNamespace("resource"),
		"-l", workflowJobSelector("resource", promiseName, pipelineName, "configure"),
		"-o=jsonpath={.items[*].metadata.name}",
	)
	return strings.Fields(output)
}

func jobNamesForPromisePipeline(promiseName, pipelineName string) []string {
	output := platform.Kubectl(
		"get", "jobs",
		"-n", workflowJobNamespace("promise"),
		"-l", workflowJobSelector("promise", promiseName, pipelineName, "configure"),
		"-o=jsonpath={.items[*].metadata.name}",
	)
	return strings.Fields(output)
}

// newJobNames returns the names in current that are absent from previous.
func newJobNames(previous, current []string) []string {
	seen := map[string]bool{}
	for _, name := range previous {
		seen[name] = true
	}
	var added []string
	for _, name := range current {
		if !seen[name] {
			added = append(added, name)
		}
	}
	return added
}

func workflowJobNamespace(workflowType string) string {
	if workflowType == "resource" {
		return "default"
	}
	return "kratix-platform-system"
}

func workflowJobSelector(workflowType, promiseName, pipelineName, action string) string {
	return strings.Join([]string{
		"kratix.io/promise-name=" + promiseName,
		"kratix.io/workflow-type=" + workflowType,
		"kratix.io/workflow-action=" + action,
		"kratix.io/pipeline-name=" + pipelineName,
	}, ",")
}

func workCountForResourcePipeline(promiseName, resourceName string) int {
	selector := strings.Join([]string{
		"kratix.io/promise-name=" + promiseName,
		"kratix.io/work-type=resource",
		"kratix.io/resource-name=" + resourceName,
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
