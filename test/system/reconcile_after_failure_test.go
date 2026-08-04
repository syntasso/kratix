package system_test

import (
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("Reconcile after failure", Serial, func() {
	const (
		assetsPath    = "assets/reconcile-after-failure"
		promiseName   = "reconcilable"
		promiseKind   = "reconcilables"
		rrName        = "example"
		gateConfigMap = "reconcile-after-failure-gate"
		// Must match numberOfJobsToKeep in kratix-config-retry.yaml
		numberOfJobsToKeep = 2
	)

	workflowStatusJSONPath := `-o=jsonpath='{.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].status}'`
	workflowTransitionJSONPath := `-o=jsonpath='{.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].lastTransitionTime}'`

	configureJobCount := func() int {
		return jobCountForResourcePipeline(promiseName, "resource-configure")
	}

	configureJobNames := func() []string {
		return jobNamesForResourcePipeline(promiseName, "resource-configure")
	}

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)

		platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "promise.yaml"))
		Eventually(func() string {
			return platform.Kubectl("get", "promise", promiseName)
		}).Should(ContainSubstring("Available"))

		// Wait for any Jobs from a previous spec to be garbage-collected so the
		// configure-Job count starts from a clean, reliable baseline.
		Eventually(configureJobCount).Should(Equal(0))
	})

	AfterEach(func() {
		platform.EventuallyKubectlDelete(promiseKind, rrName)
		platform.EventuallyKubectlDelete("promise", promiseName)
		platform.KubectlAllowFail("delete", "configmap", gateConfigMap, "-n", "default")

		platform.Kubectl("apply", "-f", kratixConfigPath)
		restartController()
	})

	When("reconcileAfterFailure is true", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "kratix-config-retry.yaml"))
			restartController()
		})

		It("re-runs failed workflows on the schedule and resumes to success", func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "resource-request.yaml"))

			var jobsAtFirstFailure []string
			By("failing the configure workflow", func() {
				Eventually(func(g Gomega) {
					g.Expect(configureJobCount()).To(BeNumerically(">=", 1))
					g.Expect(platform.Kubectl("get", "--namespace=default", promiseKind, rrName, workflowStatusJSONPath)).
						To(ContainSubstring("False"))
				}).Should(Succeed())
				jobsAtFirstFailure = configureJobNames()
			})

			By("re-running the workflow automatically", func() {
				// Job names, not the count: failed jobs are now pruned to
				// numberOfJobsToKeep, so the count stops growing once it caps.
				Eventually(func(g Gomega) {
					g.Expect(newJobNames(jobsAtFirstFailure, configureJobNames())).NotTo(BeEmpty())
				}).Should(Succeed())
			})

			By("pruning the failed jobs it re-runs", func() {
				// A retry creates the next job before the previous failure is observed
				// and pruned, so the steady state is numberOfJobsToKeep plus the run in
				// flight. Without pruning on the failure path the count would climb
				// past that within a few reconciliation intervals.
				Consistently(func(g Gomega) {
					g.Expect(configureJobCount()).To(BeNumerically("<=", numberOfJobsToKeep+1))
				}, 30*time.Second, 2*time.Second).Should(Succeed())
			})

			By("succeeding once the gate exists", func() {
				platform.Kubectl("create", "configmap", gateConfigMap, "-n", "default")
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "--namespace=default", promiseKind, rrName, workflowStatusJSONPath)).
						To(ContainSubstring("True"))
				}).Should(Succeed())
			})

			By("continuing to reconcile after success", func() {
				// Job count is unreliable here: numberOfJobsToKeep prunes on the success
				// path, pinning the count. A success re-run flips the condition
				// True->InProgress->True, so lastTransitionTime advances each cycle.
				transitionBeforeReRun := platform.Kubectl("get", "--namespace=default", promiseKind, rrName, workflowTransitionJSONPath)
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "--namespace=default", promiseKind, rrName, workflowTransitionJSONPath)).
						NotTo(Equal(transitionBeforeReRun))
				}).Should(Succeed())
			})
		})
	})

	When("reconcileAfterFailure is false", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "kratix-config-no-retry.yaml"))
			restartController()
		})

		It("does not re-run failed workflows, but manual reconciliation works", func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "resource-request.yaml"))

			var failedCount int
			By("failing the configure workflow", func() {
				Eventually(func(g Gomega) {
					g.Expect(configureJobCount()).To(BeNumerically(">=", 1))
					g.Expect(platform.Kubectl("get", "--namespace=default", promiseKind, rrName, workflowStatusJSONPath)).
						To(ContainSubstring("False"))
				}).Should(Succeed())
				failedCount = configureJobCount()
			})

			By("not re-running on the schedule", func() {
				Consistently(func(g Gomega) {
					g.Expect(configureJobCount()).To(Equal(failedCount))
				}, 30*time.Second, 3*time.Second).Should(Succeed())
			})

			By("re-running when manually labelled", func() {
				platform.Kubectl("label", "--overwrite", "--namespace=default", promiseKind, rrName,
					"kratix.io/manual-reconciliation=true")
				Eventually(func(g Gomega) {
					g.Expect(configureJobCount()).To(BeNumerically(">", failedCount))
				}).Should(Succeed())
			})
		})
	})
})
