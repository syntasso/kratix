package system_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("Reconciliation", func() {
	When("a Promise is paused", func() {
		var promiseName = "pausedtest"
		BeforeEach(func() {
			SetDefaultEventuallyTimeout(3 * time.Minute)
			SetDefaultEventuallyPollingInterval(2 * time.Second)
			kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)

			platform.Kubectl("apply", "-f", "assets/reconciliation/promise.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "promise", promiseName)
			}).Should(ContainSubstring("Available"))
			platform.Kubectl("apply", "-f", "assets/reconciliation/rr-one.yaml")
		})

		AfterEach(func() {
			platform.Kubectl("delete", "promise", promiseName)
		})

		It("pauses reconciliation and resumes after label has been removed", func() {
			workflowTimeStampJsonPath := `-o=jsonpath='{.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].lastTransitionTime}'`
			promiseWorkflowTimeStamp := platform.Kubectl("get", "promises", promiseName, workflowTimeStampJsonPath)

			Eventually(func() string {
				return platform.Kubectl("get", promiseName, "one")
			}).Should(ContainSubstring("Reconciled"))

			podLabels := "kratix.io/promise-name=pausedtest,kratix.io/workflow-type=resource"
			goTemplate := `go-template='{{printf "%d\n" (len  .items)}}'`
			numberOfTriggeredPods := platform.Kubectl("get", "pods", "-l", podLabels, "-o", goTemplate)

			platform.Kubectl("label", "promise", promiseName, "kratix.io/paused=true")

			By("accepting create/update requests while paused")
			Eventually(func() string {
				return platform.Kubectl("get", "promises", promiseName)
			}).Should(ContainSubstring("Paused"))
			platform.Kubectl("apply", "-f", "assets/reconciliation/rr-one-updated.yaml")
			platform.Kubectl("apply", "-f", "assets/reconciliation/rr-two.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", promiseName, "two")
			}).Should(ContainSubstring("Paused"))
			Eventually(func() string {
				return platform.Kubectl("get", promiseName, "one")
			}).Should(ContainSubstring("Paused"))

			By("not running any workflow while paused")
			Consistently(func() string {
				return platform.Kubectl("get", "pods", "-l", podLabels, "-o", goTemplate)
			}, 10*time.Second).Should(Equal(numberOfTriggeredPods))

			By("rerunning promise workflows after unpaused")
			platform.Kubectl("label", "promise", promiseName, "kratix.io/paused-")

			Eventually(func() string {
				return platform.Kubectl("get", "promises", promiseName, workflowTimeStampJsonPath)
			}).ShouldNot(Equal(promiseWorkflowTimeStamp))

			By("resuming reconciliation for resource requests after unpaused")
			Eventually(func() string {
				return platform.Kubectl("get", "pods", "-l", podLabels, "-o", goTemplate)
			}, 10*time.Second).Should(ContainSubstring("3"))

			Eventually(func() string {
				return platform.Kubectl("get", promiseName, "one")
			}).Should(ContainSubstring("Reconciled"))
			Eventually(func() string {
				return platform.Kubectl("get", promiseName, "two")
			}).Should(ContainSubstring("Reconciled"))
		})
	})
})
