package system_test

import (
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

const (
	rafAssetsPath = "assets/reconcile-after-failure"

	rafPromiseName   = "reconcilable"
	rafPromiseKind   = "reconcilables"
	rafRRName        = "example"
	rafResourceGate  = "reconcile-after-failure-gate"
	rafWFPromiseName = "reconcilable-promise-wf"
	rafWFGate        = "reconcile-after-failure-promise-gate"
	rafWFHold        = "reconcile-after-failure-promise-hold"
	rafGateNamespace = "kratix-platform-system"
	// Must match numberOfJobsToKeep in kratix-config-retry.yaml
	rafNumberOfJobsToKeep = 2
)

var (
	rafWorkflowStatusJSONPath     = `-o=jsonpath='{.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].status}'`
	rafWorkflowTransitionJSONPath = `-o=jsonpath='{.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].lastTransitionTime}'`
)

func rafResourceJobCount() int {
	return jobCountForResourcePipeline(rafPromiseName, "resource-configure")
}

func rafResourceJobNames() []string {
	return jobNamesForResourcePipeline(rafPromiseName, "resource-configure")
}

func rafWFJobCount() int {
	return jobCountForPromisePipeline(rafWFPromiseName, "promise-configure")
}

func rafWFJobNames() []string {
	return jobNamesForPromisePipeline(rafWFPromiseName, "promise-configure")
}

func rafSetTimeouts() {
	SetDefaultEventuallyTimeout(4 * time.Minute)
	SetDefaultEventuallyPollingInterval(2 * time.Second)
	kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)
}

