package core_test

import (
	"fmt"
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/test/kubeutils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	timeout, longerTimeout, interval = 2 * time.Minute, 3 * time.Minute, 2 * time.Second
	workerOne                        = "worker-1"
	workerTwo                        = "worker-2"
)

var destinationName = getEnvOrDefault("DESTINATION_NAME", "worker-1")

var _ = Describe("Core Tests", Ordered, func() {
	BeforeAll(func() {
		SetDefaultEventuallyTimeout(timeout)
		SetDefaultEventuallyPollingInterval(interval)
		kubeutils.SetTimeoutAndInterval(timeout, interval)

		platform.Kubectl("apply", "-f", "assets/destination.yaml")
		platform.Kubectl("label", "destination", destinationName, "target="+workerOne)
		platform.Kubectl("label", "destination", workerTwo, "target="+workerTwo)
		worker.Kubectl("apply", "-f", "assets/flux.yaml")
	})

	AfterAll(func() {
		platform.Kubectl("delete", "-f", "assets/promise.yaml", "--ignore-not-found")
		platform.Kubectl("delete", "-f", "assets/destination.yaml", "--ignore-not-found")
		platform.Kubectl("label", "destination", destinationName, "target-")
		worker.Kubectl("delete", "-f", "assets/flux.yaml", "--ignore-not-found")
	})

	Context("Kratix Core Functionality", func() {
		var (
			resourceRequestName = "kratix-test"
			rrConfigMapName     = "ksystest-cm"      // needs to match assets/example-resource.yaml
			rrNsName            = "ksystest"         // needs to match assets/example-resource.yaml
			rrNsNameUpdated     = "ksystest-updated" // needs to match assets/example-resource-v2.yaml

			systemNamespaceFlag = "--namespace=kratix-platform-system"
			asYaml              = "--output=yaml"

			promiseLabel = "-l=kratix.io/promise-name=testbundle"
		)

		It("should deliver xaas to users", func() {
			var originalPromiseConfigMapTimestamp1 string
			By("successfully installing a Promise", func() {
				Expect(platform.Kubectl("apply", "-f", "assets/promise.yaml")).To(ContainSubstring("testbundle created"))

				By("applying the promise api as a crd", func() {
					Eventually(func() string {
						return platform.Kubectl("get", "crd")
					}, timeout, interval).Should(ContainSubstring("testbundles.test.kratix.io"))
				})

				By("running the promise configure pipeline", func() {
					Eventually(func() string {
						return platform.Kubectl("get", "pods", systemNamespaceFlag)
					}, timeout, interval).Should(ContainSubstring("setup-deps"))
				})

				By("deploying the dependencies to the correct destinations", func() {
					var workLabel string
					By("creating the right works and workplacements", func() {
						Eventually(func(g Gomega) {
							var works v1alpha1.WorkList
							kubeutils.ParseOutput(platform.Kubectl("get", "works", asYaml, systemNamespaceFlag, promiseLabel), &works)
							g.Expect(works.Items).To(HaveLen(1))

							workLabel = "-l=kratix.io/work=" + works.Items[0].Name
							var workPlacements v1alpha1.WorkPlacementList
							kubeutils.ParseOutput(
								platform.Kubectl("get", "workplacements", asYaml, systemNamespaceFlag, workLabel),
								&workPlacements,
							)
							g.Expect(workPlacements.Items).To(HaveLen(2))
							g.Expect(workPlacements.Items).To(SatisfyAll(
								ContainElement(SatisfyAll(
									HaveField("Spec.TargetDestinationName", Equal(destinationName)),
								)),
								ContainElement(SatisfyAll(
									HaveField("Spec.TargetDestinationName", Equal(workerTwo)),
								)),
							))
						}, timeout, interval).Should(Succeed())
					})

					By("setting status.conditions in workplacements", func() {
						Eventually(func(g Gomega) {
							var workPlacements v1alpha1.WorkPlacementList
							kubeutils.ParseOutput(
								platform.Kubectl("get", "workplacements", asYaml, systemNamespaceFlag, workLabel),
								&workPlacements,
							)
							for _, w := range workPlacements.Items {
								conditions := w.Status.Conditions
								for i := range conditions {
									conditions[i].LastTransitionTime = metav1.Time{}
								}
								g.Expect(conditions).To(ConsistOf(
									metav1.Condition{
										Type:    "ScheduleSucceeded",
										Status:  metav1.ConditionTrue,
										Reason:  "ScheduledToDestination",
										Message: "Scheduled to correct Destination"},
									metav1.Condition{
										Type:    "Ready",
										Status:  metav1.ConditionTrue,
										Reason:  "WorkloadsWrittenToTargetDestination",
										Message: "Ready",
									},
									metav1.Condition{
										Type:   "WriteSucceeded",
										Status: metav1.ConditionTrue,
										Reason: "WorkloadsWrittenToStateStore",
									}))
							}
						}, 30*time.Second, interval).Should(Succeed())
					})

					By("setting status.conditions in works", func() {
						Eventually(func(g Gomega) {
							var works v1alpha1.WorkList
							kubeutils.ParseOutput(platform.Kubectl("get", "works", asYaml, systemNamespaceFlag, promiseLabel), &works)
							g.Expect(works.Items).To(HaveLen(1))
							conditions := works.Items[0].Status.Conditions
							for i := range conditions {
								conditions[i].LastTransitionTime = metav1.Time{}
							}
							g.Expect(conditions).To(ConsistOf(
								metav1.Condition{
									Type:    "Ready",
									Status:  metav1.ConditionTrue,
									Reason:  "AllWorkplacementsScheduled",
									Message: "Ready",
								},
								metav1.Condition{
									Type:    "ScheduleSucceeded",
									Status:  metav1.ConditionTrue,
									Reason:  "AllWorkplacementsScheduled",
									Message: "All workplacements scheduled successfully",
								}))
						}, 30*time.Second, interval).Should(Succeed())
					})

					By("applying the documents", func() {
						Eventually(func(g Gomega) {
							g.Expect(worker.Kubectl("get", "namespaces")).To(ContainSubstring("testbundle-ns"))
							g.Expect(worker.Kubectl("-n", "testbundle-ns", "get", "configmap")).To(ContainSubstring("testbundle-cm"))
						}, timeout, interval).Should(Succeed())
					})

					By("updating the promise status", func() {
						promiseArgs := []string{"-n", "default", "get", "promise", "testbundle"}
						workflowCompletedCondition := `.status.conditions[?(@.type=="ConfigureWorkflowCompleted")]`
						reconciledCondition := `.status.conditions[?(@.type=="Reconciled")]`
						worksSucceededCondition := `.status.conditions[?(@.type=="WorksSucceeded")]`
						promiseStatus := ".status.status"
						promiseWorkflows := ".status.workflows"
						promiseWorkflowsSucceeded := ".status.workflowsSucceeded"
						promiseWorkflowsFailed := ".status.workflowsFailed"

						Eventually(func(g Gomega) {
							g.Expect(
								platform.Kubectl(append(promiseArgs, fmt.Sprintf(`-o=jsonpath='{%s}'`, promiseStatus))...),
							).To(ContainSubstring("Available"))
							g.Expect(
								platform.Kubectl(append(promiseArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, workflowCompletedCondition))...),
							).To(ContainSubstring("True"))
							g.Expect(
								platform.Kubectl(append(promiseArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, reconciledCondition))...),
							).To(ContainSubstring("True"))
							g.Expect(
								platform.Kubectl(append(promiseArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, worksSucceededCondition))...),
							).To(ContainSubstring("True"))
							g.Expect(
								platform.Kubectl(append(promiseArgs, fmt.Sprintf(`-o=jsonpath='{%s}'`, promiseWorkflows))...),
							).To(ContainSubstring("1"))
							g.Expect(
								platform.Kubectl(append(promiseArgs, fmt.Sprintf(`-o=jsonpath='{%s}'`, promiseWorkflowsFailed))...),
							).To(ContainSubstring("0"))
							g.Expect(
								platform.Kubectl(append(promiseArgs, fmt.Sprintf(`-o=jsonpath='{%s}'`, promiseWorkflowsSucceeded))...),
							).To(ContainSubstring("1"))
						}, timeout, interval).Should(Succeed())
					})

					originalPromiseConfigMapTimestamp1 = worker.Kubectl("-n", "testbundle-ns",
						"get", "configmap", "testbundle-cm", "-o=jsonpath={.data.timestamp}")
				})
			})

			By("fulfilling a resource request", func() {
				By("accepting a request", func() {
					Expect(platform.Kubectl("apply", "-f", "assets/example-resource.yaml")).To(ContainSubstring("kratix-test created"))
				})

				By("updating resource status", func() {
					rrArgs := []string{"-n", "default", "get", "testbundle", resourceRequestName}
					generation := platform.Kubectl(append(rrArgs, `-o=jsonpath='{.metadata.generation}'`)...)
					Eventually(func() string {
						return platform.Kubectl(append(rrArgs, "-o=jsonpath='{.status.stage}'")...)
					}, timeout, interval).Should(ContainSubstring("one"))

					workflowCompletedCondition := `.status.conditions[?(@.type=="ConfigureWorkflowCompleted")]`
					Eventually(func(g Gomega) {
						g.Expect(
							platform.Kubectl(append(rrArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, workflowCompletedCondition))...),
						).To(ContainSubstring("True"))
						g.Expect(platform.Kubectl(append(rrArgs, "-o=jsonpath='{.status.stage}'")...)).To(ContainSubstring("two"))
						g.Expect(platform.Kubectl(append(rrArgs, "-o=jsonpath='{.status.completed}'")...)).To(ContainSubstring("true"))
						g.Expect(platform.Kubectl(append(rrArgs, `-o=jsonpath='{.status.observedGeneration}'`)...)).To(Equal(generation))

						g.Expect(platform.Kubectl(append(rrArgs, "-o=jsonpath='{.status.workflows}'")...)).To(ContainSubstring("2"))
						g.Expect(platform.Kubectl(append(rrArgs, "-o=jsonpath='{.status.workflowsSucceeded}'")...)).To(ContainSubstring("2"))
						g.Expect(platform.Kubectl(append(rrArgs, "-o=jsonpath='{.status.workflowsFailed}'")...)).To(ContainSubstring("0"))
					}, timeout, interval).Should(Succeed())

					Eventually(func() bool {
						transitionTime := platform.Kubectl(
							append(rrArgs, fmt.Sprintf(`-o=jsonpath={%s.lastTransitionTime}`, workflowCompletedCondition))...,
						)
						lastSuccessful := platform.Kubectl(append(rrArgs, `-o=jsonpath={.status.lastSuccessfulConfigureWorkflowTime}`)...)
						return transitionTime == lastSuccessful
					}, timeout, interval).Should(BeTrue(), "lastTransitionTime should be equal to lastSuccessfulConfigureWorkflowTime")

					worksSucceededCondition := `.status.conditions[?(@.type=="WorksSucceeded")]`
					reconciledCondition := `.status.conditions[?(@.type=="Reconciled")]`
					Eventually(func(g Gomega) {
						g.Expect(
							platform.Kubectl(append(rrArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, worksSucceededCondition))...),
						).To(ContainSubstring("True"))
						g.Expect(
							platform.Kubectl(append(rrArgs, fmt.Sprintf(`-o=jsonpath='{%s.status}'`, reconciledCondition))...),
						).To(ContainSubstring("True"))
					}, timeout, interval).Should(Succeed())
				})

				By("creating the right works and workplacements", func() {
					Eventually(func(g Gomega) {
						var works v1alpha1.WorkList
						kubeutils.ParseOutput(platform.Kubectl("get", "works", asYaml, promiseLabel), &works)
						g.Expect(works.Items).To(HaveLen(2))

						singleDestinationWork := works.Items[0]
						multiDestinationWork := works.Items[1]
						if len(works.Items[0].Spec.WorkloadGroups) > 1 {
							singleDestinationWork = works.Items[1]
							multiDestinationWork = works.Items[0]
						}

						var singleDestinationWP v1alpha1.WorkPlacementList
						kubeutils.ParseOutput(
							platform.Kubectl("get", "workplacements", asYaml, "-l=kratix.io/work="+singleDestinationWork.Name),
							&singleDestinationWP,
						)
						g.Expect(singleDestinationWP.Items).To(HaveLen(1))
						g.Expect(singleDestinationWP.Items[0].Spec.TargetDestinationName).To(Equal(destinationName))

						var multiDestinationWP v1alpha1.WorkPlacementList
						kubeutils.ParseOutput(
							platform.Kubectl("get", "workplacements", asYaml, "-l=kratix.io/work="+multiDestinationWork.Name),
							&multiDestinationWP,
						)
						g.Expect(multiDestinationWP.Items).To(HaveLen(2))
						g.Expect(multiDestinationWP.Items[0].Spec.TargetDestinationName).To(Equal(destinationName))
						g.Expect(multiDestinationWP.Items[1].Spec.TargetDestinationName).To(Equal(workerTwo))
					}, timeout, interval).Should(Succeed())
				})

				cmArgs := []string{"-n", "testbundle-ns", "get", "cm"}
				By("scheduling works to the right destination", func() {
					// Increased timeout to prevent test failures caused by flux waits
					Eventually(func(g Gomega) {
						g.Expect(worker.Kubectl("get", "namespaces")).To(ContainSubstring(rrNsName))
						g.Expect(worker.Kubectl(append(cmArgs, rrConfigMapName+"-1")...)).To(ContainSubstring(rrConfigMapName))
						g.Expect(worker.Kubectl(append(cmArgs, rrConfigMapName+"-2")...)).To(ContainSubstring(rrConfigMapName))
					}, longerTimeout, interval).Should(Succeed())
				})

				By("rerunning pipelines when updating a resource request", func() {
					originalTimeStampW1 := worker.Kubectl(append(cmArgs, rrConfigMapName+"-1", "-o=jsonpath={.data.timestamp}")...)
					originalTimeStampW2 := worker.Kubectl(append(cmArgs, rrConfigMapName+"-2", "-o=jsonpath={.data.timestamp}")...)
					Expect(
						platform.Kubectl("apply", "-f", "assets/example-resource-v2.yaml"),
					).To(ContainSubstring("kratix-test configured"))

					// Increased timeout to prevent test failures caused by flux waits
					Eventually(func(g Gomega) {
						g.Expect(worker.Kubectl("get", "namespaces")).To(ContainSubstring(rrNsNameUpdated))
						g.Expect(
							worker.Kubectl(append(cmArgs, rrConfigMapName+"-1", "-o=jsonpath={.data.timestamp}")...),
						).ToNot(Equal(originalTimeStampW1))
						g.Expect(
							worker.Kubectl(append(cmArgs, rrConfigMapName+"-2", "-o=jsonpath={.data.timestamp}")...),
						).ToNot(Equal(originalTimeStampW2))
					}, longerTimeout, interval).Should(Succeed())
				})
			})

			By("updating Promise", func() {
				cmArgs := []string{"-n", "testbundle-ns", "get", "cm"}
				originalTimeStampW1 := worker.Kubectl(append(cmArgs, rrConfigMapName+"-1", "-o=jsonpath={.data.timestamp}")...)
				originalTimeStampW2 := worker.Kubectl(append(cmArgs, rrConfigMapName+"-2", "-o=jsonpath={.data.timestamp}")...)

				Expect(platform.Kubectl("apply", "-f", "assets/promise-v2.yaml")).To(ContainSubstring("testbundle configured"))

				By("updating the promise api", func() {
					Eventually(func() string {
						return platform.Kubectl("get", "crd", "testbundles.test.kratix.io", asYaml)
					}, timeout, interval).Should(ContainSubstring("newName"))
				})

				By("rerunning the promise configure pipeline", func() {
					getCMTimestamp := []string{"-n", "testbundle-ns",
						"get", "configmap", "testbundle-cm", "-o=jsonpath={.data.timestamp}"}
					getCMEnvValue := []string{"-n", "testbundle-ns",
						"get", "configmap", "testbundle-cm", "-o=jsonpath={.data.promiseEnv}"}

					// Increased timeout to prevent test failures caused by flux waits
					Eventually(func(g Gomega) {
						g.Expect(worker.Kubectl(getCMTimestamp...)).ToNot(Equal(originalPromiseConfigMapTimestamp1))
						g.Expect(worker.Kubectl(getCMEnvValue...)).To(ContainSubstring("second"))
					}, longerTimeout, interval).Should(Succeed())
				})

				By("rerunning the resource configure pipeline", func() {
					// Increased timeout to prevent test failures caused by flux waits
					Eventually(func(g Gomega) {
						g.Expect(worker.Kubectl("get", "namespaces")).To(ContainSubstring(rrNsNameUpdated))
						g.Expect(worker.Kubectl(append(cmArgs, rrConfigMapName+"-1", "-o=jsonpath={.data.timestamp}")...)).ToNot(Equal(originalTimeStampW1))
						g.Expect(worker.Kubectl(append(cmArgs, rrConfigMapName+"-2", "-o=jsonpath={.data.timestamp}")...)).ToNot(Equal(originalTimeStampW2))
					}, longerTimeout, interval).Should(Succeed())
				})
			})

			By("cleaning up after deleting a resource request", func() {
				platform.Kubectl("delete", "-f", "assets/example-resource-v2.yaml")
				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("-n", "default", "get", "workplacements")).NotTo(ContainSubstring(resourceRequestName))
					g.Expect(platform.Kubectl("-n", "default", "get", "works")).NotTo(ContainSubstring(resourceRequestName))
					g.Expect(worker.Kubectl("get", "namespaces")).NotTo(ContainSubstring(rrNsNameUpdated))
					g.Expect(worker.Kubectl("-n", "testbundle-ns", "get", "cm")).NotTo(ContainSubstring(rrConfigMapName))
				}, timeout, interval).Should(Succeed())
			})

			By("cleaning up after uninstalling the Promise", func() {
				platform.Kubectl("delete", "-f", "assets/promise-v2.yaml")

				Eventually(func() string {
					return platform.Kubectl("get", "crd")
				}, timeout, interval).ShouldNot(ContainSubstring("testbundles.test.kratix.io"))

				Eventually(func(g Gomega) {
					g.Expect(platform.Kubectl("-n", "kratix-platform-system", "get", "workplacements")).NotTo(ContainSubstring("testbundle"))
					g.Expect(platform.Kubectl("-n", "kratix-platform-system", "get", "works")).NotTo(ContainSubstring("testbundle"))
					g.Expect(worker.Kubectl("get", "namespaces")).NotTo(ContainSubstring("testbundle-ns"))
				}, timeout, interval).Should(Succeed())
			})
		})
	})
})
