package system_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("Reconciliation", func() {
	BeforeEach(func() {
		SetDefaultEventuallyTimeout(3 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)
	})

	When("a Promise is paused", func() {
		var promiseName = "pausedtest"
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", "assets/reconciliation/pause-promise.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "promise", promiseName)
			}).Should(ContainSubstring("Available"))
			platform.Kubectl("apply", "-f", "assets/reconciliation/pause-promise-rr-one.yaml")
		})

		AfterEach(func() {
			platform.KubectlAllowFail("label", "promise", promiseName, "kratix.io/paused-")
			platform.EventuallyKubectlDelete(promiseName, "one")
			platform.EventuallyKubectlDelete(promiseName, "two")
			platform.EventuallyKubectlDelete("promise", promiseName)
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
			platform.Kubectl("apply", "-f", "assets/reconciliation/pause-promise-rr-one-updated.yaml")
			platform.Kubectl("apply", "-f", "assets/reconciliation/pause-promise-rr-two.yaml")
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
			}, 30*time.Second).Should(ContainSubstring("3"))

			Eventually(func() string {
				return platform.Kubectl("get", promiseName, "one")
			}).Should(ContainSubstring("Reconciled"))
			Eventually(func() string {
				return platform.Kubectl("get", promiseName, "two")
			}).Should(ContainSubstring("Reconciled"))
		})
	})

	When("a Resource Request is paused", func() {
		var (
			promiseName         = "pausedtest-rr"
			resourceRequestName = "one"
			kindName            = "pausedtestrr"
		)

		BeforeEach(func() {
			platform.Kubectl("apply", "-f", "assets/reconciliation/pause-request-promise.yaml")
			Eventually(func() string {
				return platform.Kubectl("get", "promise", promiseName)
			}).Should(ContainSubstring("Available"))
			platform.Kubectl("apply", "-f", "assets/reconciliation/pause-request-promise-rr.yaml")
		})

		It("does not do reconciliation for paused resource request", func() {
			podLabels := fmt.Sprintf("kratix.io/promise-name=%s,kratix.io/workflow-type=resource", promiseName)
			goTemplate := `go-template='{{printf "%d\n" (len  .items)}}'`
			numberOfTriggeredPods := platform.Kubectl("get", "pods", "-l", podLabels, "-o", goTemplate)

			platform.Kubectl("label", kindName, resourceRequestName, "kratix.io/paused=true")
			platform.Kubectl("apply", "-f", "assets/reconciliation/pause-request-promise-rr-update.yaml")
			platform.Kubectl("apply", "-f", "assets/reconciliation/pause-request-promise-update.yaml")

			By("not running any workflow while paused")
			Consistently(func() string {
				return platform.Kubectl("get", "pods", "-l", podLabels, "-o", goTemplate)
			}, 10*time.Second).Should(Equal(numberOfTriggeredPods))

			platform.Kubectl("label", kindName, resourceRequestName, "kratix.io/paused-")

			By("resuming reconciliation for resource request after unpaused")
			numberOfTriggeredPodsPlusOne := "2"
			Eventually(func() string {
				return platform.Kubectl("get", "pods", "-l", podLabels, "-o", goTemplate)
			}, 30*time.Second).Should(ContainSubstring(numberOfTriggeredPodsPlusOne))

			Eventually(func() string {
				return platform.Kubectl("get", kindName, resourceRequestName)
			}).Should(ContainSubstring("Reconciled"))

			Eventually(func() string {
				return worker.Kubectl("get", "configmap", "one-after", "-n", "pausedtestrr", "-o=jsonpath='{.data.key1}'")
			}).Should(ContainSubstring("config1"))
		})

		AfterEach(func() {
			platform.KubectlAllowFail("label", kindName, resourceRequestName, "kratix.io/paused-")
			platform.EventuallyKubectlDelete(kindName, resourceRequestName)
			platform.EventuallyKubectlDelete("promise", promiseName)
		})

	})
})