// The specs are grouped by the Kratix config they need, so each config is
// applied (and the controller restarted) once per group rather than once per
// spec. The default config is not restored between groups — every group applies
// its own config in BeforeAll, and the suite restores the default once at the
// end (SynchronizedAfterSuite).
var _ = Describe("Reconcile after failure", Label("config-mutating"), Serial, Ordered, func() {
	BeforeAll(func() {
		rafSetTimeouts()
		platform.Kubectl("apply", "-f", filepath.Join(rafAssetsPath, "kratix-config-retry.yaml"))
		restartController()
	})

	BeforeEach(rafSetTimeouts)

	When("a resource workflow fails and reconcileAfterFailure uses its default value", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(rafAssetsPath, "promise.yaml"))
			Eventually(func() string {
				return platform.Kubectl("get", "promise", rafPromiseName)
			}).Should(ContainSubstring("Available"))

			// Wait for any Jobs from a previous spec to be garbage-collected so the
			// configure-Job count starts from a clean, reliable baseline.
			Eventually(rafResourceJobCount).Should(Equal(0))
		})

		AfterEach(func() {
			platform.EventuallyKubectlDelete(rafPromiseKind, rafRRName)
			platform.EventuallyKubectlDelete("promise", rafPromiseName)
			platform.KubectlAllowFail("delete", "configmap", rafResourceGate, "-n", "default")
		})

		It("re-runs failed workflows on the schedule and resumes to success", func() {
			platform.Kubectl("apply", "-f", filepath.Join(rafAssetsPath, "resource-request.yaml"))

			var jobsAtFirstFailure []string
			By("failing the configure workflow", func() {
				Eventually(func(g Gomega) {
					g.Expect(rafResourceJobCount()).To(BeNumerically(">=", 1))
					g.Expect(platform.Kubectl("get", "--namespace=default", rafPromiseKind, rafRRName, rafWorkflowStatusJSONPath)).
						To(ContainSubstring("False"))
				}).Should(Succeed())
				jobsAtFirstFailure = rafResourceJobNames()
			})

			By("re-running the workflow automatically", func() {
				// Job names, not the count: failed jobs are now pruned to
				// numberOfJobsToKeep, so the count stops growing once it caps.
				Eventually(func(g Gomega) {
					g.Expect(newJobNames(jobsAtFirstFailure, rafResourceJobNames())).NotTo(BeEmpty())
				}).Should(Succeed())
			})

			By("pruning the failed jobs it re-runs", func() {
				// A retry creates the next job before the previous failure is observed
				// and pruned, so the steady state is numberOfJobsToKeep plus the run in
				// flight. Without pruning on the failure path the count would climb
				// past that within a few reconciliation intervals.
				Consistently(func(g Gomega) {
					g.Expect(rafResourceJobCount()).To(BeNumerically("<=", rafNumberOfJobsToKeep+1))
				}, 30*time.Second, 2*time.Second).Should(Succeed())
			})

			By("succeeding once the gate exists", func() {
				platform.Kubectl("create", "configmap", rafResourceGate, "-n", "default")
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "--namespace=default", rafPromiseKind, rafRRName, rafWorkflowStatusJSONPath)).
						To(ContainSubstring("True"))
				}).Should(Succeed())
			})

			By("continuing to reconcile after success", func() {
				// Job count is unreliable here: numberOfJobsToKeep prunes on the success
				// path, pinning the count. A success re-run flips the condition
				// True->InProgress->True, so lastTransitionTime advances each cycle.
				transitionBeforeReRun := platform.Kubectl("get", "--namespace=default", rafPromiseKind, rafRRName, rafWorkflowTransitionJSONPath)
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "--namespace=default", rafPromiseKind, rafRRName, rafWorkflowTransitionJSONPath)).
						NotTo(Equal(transitionBeforeReRun))
				}).Should(Succeed())
			})
		})
	})

	When("a promise workflow fails and reconcileAfterFailure uses its default value", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(rafAssetsPath, "promise-workflow.yaml"))
		})

		AfterEach(func() {
			platform.EventuallyKubectlDelete("promise", rafWFPromiseName)
			platform.KubectlAllowFail("delete", "configmap", rafWFGate, "-n", rafGateNamespace)
			platform.KubectlAllowFail("delete", "configmap", rafWFHold, "-n", rafGateNamespace)
		})

		It("continuously reconciles failed, in-progress, and successful promise workflows", func() {
			var jobsAtFirstFailure []string
			By("failing the promise configure workflow", func() {
				Eventually(func(g Gomega) {
					g.Expect(rafWFJobCount()).To(BeNumerically(">=", 1))
					g.Expect(platform.Kubectl("get", "promise", rafWFPromiseName, rafWorkflowStatusJSONPath)).
						To(ContainSubstring("False"))
				}).Should(Succeed())
				jobsAtFirstFailure = rafWFJobNames()
			})

			By("re-running the workflow automatically", func() {
				// Hold the scheduled retry open across another reconciliation interval.
				// This makes an accidental restart or suspension observable.
				platform.Kubectl("create", "configmap", rafWFHold, "-n", rafGateNamespace)

				var heldJobName string
				Eventually(func(g Gomega) {
					newJobs := newJobNames(jobsAtFirstFailure, rafWFJobNames())
					g.Expect(newJobs).NotTo(BeEmpty())
					heldJobName = newJobs[0]
					g.Expect(platform.Kubectl("get", "job", heldJobName, "-n", rafGateNamespace,
						`-o=jsonpath={.status.active}`)).To(Equal("1"))
				}).Should(Succeed())

				jobsWhileInProgress := rafWFJobNames()
				Consistently(func(g Gomega) {
					g.Expect(newJobNames(jobsWhileInProgress, rafWFJobNames())).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "job", heldJobName, "-n", rafGateNamespace,
						`-o=jsonpath={.status.active}`)).To(Equal("1"))
					g.Expect(platform.Kubectl("get", "job", heldJobName, "-n", rafGateNamespace,
						`-o=jsonpath={.spec.suspend}`)).NotTo(Equal("true"))
				}, 10*time.Second, 2*time.Second).Should(Succeed())

				platform.Kubectl("delete", "configmap", rafWFHold, "-n", rafGateNamespace)
			})

			By("pruning failed jobs while retries continue", func() {
				observedJobNames := map[string]bool{}
				Consistently(func(g Gomega) {
					currentJobNames := rafWFJobNames()
					for _, name := range currentJobNames {
						observedJobNames[name] = true
					}
					// A retry creates the next job before the preceding failure is
					// observed and pruned, so one transient extra job is expected.
					g.Expect(len(currentJobNames)).To(BeNumerically("<=", rafNumberOfJobsToKeep+1))
				}, 75*time.Second, 2*time.Second).Should(Succeed())
				Expect(len(observedJobNames)).To(BeNumerically(">", rafNumberOfJobsToKeep+1))
			})

			By("succeeding once the gate exists", func() {
				platform.Kubectl("create", "configmap", rafWFGate, "-n", rafGateNamespace)
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", rafWFPromiseName, rafWorkflowStatusJSONPath)).
						To(ContainSubstring("True"))
				}).Should(Succeed())
			})

			By("continuing to reconcile after success", func() {
				jobsAtSuccess := rafWFJobNames()
				Eventually(func(g Gomega) {
					g.Expect(newJobNames(jobsAtSuccess, rafWFJobNames())).NotTo(BeEmpty())
				}).Should(Succeed())
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", rafWFPromiseName, rafWorkflowStatusJSONPath)).
						To(ContainSubstring("True"))
				}).Should(Succeed())
			})
		})
	})
})

