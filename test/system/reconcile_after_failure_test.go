package system_test

import (
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/test/kubeutils"
)

var _ = Describe("Reconcile after failure", Label("config-mutating"), Serial, func() {
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

	When("reconcileAfterFailure uses its default value", func() {
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

var _ = Describe("Reconcile promise workflow after failure", Label("config-mutating"), Serial, func() {
	const (
		assetsPath         = "assets/reconcile-after-failure"
		promiseName        = "reconcilable-promise-wf"
		gateConfigMap      = "reconcile-after-failure-promise-gate"
		holdConfigMap      = "reconcile-after-failure-promise-hold"
		gateNamespace      = "kratix-platform-system"
		numberOfJobsToKeep = 2
	)

	workflowStatusJSONPath := `-o=jsonpath='{.status.conditions[?(@.type=="ConfigureWorkflowCompleted")].status}'`

	configureJobCount := func() int {
		return jobCountForPromisePipeline(promiseName, "promise-configure")
	}
	configureJobNames := func() []string {
		return jobNamesForPromisePipeline(promiseName, "promise-configure")
	}

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(4 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(4*time.Minute, 2*time.Second)
	})

	AfterEach(func() {
		platform.EventuallyKubectlDelete("promise", promiseName)
		platform.KubectlAllowFail("delete", "configmap", gateConfigMap, "-n", gateNamespace)
		platform.KubectlAllowFail("delete", "configmap", holdConfigMap, "-n", gateNamespace)
		platform.Kubectl("apply", "-f", kratixConfigPath)
		restartController()
	})

	When("reconcileAfterFailure uses its default value", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "kratix-config-retry.yaml"))
			restartController()
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "promise-workflow.yaml"))
		})

		It("continuously reconciles failed, in-progress, and successful promise workflows", func() {
			var jobsAtFirstFailure []string
			By("failing the promise configure workflow", func() {
				Eventually(func(g Gomega) {
					g.Expect(configureJobCount()).To(BeNumerically(">=", 1))
					g.Expect(platform.Kubectl("get", "promise", promiseName, workflowStatusJSONPath)).
						To(ContainSubstring("False"))
				}).Should(Succeed())
				jobsAtFirstFailure = configureJobNames()
			})

			By("re-running the workflow automatically", func() {
				// Hold the scheduled retry open across another reconciliation interval.
				// This makes an accidental restart or suspension observable.
				platform.Kubectl("create", "configmap", holdConfigMap, "-n", gateNamespace)

				var heldJobName string
				Eventually(func(g Gomega) {
					newJobs := newJobNames(jobsAtFirstFailure, configureJobNames())
					g.Expect(newJobs).NotTo(BeEmpty())
					heldJobName = newJobs[0]
					g.Expect(platform.Kubectl("get", "job", heldJobName, "-n", gateNamespace,
						`-o=jsonpath={.status.active}`)).To(Equal("1"))
				}).Should(Succeed())

				jobsWhileInProgress := configureJobNames()
				Consistently(func(g Gomega) {
					g.Expect(newJobNames(jobsWhileInProgress, configureJobNames())).To(BeEmpty())
					g.Expect(platform.Kubectl("get", "job", heldJobName, "-n", gateNamespace,
						`-o=jsonpath={.status.active}`)).To(Equal("1"))
					g.Expect(platform.Kubectl("get", "job", heldJobName, "-n", gateNamespace,
						`-o=jsonpath={.spec.suspend}`)).NotTo(Equal("true"))
				}, 10*time.Second, 2*time.Second).Should(Succeed())

				platform.Kubectl("delete", "configmap", holdConfigMap, "-n", gateNamespace)
			})

			By("pruning failed jobs while retries continue", func() {
				observedJobNames := map[string]bool{}
				Consistently(func(g Gomega) {
					currentJobNames := configureJobNames()
					for _, name := range currentJobNames {
						observedJobNames[name] = true
					}
					// A retry creates the next job before the preceding failure is
					// observed and pruned, so one transient extra job is expected.
					g.Expect(len(currentJobNames)).To(BeNumerically("<=", numberOfJobsToKeep+1))
				}, 75*time.Second, 2*time.Second).Should(Succeed())
				Expect(len(observedJobNames)).To(BeNumerically(">", numberOfJobsToKeep+1))
			})

			By("succeeding once the gate exists", func() {
				platform.Kubectl("create", "configmap", gateConfigMap, "-n", gateNamespace)
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", promiseName, workflowStatusJSONPath)).
						To(ContainSubstring("True"))
				}).Should(Succeed())
			})

			By("continuing to reconcile after success", func() {
				jobsAtSuccess := configureJobNames()
				Eventually(func(g Gomega) {
					g.Expect(newJobNames(jobsAtSuccess, configureJobNames())).NotTo(BeEmpty())
				}).Should(Succeed())
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("get", "promise", promiseName, workflowStatusJSONPath)).
						To(ContainSubstring("True"))
				}).Should(Succeed())
			})
		})
	})

	When("reconcileAfterFailure is false", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "kratix-config-no-retry.yaml"))
			restartController()
			platform.Kubectl("apply", "-f", filepath.Join(assetsPath, "promise-workflow.yaml"))
		})

		It("does not retry on the schedule, but label and spec changes still reconcile it", func() {
			var failedJobs []string
			By("failing the promise configure workflow", func() {
				Eventually(func(g Gomega) {
					g.Expect(configureJobCount()).To(BeNumerically(">=", 1))
					g.Expect(platform.Kubectl("get", "promise", promiseName, workflowStatusJSONPath)).
						To(ContainSubstring("False"))
				}).Should(Succeed())
				failedJobs = configureJobNames()
			})

			By("not re-running on the schedule", func() {
				Consistently(func(g Gomega) {
					g.Expect(newJobNames(failedJobs, configureJobNames())).To(BeEmpty())
				}, 30*time.Second, 3*time.Second).Should(Succeed())
			})

			By("re-running when manually labelled", func() {
				platform.Kubectl("label", "--overwrite", "promise", promiseName,
					"kratix.io/manual-reconciliation=true")
				var manuallyStartedJob string
				Eventually(func(g Gomega) {
					newJobs := newJobNames(failedJobs, configureJobNames())
					g.Expect(newJobs).NotTo(BeEmpty())
					manuallyStartedJob = newJobs[0]
					g.Expect(platform.Kubectl("get", "job", manuallyStartedJob, "-n", gateNamespace,
						`-o=jsonpath={.status.failed}`)).To(Equal("1"))
				}).Should(Succeed())
			})

			By("re-running after a Promise spec change", func() {
				jobsBeforeSpecChange := configureJobNames()
				platform.Kubectl("patch", "promise", promiseName, "--type=merge", "-p",
					`{"spec":{"workflows":{"config":{"pipelineNamespace":"kratix-platform-system"}}}}`)
				Eventually(func(g Gomega) {
					g.Expect(newJobNames(jobsBeforeSpecChange, configureJobNames())).NotTo(BeEmpty())
				}).Should(Succeed())
			})
		})
	})
})
