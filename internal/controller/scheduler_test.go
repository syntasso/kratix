package controller_test

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	. "github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/internal/telemetry"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/hash"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Controllers/Scheduler", func() {
	var (
		scheduler                                                                           *Scheduler
		schedulerRecorder                                                                   *record.FakeRecorder
		workPlacements                                                                      v1alpha1.WorkPlacementList
		fakeCompressedContent                                                               []byte
		devDestination, devDestination2, pciDestination, prodDestination, strictDestination v1alpha1.Destination
		schedulerMetricsReader                                                              *sdkmetric.ManualReader
		restoreSchedulerMeter                                                               func()
	)

	BeforeEach(func() {
		telemetry.ResetWorkPlacementMetricsForTest()
		schedulerMetricsReader, restoreSchedulerMeter = setupSchedulerMeterProvider()

		// create a set of destinations to be used throughout the tests
		devDestination = newDestination("dev-1", map[string]string{"environment": "dev"})
		devDestination2 = newDestination("dev-2", map[string]string{"environment": "dev"})
		strictDestination = newDestination("strict", map[string]string{"strict": "true"})
		strictDestination.Spec.StrictMatchLabels = true
		pciDestination = newDestination("pci", map[string]string{"pci": "true"})
		prodDestination = newDestination("prod", map[string]string{"environment": "prod"})

		Expect(fakeK8sClient.Create(context.Background(), &devDestination)).To(Succeed())
		Expect(fakeK8sClient.Create(context.Background(), &devDestination2)).To(Succeed())
		Expect(fakeK8sClient.Create(context.Background(), &pciDestination)).To(Succeed())
		Expect(fakeK8sClient.Create(context.Background(), &prodDestination)).To(Succeed())
		Expect(fakeK8sClient.Create(context.Background(), &strictDestination)).To(Succeed())

		var err error
		fakeCompressedContent, err = compression.CompressContent([]byte(string("fake: content")))
		Expect(err).ToNot(HaveOccurred())

		schedulerRecorder = record.NewFakeRecorder(1024)
		scheduler = &Scheduler{
			Client:        fakeK8sClient,
			Log:           ctrl.Log.WithName("controllers").WithName("Scheduler"),
			EventRecorder: schedulerRecorder,
		}
	})

	AfterEach(func() {
		if restoreSchedulerMeter != nil {
			restoreSchedulerMeter()
		}
		telemetry.ResetWorkPlacementMetricsForTest()
	})

	Describe("#ReconcileWork", func() {
		Describe("Scheduling Resources", func() {
			var resourceWork, resourceWorkWithMultipleGroups v1alpha1.Work

			BeforeEach(func() {
				resourceWork = newWork("rr-work-name", true, schedulingFor(devDestination))
				resourceWorkWithMultipleGroups = newWorkWithTwoWorkloadGroups("rr-work-name-with-two-groups", true, schedulingFor(devDestination), schedulingFor(pciDestination))
			})

			When("a resource Work with scheduling is reconciled", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("creates a WorkPlacement for the new Work", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					workPlacement := workPlacements.Items[0]
					Expect(workPlacement.Namespace).To(Equal("default"))
					Expect(workPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name"))
					Expect(workPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(hash.ComputeHash(".")))
					Expect(workPlacement.Name).To(Equal("rr-work-name." + workPlacement.Spec.TargetDestinationName + "-" + hash.ComputeHash(".")[0:5]))
					Expect(workPlacement.Spec.Workloads).To(Equal(resourceWork.Spec.WorkloadGroups[0].Workloads))
					Expect(workPlacement.Spec.ID).To(Equal(resourceWork.Spec.WorkloadGroups[0].ID))
					Expect(workPlacement.Spec.TargetDestinationName).To(MatchRegexp("prod|dev\\-\\d"))
					Expect(workPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
					Expect(workPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
					Expect(workPlacement.Spec.PromiseName).To(Equal("promise"))
					Expect(workPlacement.Spec.ResourceName).To(Equal("resource"))
					Expect(workPlacement.GetLabels()).To(SatisfyAll(
						HaveKeyWithValue("kratix.io/pipeline-name", resourceWork.Labels["kratix.io/pipeline-name"]),
					))
					Expect(workPlacement.GetAnnotations()).To(Equal(resourceWork.GetAnnotations()))
				})

				It("records a scheduled outcome metric", func() {
					counts := collectWorkPlacementOutcomeMetrics(context.Background(), schedulerMetricsReader)
					Expect(counts).To(HaveKey(telemetry.WorkPlacementOutcomeScheduled))
					point := counts[telemetry.WorkPlacementOutcomeScheduled]
					Expect(point.Value).To(Equal(int64(1)))
					Expect(outcomeAttributeValue(point.Attributes, "promise")).To(Equal(resourceWork.Spec.PromiseName))
					Expect(outcomeAttributeValue(point.Attributes, "resource")).To(Equal(resourceWork.Spec.ResourceName))
					Expect(outcomeAttributeValue(point.Attributes, "namespace")).To(Equal(resourceWork.Namespace))
					Expect(outcomeAttributeValue(point.Attributes, "destination")).NotTo(BeEmpty())
				})
			})

			When("no destinations match the workload group selectors", func() {
				var unscheduledWork v1alpha1.Work

				BeforeEach(func() {
					unscheduledWork = newWork("rr-work-unscheduled", true, v1alpha1.WorkloadGroupScheduling{
						MatchLabels: map[string]string{"environment": "staging"},
					})
					_, err := scheduler.ReconcileWork(context.Background(), &unscheduledWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("records an unscheduled outcome metric", func() {
					counts := collectWorkPlacementOutcomeMetrics(context.Background(), schedulerMetricsReader)
					Expect(counts).To(HaveKey(telemetry.WorkPlacementOutcomeUnscheduled))
					point := counts[telemetry.WorkPlacementOutcomeUnscheduled]
					Expect(point.Value).To(Equal(int64(1)))
					Expect(outcomeAttributeValue(point.Attributes, "destination")).To(BeEmpty())
					Expect(outcomeAttributeValue(point.Attributes, "promise")).To(Equal("promise"))
					Expect(outcomeAttributeValue(point.Attributes, "namespace")).To(Equal(unscheduledWork.Namespace))
				})
			})

			When("an existing workplacement becomes misplaced", func() {
				var misplacedWork v1alpha1.Work

				BeforeEach(func() {
					misplacedWork = newWork("rr-work-misplaced", true, schedulingFor(prodDestination))
					workloadGroup := misplacedWork.Spec.WorkloadGroups[0]
					workPlacementName := fmt.Sprintf("%s.%s-%s", misplacedWork.Name, devDestination.Name, workloadGroup.ID[:5])
					workPlacement := &v1alpha1.WorkPlacement{
						ObjectMeta: metav1.ObjectMeta{
							Name:      workPlacementName,
							Namespace: misplacedWork.Namespace,
							Labels: map[string]string{
								v1alpha1.KratixPrefix + "work":              misplacedWork.Name,
								v1alpha1.KratixPrefix + "workload-group-id": workloadGroup.ID,
							},
						},
						Spec: v1alpha1.WorkPlacementSpec{
							ID:                    workloadGroup.ID,
							PromiseName:           misplacedWork.Spec.PromiseName,
							ResourceName:          misplacedWork.Spec.ResourceName,
							TargetDestinationName: devDestination.Name,
							Workloads:             workloadGroup.Workloads,
						},
					}
					workPlacement.SetPipelineName(&misplacedWork)
					Expect(fakeK8sClient.Create(context.Background(), workPlacement)).To(Succeed())

					_, err := scheduler.ReconcileWork(context.Background(), &misplacedWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("records a misplaced outcome metric", func() {
					counts := collectWorkPlacementOutcomeMetrics(context.Background(), schedulerMetricsReader)
					Expect(counts).NotTo(HaveKey(telemetry.WorkPlacementOutcomeScheduled))
					Expect(counts).To(HaveKey(telemetry.WorkPlacementOutcomeMisplaced))

					misplacedPoint := counts[telemetry.WorkPlacementOutcomeMisplaced]
					Expect(misplacedPoint.Value).To(Equal(int64(1)))
					Expect(outcomeAttributeValue(misplacedPoint.Attributes, "destination")).To(Equal(devDestination.Name))
				})
			})

			When("a resource Work with scheduling is reconciled twice", func() {
				var workPlacement v1alpha1.WorkPlacement

				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())

					latestWork := &v1alpha1.Work{}
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), latestWork)).To(Succeed())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))
					workPlacement = workPlacements.Items[0]

					_, err = scheduler.ReconcileWork(context.Background(), latestWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("does not change the existing WorkPlacement", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))
					newWorkPlacement := workPlacements.Items[0]

					// compare each field in the work placements to ensure they are the same
					Expect(newWorkPlacement.Name).To(Equal(workPlacement.Name))
					Expect(newWorkPlacement.Namespace).To(Equal(workPlacement.Namespace))
					Expect(newWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal(workPlacement.ObjectMeta.Labels["kratix.io/work"]))
					Expect(newWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(workPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]))
					Expect(newWorkPlacement.Spec.Workloads).To(Equal(workPlacement.Spec.Workloads))
					Expect(newWorkPlacement.Spec.ID).To(Equal(workPlacement.Spec.ID))
					Expect(newWorkPlacement.Spec.TargetDestinationName).To(Equal(workPlacement.Spec.TargetDestinationName))
					Expect(newWorkPlacement.Finalizers).To(Equal(workPlacement.Finalizers))
					Expect(newWorkPlacement.Spec.PromiseName).To(Equal(workPlacement.Spec.PromiseName))
					Expect(newWorkPlacement.Spec.ResourceName).To(Equal(workPlacement.Spec.ResourceName))
					Expect(newWorkPlacement.GetAnnotations()).To(Equal(workPlacement.GetAnnotations()))
				})
			})

			When("a resource Work with scheduling is reconciled with an updated WorkloadGroup", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("updates the WorkPlacement", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					workPlacement := workPlacements.Items[0]
					Expect(workPlacement.Spec.Workloads).To(HaveLen(1))

					previousResourceVersion, err := strconv.Atoi(workPlacement.ResourceVersion)
					Expect(err).ToNot(HaveOccurred())

					// update the Work's WorkloadGroup with an extra Workload
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork)).To(Succeed())
					resourceWork.Spec.WorkloadGroups[0].Workloads = append(resourceWork.Spec.WorkloadGroups[0].Workloads, v1alpha1.Workload{
						Content: string(fakeCompressedContent),
					})

					_, err = scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					// expect the new Workload to be added to the existing WorkPlacement
					workPlacement = workPlacements.Items[0]
					Expect(workPlacement.Spec.Workloads).To(HaveLen(2))
					Expect(workPlacement.Spec.Workloads).To(ContainElement(v1alpha1.Workload{
						Content: string(fakeCompressedContent),
					}))

					newResourceVersion, err := strconv.Atoi(workPlacement.ResourceVersion)
					Expect(err).ToNot(HaveOccurred())
					Expect(newResourceVersion).To(BeNumerically(">", previousResourceVersion))
				})
			})

			When("an update to the resource Work deletes the only WorkloadGroup", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					// set WorkloadGroups to an empty list and reconcile again
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork)).To(Succeed())
					resourceWork.Spec.WorkloadGroups = []v1alpha1.WorkloadGroup{}
					Expect(fakeK8sClient.Update(context.Background(), &resourceWork)).To(Succeed())
					_, err = scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())
				})

				/* what is this testing? */
				It("removes the WorkPlacements for the deleted WorkloadGroup", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					// until the finalizer is removed on the WorkPlacement, it will not be deleted
					Expect(workPlacements.Items).To(HaveLen(1))

					// remove finalizers so the old WorkPlacement can be deleted
					workPlacement := workPlacements.Items[0]
					workPlacement.Finalizers = nil
					Expect(fakeK8sClient.Update(context.Background(), &workPlacement)).To(Succeed())

					// the WorkPlacement should now be deleted
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(BeEmpty())
				})
			})

			When("an update to the resource Work adds a new WorkloadGroup", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					// append a new WorkloadGroup to the Work and reconcile again
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork)).To(Succeed())
					Expect(resourceWork.Spec.WorkloadGroups).To(HaveLen(1))
					resourceWork.Spec.WorkloadGroups = append(resourceWork.Spec.WorkloadGroups, v1alpha1.WorkloadGroup{
						Directory: "foo",
						ID:        hash.ComputeHash("foo"),
						Workloads: []v1alpha1.Workload{
							{
								Content: string(fakeCompressedContent),
							},
						},
						DestinationSelectors: []v1alpha1.WorkloadGroupScheduling{schedulingFor(devDestination)},
					})
					Expect(fakeK8sClient.Update(context.Background(), &resourceWork)).To(Succeed())
					_, err = scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(resourceWork.Spec.WorkloadGroups).To(HaveLen(2))
					Expect(err).ToNot(HaveOccurred())
				})

				It("creates a WorkPlacement for the new WorkloadGroup", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					// expect the new WorkloadGroup to create a new WorkPlacement
					Expect(workPlacements.Items).To(HaveLen(2))

					var newWorkPlacement v1alpha1.WorkPlacement
					for _, wp := range workPlacements.Items {
						if wp.Spec.ID == hash.ComputeHash("foo") {
							newWorkPlacement = wp
						}
					}
					Expect(newWorkPlacement.Namespace).To(Equal("default"))
					Expect(newWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name"))
					Expect(newWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(hash.ComputeHash("foo")))
					Expect(newWorkPlacement.Name).To(Equal("rr-work-name." + newWorkPlacement.Spec.TargetDestinationName + "-" + hash.ComputeHash("foo")[0:5]))
					Expect(resourceWork.Spec.WorkloadGroups).To(HaveLen(2))
					Expect(newWorkPlacement.Spec.Workloads).To(Equal(resourceWork.Spec.WorkloadGroups[1].Workloads))
					Expect(newWorkPlacement.Spec.ID).To(Equal(resourceWork.Spec.WorkloadGroups[1].ID))
					Expect(newWorkPlacement.Spec.TargetDestinationName).To(MatchRegexp("prod|dev\\-\\d"))
					Expect(newWorkPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
					Expect(newWorkPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
					Expect(newWorkPlacement.Spec.PromiseName).To(Equal("promise"))
					Expect(newWorkPlacement.Spec.ResourceName).To(Equal("resource"))
				})
			})

			When("an update to the resource Work replaces a WorkloadGroup with a new WorkloadGroup", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					// replace the WorkloadGroup in the Work and reconcile again
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork)).To(Succeed())
					resourceWork.Spec.WorkloadGroups = []v1alpha1.WorkloadGroup{
						{
							Directory: "foo",
							ID:        hash.ComputeHash("foo"),
							Workloads: []v1alpha1.Workload{
								{
									Content: string(fakeCompressedContent),
								},
							},
						},
					}
					_, err = scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("deletes the WorkPlacement for the old WorkloadGroup and creates a WorkPlacement for the new WorkloadGroup", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					// until the finalizer is removed on the replaced WorkPlacement, it will not be deleted
					Expect(workPlacements.Items).To(HaveLen(2))

					// remove finalizers on the old WorkPlacement so it can be deleted
					for _, wp := range workPlacements.Items {
						if wp.Spec.ID == hash.ComputeHash(".") {
							wp.Finalizers = nil
							Expect(fakeK8sClient.Update(context.Background(), &wp)).To(Succeed())
						}
					}

					// the old WorkPlacement should now be deleted
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					workPlacement := workPlacements.Items[0]
					Expect(workPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(hash.ComputeHash("foo")))
					Expect(workPlacement.Name).To(Equal("rr-work-name." + workPlacement.Spec.TargetDestinationName + "-" + hash.ComputeHash("foo")[0:5]))
					Expect(workPlacement.Spec.Workloads).To(Equal(resourceWork.Spec.WorkloadGroups[0].Workloads))
					Expect(workPlacement.Spec.ID).To(Equal(resourceWork.Spec.WorkloadGroups[0].ID))
				})
			})

			When("the Work needs to schedule to multiple destinations", func() {
				When("the Work is reconciled", func() {
					BeforeEach(func() {
						_, err := scheduler.ReconcileWork(context.Background(), &resourceWorkWithMultipleGroups)
						Expect(err).ToNot(HaveOccurred())
					})

					It("creates a WorkPlacement per WorkloadGroup", func() {
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(2))

						var pciWorkPlacement, devOrProdWorkPlacement v1alpha1.WorkPlacement
						for _, wp := range workPlacements.Items {
							if wp.Spec.TargetDestinationName == "pci" {
								pciWorkPlacement = wp
							} else {
								devOrProdWorkPlacement = wp
							}
						}

						Expect(devOrProdWorkPlacement.Namespace).To(Equal("default"))
						Expect(devOrProdWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name-with-two-groups"))
						Expect(devOrProdWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(hash.ComputeHash(".")))
						Expect(devOrProdWorkPlacement.Name).To(Equal("rr-work-name-with-two-groups." + devOrProdWorkPlacement.Spec.TargetDestinationName + "-" + hash.ComputeHash(".")[0:5]))
						Expect(devOrProdWorkPlacement.Spec.Workloads).To(Equal(resourceWorkWithMultipleGroups.Spec.WorkloadGroups[0].Workloads))
						Expect(devOrProdWorkPlacement.Spec.TargetDestinationName).To(MatchRegexp("prod|dev\\-\\d"))
						Expect(devOrProdWorkPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
						Expect(devOrProdWorkPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
						Expect(devOrProdWorkPlacement.Spec.PromiseName).To(Equal("promise"))
						Expect(devOrProdWorkPlacement.Spec.ResourceName).To(Equal("resource"))
						Expect(devOrProdWorkPlacement.GetAnnotations()).To(Equal(resourceWorkWithMultipleGroups.GetAnnotations()))

						Expect(pciWorkPlacement.Namespace).To(Equal("default"))
						Expect(pciWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name-with-two-groups"))
						Expect(pciWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(hash.ComputeHash("foo")))
						Expect(pciWorkPlacement.Name).To(Equal("rr-work-name-with-two-groups." + pciWorkPlacement.Spec.TargetDestinationName + "-" + hash.ComputeHash("foo")[0:5]))
						Expect(pciWorkPlacement.Spec.Workloads).To(Equal(resourceWorkWithMultipleGroups.Spec.WorkloadGroups[1].Workloads))
						Expect(pciWorkPlacement.Spec.TargetDestinationName).To(Equal("pci"))
						Expect(pciWorkPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
						Expect(pciWorkPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
						Expect(pciWorkPlacement.Spec.PromiseName).To(Equal("promise"))
						Expect(pciWorkPlacement.Spec.ResourceName).To(Equal("resource"))
						Expect(pciWorkPlacement.Spec.ResourceName).To(Equal("resource"))
						Expect(pciWorkPlacement.GetAnnotations()).To(Equal(resourceWorkWithMultipleGroups.GetAnnotations()))
					})
				})

				When("an update to the resource Work deletes one of the WorkloadGroups", func() {
					var pciWorkPlacement v1alpha1.WorkPlacement

					BeforeEach(func() {
						_, err := scheduler.ReconcileWork(context.Background(), &resourceWorkWithMultipleGroups)
						Expect(err).ToNot(HaveOccurred())

						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(2))

						for _, wp := range workPlacements.Items {
							if wp.Spec.TargetDestinationName == "pci" {
								pciWorkPlacement = wp
							}
						}

						// remove the pci WorkloadGroup from the Work and reconcile again
						pciWorkloadGroupID := hash.ComputeHash("foo")
						Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWorkWithMultipleGroups), &resourceWorkWithMultipleGroups)).To(Succeed())
						for i, wg := range resourceWorkWithMultipleGroups.Spec.WorkloadGroups {
							if wg.ID == pciWorkloadGroupID {
								resourceWorkWithMultipleGroups.Spec.WorkloadGroups = append(resourceWorkWithMultipleGroups.Spec.WorkloadGroups[:i], resourceWorkWithMultipleGroups.Spec.WorkloadGroups[i+1:]...)
							}
						}

						Expect(fakeK8sClient.Update(context.Background(), &resourceWorkWithMultipleGroups)).To(Succeed())
						_, err = scheduler.ReconcileWork(context.Background(), &resourceWorkWithMultipleGroups)
						Expect(err).ToNot(HaveOccurred())
					})

					It("removes the WorkPlacements for the deleted WorkloadGroup", func() {
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())

						// until the finalizer is removed on the WorkPlacement, it will not be deleted
						Expect(workPlacements.Items).To(HaveLen(2))

						// remove finalizers so the old WorkPlacement can be deleted
						Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&pciWorkPlacement), &pciWorkPlacement)).To(Succeed())
						pciWorkPlacement.Finalizers = nil
						Expect(fakeK8sClient.Update(context.Background(), &pciWorkPlacement)).To(Succeed())

						// the pci WorkPlacement should now be deleted
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(1))
						Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(MatchRegexp("prod|dev\\-\\d"))
					})
				})

				When("one of the WorkloadGroups is unschedulable", func() {
					var unschedulable []string

					BeforeEach(func() {
						var err error

						// set the dev/prod WorkloadGroup to be unschedulable (i.e. no matching Destinations)
						resourceWorkWithMultipleGroups.Spec.WorkloadGroups[0].DestinationSelectors[0].MatchLabels = map[string]string{"not": "schedulable"}

						unschedulable, err = scheduler.ReconcileWork(context.Background(), &resourceWorkWithMultipleGroups)
						Expect(err).NotTo(HaveOccurred())
					})

					It("still schedules the remaining workload groups", func() {
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(1))

						pciWorkPlacement := workPlacements.Items[0]
						Expect(pciWorkPlacement.Namespace).To(Equal("default"))
						Expect(pciWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name-with-two-groups"))
						Expect(pciWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(hash.ComputeHash("foo")))
						Expect(pciWorkPlacement.Name).To(Equal("rr-work-name-with-two-groups." + pciWorkPlacement.Spec.TargetDestinationName + "-" + hash.ComputeHash("foo")[0:5]))
						Expect(pciWorkPlacement.Spec.TargetDestinationName).To(Equal("pci"))
						Expect(pciWorkPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
						Expect(pciWorkPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
						Expect(pciWorkPlacement.Spec.Workloads).To(Equal(resourceWorkWithMultipleGroups.Spec.WorkloadGroups[1].Workloads))
						Expect(pciWorkPlacement.Spec.PromiseName).To(Equal("promise"))
						Expect(pciWorkPlacement.Spec.ResourceName).To(Equal("resource"))
						Expect(pciWorkPlacement.GetAnnotations()).To(Equal(resourceWorkWithMultipleGroups.GetAnnotations()))
					})

					It("returns an error indicating what was unschedulable", func() {
						Expect(unschedulable).To(ConsistOf(hash.ComputeHash(".")))
					})
				})
			})

			When("the scheduling changes such that the WorkPlacement is not on a correct Destination", func() {
				var preUpdateDestination string

				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					preUpdateDestination = workPlacements.Items[0].Spec.TargetDestinationName

					// change the scheduling on the resource work from devDestination to prodDestination
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork)).To(Succeed())
					resourceWork.Spec.WorkloadGroups[0].DestinationSelectors[0] = schedulingFor(prodDestination)
					_, err = scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("does not reschedule the WorkPlacement", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					workPlacement := workPlacements.Items[0]
					Expect(workPlacement.Spec.TargetDestinationName).To(Equal(preUpdateDestination))
					Expect(workPlacement.GetLabels()).To(HaveKeyWithValue("kratix.io/misplaced", "true"))
					Expect(workPlacement.GetLabels()).To(HaveKeyWithValue("kratix.io/workload-group-id", hash.ComputeHash(".")))
					Expect(workPlacement.Status.Conditions).To(HaveLen(2))
					//ignore time for assertion
					workPlacement.Status.Conditions[0].LastTransitionTime = metav1.Time{}
					workPlacement.Status.Conditions[1].LastTransitionTime = metav1.Time{}
					Expect(workPlacement.Status.Conditions).To(ConsistOf(misplacedWorkPlacementConditions()))
					Eventually(schedulerRecorder.Events).Should(Receive(ContainSubstring(
						fmt.Sprintf("labels for destination: %s no longer match the expected labels, "+
							"marking this workplacement as misplaced", preUpdateDestination))))
				})

				It("sets correct status conditions on the resource Work to indicate it's misplaced", func() {
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork)).To(Succeed())
					Expect(resourceWork.Status.Conditions).To(HaveLen(2))
					resourceWork.Status.Conditions[0].LastTransitionTime = metav1.Time{}
					resourceWork.Status.Conditions[1].LastTransitionTime = metav1.Time{}
					Expect(resourceWork.Status.Conditions).To(ConsistOf(misplacedConditions(resourceWork.Spec.WorkloadGroups[0].ID)))
					Eventually(schedulerRecorder.Events).Should(Receive(ContainSubstring(
						fmt.Sprintf(
							"Target destination no longer matches destinationSelectors for workloadGroups: [%s]",
							resourceWork.Spec.WorkloadGroups[0].ID))))
				})
			})

			When("scheduling is defined in the Promise, Promise Workflow and Resource Workflow", func() {
				BeforeEach(func() {
					resourceWork.Spec.WorkloadGroups[0].DestinationSelectors = []v1alpha1.WorkloadGroupScheduling{
						{
							MatchLabels: map[string]string{
								"environment": "dev",
							},
							Source: "promise-workflow",
						},
						{
							MatchLabels: map[string]string{
								"environment": "prod",
							},
							Source: "promise",
						},
						{
							MatchLabels: map[string]string{
								"environment": "staging",
							},
							Source: "resource-workflow",
						},
					}
					_, err := scheduler.ReconcileWork(context.Background(), &resourceWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("prioritises Promise scheduling", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					workPlacement := workPlacements.Items[0]
					Expect(workPlacement.Spec.TargetDestinationName).To(Equal("prod"))
				})
			})
		})

		Describe("Scheduling Dependencies", func() {
			var dependencyWork, dependencyWorkForDev, dependencyWorkForProd v1alpha1.Work

			BeforeEach(func() {
				dependencyWork = newWork("work-name", false)
				dependencyWorkForDev = newWork("dev-work-name", false, schedulingFor(devDestination))
				dependencyWorkForProd = newWork("prod-work-name", false, schedulingFor(prodDestination))
			})

			When("the Work matches a single Destination", func() {
				When("the Work is reconciled", func() {
					BeforeEach(func() {
						_, err := scheduler.ReconcileWork(context.Background(), &dependencyWorkForProd)
						Expect(err).ToNot(HaveOccurred())
					})

					It("creates a single WorkPlacement", func() {
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(1))
						Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(Equal(prodDestination.Name))
						Expect(workPlacements.Items[0].GetLabels()).To(SatisfyAll(
							HaveKeyWithValue("kratix.io/pipeline-name", dependencyWorkForProd.Labels["kratix.io/pipeline-name"]),
							HaveKeyWithValue("kratix.io/work", dependencyWorkForProd.Name),
						))
						Expect(workPlacements.Items[0].GetAnnotations()).To(Equal(dependencyWorkForProd.GetAnnotations()))
					})
				})

				When("the WorkPlacement is deleted", func() {
					BeforeEach(func() {
						_, err := scheduler.ReconcileWork(context.Background(), &dependencyWorkForProd)
						Expect(err).ToNot(HaveOccurred())
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(1))

						// remove finalizer so the WorkPlacement can be deleted
						workPlacement := workPlacements.Items[0]
						workPlacement.Finalizers = nil
						Expect(fakeK8sClient.Update(context.Background(), &workPlacement)).To(Succeed())

						// manually delete the WorkPlacement
						Expect(fakeK8sClient.Delete(context.Background(), &workPlacement)).To(Succeed())

						// check that the WorkPlacement has been deleted
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(BeEmpty())
					})

					It("gets recreated on next reconciliation", func() {
						// re-reconcile the Work
						Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWorkForProd), &dependencyWorkForProd)).To(Succeed())
						_, err := scheduler.ReconcileWork(context.Background(), &dependencyWorkForProd)
						Expect(err).ToNot(HaveOccurred())

						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(1))
						Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(Equal(prodDestination.Name))
						Expect(workPlacements.Items[0].ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForProd.Name))
					})
				})
			})

			When("the Work matches multiple Destinations", func() {
				When("the Work is reconciled", func() {
					BeforeEach(func() {
						_, err := scheduler.ReconcileWork(context.Background(), &dependencyWorkForDev)
						Expect(err).ToNot(HaveOccurred())
					})

					It("creates WorkPlacements for the Destinations with the label", func() {
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(2))

						devWorkPlacement := workPlacements.Items[0]
						Expect(devWorkPlacement.Spec.TargetDestinationName).To(Equal(devDestination.Name))
						Expect(devWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForDev.Name))

						devWorkPlacement2 := workPlacements.Items[1]
						Expect(devWorkPlacement2.Spec.TargetDestinationName).To(Equal(devDestination2.Name))
						Expect(devWorkPlacement2.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForDev.Name))
					})
				})

				When("a WorkPlacement is deleted", func() {
					BeforeEach(func() {
						_, err := scheduler.ReconcileWork(context.Background(), &dependencyWorkForDev)
						Expect(err).ToNot(HaveOccurred())

						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(2))

						Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(Equal(devDestination.Name))
						devWorkPlacement := workPlacements.Items[0]

						Expect(workPlacements.Items[1].Spec.TargetDestinationName).To(Equal(devDestination2.Name))

						// remove finalizers on the devDestination WorkPlacement so it can be deleted
						devWorkPlacement.Finalizers = nil
						Expect(fakeK8sClient.Update(context.Background(), &devWorkPlacement)).To(Succeed())

						// manually delete the WorkPlacement
						Expect(fakeK8sClient.Delete(context.Background(), &devWorkPlacement)).To(Succeed())

						// check that the devDestination WorkPlacement has been deleted
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(1))
						Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(Equal(devDestination2.Name))
					})

					It("gets recreated on next reconciliation", func() {
						// re-reconcile the Work
						Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWorkForDev), &dependencyWorkForDev)).To(Succeed())
						_, err := scheduler.ReconcileWork(context.Background(), &dependencyWorkForDev)
						Expect(err).ToNot(HaveOccurred())

						// check the devDestination WorkPlacement has been recreated
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(2))

						Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(Equal(devDestination.Name))
						devWorkPlacement := workPlacements.Items[0]

						Expect(workPlacements.Items[1].Spec.TargetDestinationName).To(Equal(devDestination2.Name))
						devWorkPlacement2 := workPlacements.Items[1]

						Expect(devWorkPlacement.Spec.TargetDestinationName).To(Equal(devDestination.Name))
						Expect(devWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForDev.Name))

						// check that the devDestination2 WorkPlacement is still there
						Expect(devWorkPlacement2.Spec.TargetDestinationName).To(Equal(devDestination2.Name))
						Expect(devWorkPlacement2.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForDev.Name))
						// TODO it would be nice if we could check this is exactly the same
						// object as the original WorkPlacement for devWorkPlacement2:
						// https://github.com/syntasso/kratix/pull/59#discussion_r1435163108
					})
				})
			})

			When("the Work selector matches no Destinations", func() {
				var unschedulable []string

				BeforeEach(func() {
					dependencyWork.Spec.WorkloadGroups[0].DestinationSelectors = []v1alpha1.WorkloadGroupScheduling{
						{
							MatchLabels: map[string]string{"environment": "non-matching"},
						},
					}

					var err error
					unschedulable, err = scheduler.ReconcileWork(context.Background(), &dependencyWork)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork)).To(Succeed())
				})

				It("creates no workplacements", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(BeEmpty())
				})

				It("does not mark the Work as scheduled", func() {
					Expect(dependencyWork.Status.Conditions[0].Type).To(Equal("ScheduleSucceeded"))
					Expect(dependencyWork.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))
				})

				It("returns the unschedulable workload group IDs", func() {
					Expect(unschedulable).To(ConsistOf(hash.ComputeHash(".")))
				})
			})

			When("the Work has no selector", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &dependencyWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("creates WorkPlacements for all non-strict label matching registered Destinations", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(4))
					for _, workPlacement := range workPlacements.Items {
						Expect(workPlacement.Spec.Workloads).To(ConsistOf(v1alpha1.Workload{
							Content: "key: value",
						}))
						Expect(workPlacement.Spec.TargetDestinationName).ToNot(Equal(strictDestination.Name))
					}
				})

				It("updates WorkPlacements for all registered Destinations", func() {
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork)).To(Succeed())
					dependencyWork.Spec.WorkloadGroups[0].Workloads = append(dependencyWork.Spec.WorkloadGroups[0].Workloads, v1alpha1.Workload{
						Content: "fake: new-content",
					})

					_, err := scheduler.ReconcileWork(context.Background(), &dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(4))
					for _, workPlacement := range workPlacements.Items {
						Expect(workPlacement.Spec.Workloads).To(ConsistOf(
							v1alpha1.Workload{Content: "key: value"},
							v1alpha1.Workload{Content: "fake: new-content"},
						))
					}
				})
			})

			When("the Work is updated to no longer match a previous Destination", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					// add scheduling for the devDestination, so that the WorkPlacements
					// for prod and pci are now misplaced
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork)).To(Succeed())
					dependencyWork.Spec.WorkloadGroups[0].DestinationSelectors = []v1alpha1.WorkloadGroupScheduling{
						schedulingFor(devDestination),
					}

					_, err = scheduler.ReconcileWork(context.Background(), &dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(4))

					// ensure the WorkPlacements are sorted by name, so the tests aren't
					// flaky by varying the order they are returned from the API!
					sort.Slice(workPlacements.Items, func(i, j int) bool {
						return workPlacements.Items[i].Spec.TargetDestinationName < workPlacements.Items[j].Spec.TargetDestinationName
					})
				})

				It("marks existing WorkPlacements which no longer match as misplaced", func() {
					// the two dev WorkPlacements should not be marked as misplaced
					Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(Equal("dev-1"))
					Expect(workPlacements.Items[0].GetLabels()).NotTo(HaveKey("kratix.io/misplaced"))

					Expect(workPlacements.Items[1].Spec.TargetDestinationName).To(Equal("dev-2"))
					Expect(workPlacements.Items[1].GetLabels()).NotTo(HaveKey("kratix.io/misplaced"))

					// the pci and prod WorkPlacements should be marked as misplaced

					Expect(workPlacements.Items[2].Spec.TargetDestinationName).To(Equal("pci"))
					Expect(workPlacements.Items[2].GetLabels()).To(HaveKeyWithValue("kratix.io/misplaced", "true"))
					Expect(workPlacements.Items[2].Status.Conditions).To(HaveLen(2))
					//ignore time for assertion
					workPlacements.Items[2].Status.Conditions[0].LastTransitionTime = metav1.Time{}
					workPlacements.Items[2].Status.Conditions[1].LastTransitionTime = metav1.Time{}
					Expect(workPlacements.Items[2].Status.Conditions).To(ConsistOf(misplacedWorkPlacementConditions()))

					Expect(workPlacements.Items[3].Spec.TargetDestinationName).To(Equal("prod"))
					Expect(workPlacements.Items[3].GetLabels()).To(HaveKeyWithValue("kratix.io/misplaced", "true"))
					Expect(workPlacements.Items[3].Status.Conditions).To(HaveLen(2))
					//ignore time for assertion
					workPlacements.Items[3].Status.Conditions[0].LastTransitionTime = metav1.Time{}
					workPlacements.Items[2].Status.Conditions[1].LastTransitionTime = metav1.Time{}
					Expect(workPlacements.Items[2].Status.Conditions).To(ConsistOf(misplacedWorkPlacementConditions()))
				})

				It("keeps the misplaced WorkPlacements updated", func() {
					// update the Work's WorkloadGroup with an extra Workload
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork)).To(Succeed())
					dependencyWork.Spec.WorkloadGroups[0].Workloads = append(dependencyWork.Spec.WorkloadGroups[0].Workloads, v1alpha1.Workload{
						Content: "fake: new-content",
					})

					_, err := scheduler.ReconcileWork(context.Background(), &dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(4))

					// check that all WorkPlacements have been updated, including the two
					// misplaced ones
					for _, workPlacement := range workPlacements.Items {
						Expect(workPlacement.Spec.Workloads).To(ConsistOf(
							v1alpha1.Workload{Content: "key: value"},
							v1alpha1.Workload{Content: "fake: new-content"},
						))
					}
				})
			})
		})

		Describe("Work Status", func() {
			var work v1alpha1.Work
			BeforeEach(func() {
				work = newWorkWithTwoWorkloadGroups("rr-work-name-with-two-groups", true, schedulingFor(devDestination), schedulingFor(pciDestination))
			})

			When("all workloads groups can be scheduled", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &work)
					Expect(err).ToNot(HaveOccurred())
					Expect(fakeK8sClient.Get(
						context.Background(),
						client.ObjectKeyFromObject(&work), &work),
					).To(Succeed())
				})

				It("sets the right status and conditions", func() {
					Expect(work.Status.WorkPlacements).To(Equal(2))
					Expect(work.Status.WorkPlacementsCreated).To(Equal(2))

					readyCond := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
					Expect(readyCond).ToNot(BeNil())
					Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
					Expect(readyCond.Reason).To(Equal("AllWorkplacementsScheduled"))
					Expect(readyCond.Message).To(Equal("Ready"))

					scheduleSucceededCond := apimeta.FindStatusCondition(work.Status.Conditions, "ScheduleSucceeded")
					Expect(scheduleSucceededCond).ToNot(BeNil())
					Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
					Expect(scheduleSucceededCond.Reason).To(Equal("AllWorkplacementsScheduled"))
					Expect(scheduleSucceededCond.Message).To(Equal("All workplacements scheduled successfully"))
				})

				It("sends the right events", func() {
					workplacements := &v1alpha1.WorkPlacementList{}
					Expect(fakeK8sClient.List(context.Background(), workplacements)).To(Succeed())

					Expect(aggregateEvents(schedulerRecorder.Events)).To(SatisfyAll(
						ContainSubstring(
							"Normal WorkplacementReconciled workplacement reconciled: %s, operation: created",
							workplacements.Items[0].GetName()),
						ContainSubstring("Normal AllWorkplacementsScheduled All workplacements scheduled successfully")))
				})

				When("multiple destination matches the scheduling rules", func() {
					var devDestination3, devDestination4, devDestination5 v1alpha1.Destination
					BeforeEach(func() {
						devDestination3 = newDestination("dev-3", map[string]string{"environment": "dev"})
						devDestination4 = newDestination("dev-4", map[string]string{"environment": "dev"})
						devDestination5 = newDestination("dev-5", map[string]string{"environment": "dev"})
						Expect(fakeK8sClient.Create(context.Background(), &devDestination3)).To(Succeed())
						Expect(fakeK8sClient.Create(context.Background(), &devDestination4)).To(Succeed())
						Expect(fakeK8sClient.Create(context.Background(), &devDestination5)).To(Succeed())
					})

					AfterEach(func() {
						Expect(fakeK8sClient.Delete(context.Background(), &devDestination3)).To(Succeed())
						Expect(fakeK8sClient.Delete(context.Background(), &devDestination4)).To(Succeed())
						Expect(fakeK8sClient.Delete(context.Background(), &devDestination5)).To(Succeed())
					})

					It("does not falsely mark work and workplacements as misplaced", func() {
						_, err := scheduler.ReconcileWork(context.Background(), &work)
						Expect(err).ToNot(HaveOccurred())
						Expect(work.Status.WorkPlacements).To(Equal(2))
						Expect(work.Status.WorkPlacementsCreated).To(Equal(2))

						readyCond := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
						Expect(readyCond).ToNot(BeNil())
						Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
						Expect(readyCond.Message).To(Equal("Ready"))

						scheduleSucceededCond := apimeta.FindStatusCondition(work.Status.Conditions, "ScheduleSucceeded")
						Expect(scheduleSucceededCond).ToNot(BeNil())
						Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
					})
				})
			})

			When("some workloads groups can't be scheduled", func() {
				BeforeEach(func() {
					work.Spec.WorkloadGroups[0].DestinationSelectors = []v1alpha1.WorkloadGroupScheduling{
						{
							MatchLabels: map[string]string{"environment": "non-matching"},
						},
					}

					_, err := scheduler.ReconcileWork(context.Background(), &work)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeK8sClient.Get(
						context.Background(),
						client.ObjectKeyFromObject(&work), &work),
					).To(Succeed())
				})

				It("sets the right status and conditions", func() {
					Expect(work.Status.WorkPlacements).To(Equal(2))
					Expect(work.Status.WorkPlacementsCreated).To(Equal(1))

					readyCond := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
					Expect(readyCond).ToNot(BeNil())
					Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
					Expect(readyCond.Reason).To(Equal("UnscheduledWorkloads"))
					Expect(readyCond.Message).To(Equal("Pending"))

					scheduleSucceededCond := apimeta.FindStatusCondition(work.Status.Conditions, "ScheduleSucceeded")
					Expect(scheduleSucceededCond).ToNot(BeNil())
					Expect(scheduleSucceededCond.Status).To(Equal(metav1.ConditionFalse))
					Expect(scheduleSucceededCond.Reason).To(Equal("UnscheduledWorkloads"))
					Expect(scheduleSucceededCond.Message).To(ContainSubstring("No matching destination found for workloadGroups: [%s]", work.Spec.WorkloadGroups[0].ID))
				})

				It("sends the right events", func() {
					Eventually(schedulerRecorder.Events).Should(Receive(
						ContainSubstring(
							"waiting for a destination with labels for workloadGroup: %s",
							work.Spec.WorkloadGroups[0].ID,
						),
					))
				})
			})

			When("some workplacements are misplaced", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(context.Background(), &work)
					Expect(err).ToNot(HaveOccurred())
					Expect(fakeK8sClient.Get(
						context.Background(),
						client.ObjectKeyFromObject(&work), &work),
					).To(Succeed())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(2))

					// change the scheduling on the resource work from devDestination to prodDestination
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&work), &work)).To(Succeed())
					work.Spec.WorkloadGroups[0].DestinationSelectors[0] = schedulingFor(prodDestination)
					_, err = scheduler.ReconcileWork(context.Background(), &work)
					Expect(err).ToNot(HaveOccurred())
				})

				It("sets the right status and conditions", func() {
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&work), &work)).To(Succeed())
					Expect(work.Status.Conditions).To(HaveLen(2))
					work.Status.Conditions[0].LastTransitionTime = metav1.Time{}
					work.Status.Conditions[1].LastTransitionTime = metav1.Time{}
					Expect(work.Status.Conditions).To(ConsistOf(misplacedConditions(work.Spec.WorkloadGroups[0].ID)))
				})
			})
		})
	})
})

