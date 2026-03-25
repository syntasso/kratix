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
	suspendPromiseName  = "workflow-suspend"
	suspendResource     = "assets/workflow-control/resource-request.yaml"
	suspendResourceName = "suspend-test"
	suspendCRDPlural    = "workflowsuspends"
	suspendConfigMap    = "assets/workflow-control/configmap.yaml"

	retryPromiseName = "workflow-retry"
)

var _ = Describe("Workflow Control", Ordered, func() {

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)
		platform.Kubectl("delete", "-f", suspendConfigMap, "--ignore-not-found")
	})

	When("the file has 'retryAfter' set", func() {
		dependentCM := "workflow-retry-test"

		AfterEach(func() {
			platform.EventuallyKubectlDelete("-n", "default", "cm", dependentCM, "--ignore-not-found")
			platform.EventuallyKubectlDelete("promise", retryPromiseName, "--ignore-not-found")
		})

		It("works", func() {
			By("retrying the pipeline after the interval", func() {
				platform.Kubectl("apply", "-f", "assets/workflow-control/promise-retry.yaml")

				pipeRetryCount := jobCountForPromisePipeline(retryPromiseName, "pipe-retry")

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-retry"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, messageJSONPath("pipe-retry"))).To(Equal("from-system-test-retrying"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, nextRetryAtJSONPath("pipe-retry"))).NotTo(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, attemptsJSONPath())).To(Equal("1"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(Equal("true"))
				}).Should(Succeed())

				Consistently(func() int {
					return jobCountForPromisePipeline(retryPromiseName, "pipe-retry")
				}, 10*time.Second).Should(Equal(pipeRetryCount))

				Eventually(func() int {
					return jobCountForPromisePipeline(retryPromiseName, "pipe-retry")
				}).Should(Equal(pipeRetryCount + 1))

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-retry"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, attemptsJSONPath())).To(Equal("2"))
				}).Should(Succeed())

				Eventually(func() int {
					return jobCountForPromisePipeline(retryPromiseName, "pipe-retry")
				}).Should(Equal(pipeRetryCount + 2))

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-retry"))).To(Equal("Suspended"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, attemptsJSONPath())).To(Equal("3"))
				}).Should(Succeed())
			})

			By("not retrying if retryAfter is not configured", func() {
				platform.Kubectl("create", "-n", "default", "cm", dependentCM)

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, phaseJSONPath("pipe-retry"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, nextRetryAtJSONPath("pipe-retry"))).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, attemptsJSONPath())).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "promise", retryPromiseName)).To(ContainSubstring("Reconciled"))
				}).Should(Succeed())
			})
		})
	})

	When("pipelines are suspended by the workflow control file", func() {
		AfterEach(func() {
			platform.Kubectl("delete", "-f", suspendConfigMap, "--ignore-not-found")
			platform.Kubectl("delete", "-f", suspendResource, "--ignore-not-found")
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

func attemptsJSONPath() string {
	return fmt.Sprintf(`-o=jsonpath={.status.kratix.workflows.pipelines[?(@.name=="%s")].attempts}`, "pipe-retry")
}

func jobCountForPromisePipeline(promiseName, pipelineName string) int {
	return jobCountForWorkflow("promise", promiseName, pipelineName)
}

func jobCountForResourcePipeline(promiseName, pipelineName string) int {
	return jobCountForWorkflow("resource", promiseName, pipelineName)
}

func jobCountForWorkflow(workflowType, promiseName, pipelineName string) int {
	ns := "kratix-platform-system"
	if workflowType == "resource" {
		ns = "default"
	}
	selector := strings.Join([]string{
		"kratix.io/promise-name=" + promiseName,
		"kratix.io/workflow-type=" + workflowType,
		"kratix.io/workflow-action=configure",
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