var _ = Describe("Reconcile after failure disabled", Label("config-mutating"), Serial, Ordered, func() {
	BeforeAll(func() {
		rafSetTimeouts()
		platform.Kubectl("apply", "-f", filepath.Join(rafAssetsPath, "kratix-config-no-retry.yaml"))
		restartController()
	})

	BeforeEach(rafSetTimeouts)

	When("a resource workflow fails and reconcileAfterFailure is false", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(rafAssetsPath, "promise.yaml"))
			Eventually(func() string {
				return platform.Kubectl("get", "promise", rafPromiseName)
			}).Should(ContainSubstring("Available"))
			Eventually(rafResourceJobCount).Should(Equal(0))
		})

		AfterEach(func() {
			platform.EventuallyKubectlDelete(rafPromiseKind, rafRRName)
			platform.EventuallyKubectlDelete("promise", rafPromiseName)
			platform.KubectlAllowFail("delete", "configmap", rafResourceGate, "-n", "default")
		})

		It("does not re-run failed workflows, but manual reconciliation works", func() {
			platform.Kubectl("apply", "-f", filepath.Join(rafAssetsPath, "resource-request.yaml"))

			var failedCount int
			By("failing the configure workflow", func() {
				Eventually(func(g Gomega) {
					g.Expect(rafResourceJobCount()).To(BeNumerically(">=", 1))
					g.Expect(platform.Kubectl("get", "--namespace=default", rafPromiseKind, rafRRName, rafWorkflowStatusJSONPath)).
						To(ContainSubstring("False"))
				}).Should(Succeed())
				failedCount = rafResourceJobCount()
			})

			By("not re-running on the schedule", func() {
				Consistently(func(g Gomega) {
					g.Expect(rafResourceJobCount()).To(Equal(failedCount))
				}, 30*time.Second, 3*time.Second).Should(Succeed())
			})

			By("re-running when manually labelled", func() {
				platform.Kubectl("label", "--overwrite", "--namespace=default", rafPromiseKind, rafRRName,
					"kratix.io/manual-reconciliation=true")
				Eventually(func(g Gomega) {
					g.Expect(rafResourceJobCount()).To(BeNumerically(">", failedCount))
				}).Should(Succeed())
			})
		})
	})

	When("a promise workflow fails and reconcileAfterFailure is false", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(rafAssetsPath, "promise-workflow.yaml"))
		})

		AfterEach(func() {
			platform.EventuallyKubectlDelete("promise", rafWFPromiseName)
			platform.KubectlAllowFail("delete", "configmap", rafWFGate, "-n", rafGateNamespace)
			platform.KubectlAllowFail("delete", "configmap", rafWFHold, "-n", rafGateNamespace)
		})

		It("does not retry on the schedule, but label and spec changes still reconcile it", func() {
			var failedJobs []string
			By("failing the promise configure workflow", func() {
				Eventually(func(g Gomega) {
					g.Expect(rafWFJobCount()).To(BeNumerically(">=", 1))
					g.Expect(platform.Kubectl("get", "promise", rafWFPromiseName, rafWorkflowStatusJSONPath)).
						To(ContainSubstring("False"))
				}).Should(Succeed())
				failedJobs = rafWFJobNames()
			})

			By("not re-running on the schedule", func() {
				Consistently(func(g Gomega) {
					g.Expect(newJobNames(failedJobs, rafWFJobNames())).To(BeEmpty())
				}, 30*time.Second, 3*time.Second).Should(Succeed())
			})

			By("re-running when manually labelled", func() {
				platform.Kubectl("label", "--overwrite", "promise", rafWFPromiseName,
					"kratix.io/manual-reconciliation=true")
				var manuallyStartedJob string
				Eventually(func(g Gomega) {
					newJobs := newJobNames(failedJobs, rafWFJobNames())
					g.Expect(newJobs).NotTo(BeEmpty())
					manuallyStartedJob = newJobs[0]
					g.Expect(platform.Kubectl("get", "job", manuallyStartedJob, "-n", rafGateNamespace,
						`-o=jsonpath={.status.failed}`)).To(Equal("1"))
				}).Should(Succeed())
			})

			By("re-running after a Promise spec change", func() {
				jobsBeforeSpecChange := rafWFJobNames()
				platform.Kubectl("patch", "promise", rafWFPromiseName, "--type=merge", "-p",
					`{"spec":{"workflows":{"config":{"pipelineNamespace":"kratix-platform-system"}}}}`)
				Eventually(func(g Gomega) {
					g.Expect(newJobNames(jobsBeforeSpecChange, rafWFJobNames())).NotTo(BeEmpty())
				}).Should(Succeed())
			})
		})
	})
})