func misplacedWorkPlacementConditions() []metav1.Condition {
	return []metav1.Condition{
		{
			Message: "Target destination no longer matches destinationSelectors",
			Reason:  "DestinationSelectorMismatch",
			Type:    "ScheduleSucceeded",
			Status:  metav1.ConditionFalse},
		{
			Message: "Misplaced",
			Reason:  "Misplaced",
			Type:    "Ready",
			Status:  metav1.ConditionFalse},
	}
}

func newDestination(name string, labels map[string]string) v1alpha1.Destination {
	return v1alpha1.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}

func newWork(name string, isResource bool, scheduling ...v1alpha1.WorkloadGroupScheduling) v1alpha1.Work {
	namespace := "default"
	if !isResource {
		namespace = v1alpha1.SystemNamespace
	}
	w := &v1alpha1.Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name),
			Labels: map[string]string{
				"kratix.io/work":          name,
				"kratix.io/pipeline-name": fmt.Sprintf("workflow-%s", uuid.New().String()[0:8]),
			},
			Annotations: map[string]string{
				"kratix.io/some-annotation": "some-value",
			},
		},
		Spec: v1alpha1.WorkSpec{
			PromiseName: "promise",
			WorkloadGroups: []v1alpha1.WorkloadGroup{
				{
					Workloads: []v1alpha1.Workload{
						{Content: "key: value"},
					},
					Directory:            ".",
					ID:                   hash.ComputeHash("."),
					DestinationSelectors: scheduling,
				},
			},
		},
	}
	if isResource {
		w.Spec.ResourceName = "resource"
	}

	Expect(fakeK8sClient.Create(context.Background(), w)).To(Succeed())
	Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(w), w)).To(Succeed())

	return *w
}

