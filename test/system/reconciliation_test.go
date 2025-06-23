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
			SetDefaultEventuallyTimeout(2 * time.Minute)
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

			nsFlag := "--namespace=reconciliation-test"
			Eventually(func() string {
				return worker.Kubectl("get", "configmap", nsFlag)
			}, 1*time.Minute).Should(ContainSubstring("one-before"))

			By("accepting create and update requests")
			platform.Kubectl("label", "promise", promiseName, "kratix.io/paused=true")
			platform.Kubectl("apply", "-f", "assets/reconciliation/rr-one-updated.yaml")
			platform.Kubectl("apply", "-f", "assets/reconciliation/rr-two.yaml")
			Eventually(func() string {
				return platform.KubectlAllowFail("get", promiseName, "two")
			}).Should(ContainSubstring("Paused"))

			By("rerunning promise workflows after label was removed")
			platform.Kubectl("label", "promise", promiseName, "kratix.io/paused-")

			Eventually(func() string {
				return platform.Kubectl("get", "promises", promiseName, workflowTimeStampJsonPath)
			}, 30*time.Second).ShouldNot(Equal(promiseWorkflowTimeStamp))

			By("resuming reconciliation for resource requests")
			Eventually(func() string {
				return worker.Kubectl("get", "configmap", nsFlag)
			}, 1*time.Minute).Should(SatisfyAll(
				ContainSubstring("two"),
				ContainSubstring("one-after")))
		})
	})
})
