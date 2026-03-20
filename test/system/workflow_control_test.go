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
)

var _ = Describe("Workflow Control", Ordered, func() {

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)
		platform.Kubectl("delete", "-f", suspendConfigMap, "--ignore-not-found")
	})

	AfterEach(func() {
		platform.Kubectl("delete", "-f", suspendConfigMap, "--ignore-not-found")
		platform.Kubectl("delete", "-f", suspendResource, "--ignore-not-found")
		platform.EventuallyKubectlDelete("promise", suspendPromiseName, "--ignore-not-found")
	})

	When("pipelines are suspended by the workflow control file", func() {
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
					return jobCount("pipe-2")
				}, 10*time.Second).Should(Equal(0))
			})

			By("removing the suspend label and resuming from the suspended pipeline", func() {
				pipe0Count := jobCount("pipe-0")
				pipe1Count := jobCount("pipe-1")
				pipe2Count := jobCount("pipe-2")

				platform.Kubectl("label", "promise", suspendPromiseName, "kratix.io/workflow-suspended-")

				Eventually(func() int {
					return jobCount("pipe-1")
				}).Should(Equal(pipe1Count + 1))
				Consistently(func() int {
					return jobCount("pipe-0")
				}, 10*time.Second).Should(Equal(pipe0Count))
				Consistently(func() int {
					return jobCount("pipe-2")
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
				pipe0Count := jobCount("pipe-0")
				pipe1Count := jobCount("pipe-1")
				pipe2Count := jobCount("pipe-2")

				platform.Kubectl("label", "promise", suspendPromiseName, "kratix.io/manual-reconciliation=true", "--overwrite")

				Eventually(func() int {
					return jobCount("pipe-0")
				}).Should(Equal(pipe0Count + 1))
				Eventually(func() int {
					return jobCount("pipe-1")
				}).Should(Equal(pipe1Count + 1))
				Consistently(func() int {
					return jobCount("pipe-2")
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
				pipe0Count := jobCount("pipe-0")
				pipe1Count := jobCount("pipe-1")
				pipe2Count := jobCount("pipe-2")

				platform.Kubectl("apply", "-f", "assets/workflow-control/promise-updated.yaml")

				Eventually(func() int {
					return jobCount("pipe-0")
				}).Should(Equal(pipe0Count + 1))
				Eventually(func() int {
					return jobCount("pipe-1")
				}).Should(Equal(pipe1Count + 1))
				Eventually(func() int {
					return jobCount("pipe-2")
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
			})

			By("unsuspending the pipeline through the label", func() {
				resourceJobCountBefore := jobCountForWorkflow("resource", "resource-pipe-0")

				platform.Kubectl("apply", "-f", suspendConfigMap)
				platform.Kubectl("label", suspendCRDPlural, suspendResourceName, "kratix.io/workflow-suspended-")

				Eventually(func() int {
					return jobCountForWorkflow("resource", "resource-pipe-0")
				}).Should(Equal(resourceJobCountBefore + 1))

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, phaseJSONPath("resource-pipe-0"))).To(Equal("Succeeded"))
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, messageJSONPath("resource-pipe-0"))).To(BeEmpty())
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, `-o=jsonpath={.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].status}`)).To(Equal("True"))
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, `-o=jsonpath={.status.kratix.workflows.suspendedGeneration}`)).To(BeEmpty())
					g.Expect(platform.Kubectl("get", suspendCRDPlural, suspendResourceName, `-o=jsonpath={.metadata.labels.kratix\.io/workflow-suspended}`)).To(BeEmpty())
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

func jobCount(pipelineName string) int {
	return jobCountForWorkflow("promise", pipelineName)
}

func jobCountForWorkflow(workflowType, pipelineName string) int {
	ns := "kratix-platform-system"
	if workflowType == "resource" {
		ns = "default"
	}
	selector := strings.Join([]string{
		"kratix.io/promise-name=" + suspendPromiseName,
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