func newWorkWithTwoWorkloadGroups(name string, isResource bool, promiseScheduling, directoryOverrideScheduling v1alpha1.WorkloadGroupScheduling) v1alpha1.Work {
	namespace := "default"
	if !isResource {
		namespace = v1alpha1.SystemNamespace
	}

	newFakeCompressedContent, err := compression.CompressContent([]byte(string("key: value")))
	Expect(err).ToNot(HaveOccurred())
	additionalFakeCompressedContent, err := compression.CompressContent([]byte(string("foo: bar")))
	Expect(err).ToNot(HaveOccurred())

	w := &v1alpha1.Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				"kratix.io/annotation-key": "annotation-value",
			},
		},
		Spec: v1alpha1.WorkSpec{
			PromiseName: "promise",
			WorkloadGroups: []v1alpha1.WorkloadGroup{
				{
					Workloads: []v1alpha1.Workload{
						{Content: string(newFakeCompressedContent)},
					},
					Directory:            ".",
					ID:                   hash.ComputeHash("."),
					DestinationSelectors: []v1alpha1.WorkloadGroupScheduling{promiseScheduling},
				},
				{
					Workloads: []v1alpha1.Workload{
						{Content: string(additionalFakeCompressedContent)},
					},
					DestinationSelectors: []v1alpha1.WorkloadGroupScheduling{directoryOverrideScheduling},
					Directory:            "foo",
					ID:                   hash.ComputeHash("foo"),
				},
			},
		},
	}
	if isResource {
		w.Spec.ResourceName = "resource"
	}

	Expect(fakeK8sClient.Create(context.Background(), w)).To(Succeed())

	workWithDefaultFields := &v1alpha1.Work{}
	Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(w), workWithDefaultFields)).To(Succeed())

	return *workWithDefaultFields
}

func schedulingFor(destination v1alpha1.Destination) v1alpha1.WorkloadGroupScheduling {
	if len(destination.GetLabels()) == 0 {
		return v1alpha1.WorkloadGroupScheduling{}
	}
	return v1alpha1.WorkloadGroupScheduling{
		MatchLabels: destination.GetLabels(),
		Source:      "promise",
	}
}

func setupSchedulerMeterProvider() (*sdkmetric.ManualReader, func()) {
	reader := sdkmetric.NewManualReader()
	original := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	return reader, func() {
		otel.SetMeterProvider(original)
	}
}

func collectWorkPlacementOutcomeMetrics(ctx context.Context, reader *sdkmetric.ManualReader) map[string]metricdata.DataPoint[int64] {
	var rm metricdata.ResourceMetrics
	Expect(reader.Collect(ctx, &rm)).To(Succeed())

	collected := map[string]metricdata.DataPoint[int64]{}
	for _, scopeMetrics := range rm.ScopeMetrics {
		for _, metric := range scopeMetrics.Metrics {
			if metric.Name != telemetry.WorkPlacementOutcomesMetric {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			Expect(ok).To(BeTrue(), "expected an int64 Sum aggregation for outcomes")
			for _, dp := range sum.DataPoints {
				resultAttr, ok := dp.Attributes.Value(attribute.Key("result"))
				Expect(ok).To(BeTrue(), "expected result attribute on outcome metric")
				collected[resultAttr.AsString()] = dp
			}
		}
	}
	return collected
}

func outcomeAttributeValue(set attribute.Set, key string) string {
	if val, ok := set.Value(attribute.Key(key)); ok {
		return val.AsString()
	}
	return ""
}

func misplacedConditions(id string) []metav1.Condition {
	return []metav1.Condition{
		{
			Type:    "ScheduleSucceeded",
			Status:  metav1.ConditionFalse,
			Message: fmt.Sprintf("Target destination no longer matches destinationSelectors for workloadGroups: [%s]", id),
			Reason:  "DestinationSelectorMismatch",
		},
		{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Message: "Misplaced",
			Reason:  "Misplaced",
		},
	}
}
