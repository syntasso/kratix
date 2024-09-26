package controllers_test

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	. "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/hash"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/syntasso/kratix/controllers"
)

var _ = Describe("Controllers/Scheduler", func() {
	var devDestination, devDestination2, pciDestination, prodDestination, strictDestination Destination
	var workPlacements WorkPlacementList
	var scheduler *Scheduler
	var fakeCompressedContent []byte

	BeforeEach(func() {
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

		scheduler = &Scheduler{
			Client: fakeK8sClient,
			Log:    ctrl.Log.WithName("controllers").WithName("Scheduler"),
		}
	})

	Describe("#ReconcileWork", func() {
		Describe("Scheduling Resources", func() {
			var resourceWork, resourceWorkWithMultipleGroups Work

			BeforeEach(func() {
				resourceWork = newWork("rr-work-name", true, schedulingFor(devDestination))
				resourceWorkWithMultipleGroups = newWorkWithTwoWorkloadGroups("rr-work-name-with-two-groups", true, schedulingFor(devDestination), schedulingFor(pciDestination))
			})

			When("a resource Work with scheduling is reconciled", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(&resourceWork)
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
				})

				It("sets the scheduling conditions on the Work", func() {
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork))
					Expect(resourceWork.Status.Conditions).To(HaveLen(2))

					Expect(resourceWork.Status.Conditions[0].Type).To(Equal("Scheduled"))
					Expect(resourceWork.Status.Conditions[0].Status).To(Equal(v1.ConditionTrue))
					Expect(resourceWork.Status.Conditions[0].Message).To(Equal("All WorkloadGroups scheduled to Destination(s)"))
					Expect(resourceWork.Status.Conditions[0].Reason).To(Equal("ScheduledToDestinations"))

					Expect(resourceWork.Status.Conditions[1].Type).To(Equal("Misscheduled"))
					Expect(resourceWork.Status.Conditions[1].Status).To(Equal(v1.ConditionFalse))
					Expect(resourceWork.Status.Conditions[1].Message).To(Equal("WorkGroups that have been scheduled are at the correct Destination(s)"))
					Expect(resourceWork.Status.Conditions[1].Reason).To(Equal("ScheduledToCorrectDestinations"))
				})
			})

			When("a resource Work with scheduling is reconciled twice", func() {
				var workPlacement WorkPlacement

				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())

					latestWork := &Work{}
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), latestWork)).To(Succeed())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))
					workPlacement = workPlacements.Items[0]

					_, err = scheduler.ReconcileWork(latestWork)
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
				})
			})

			When("a resource Work with scheduling is reconciled with an updated WorkloadGroup", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(&resourceWork)
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
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork))
					resourceWork.Spec.WorkloadGroups[0].Workloads = append(resourceWork.Spec.WorkloadGroups[0].Workloads, Workload{
						Content: string(fakeCompressedContent),
					})

					_, err = scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					// expect the new Workload to be added to the existing WorkPlacement
					workPlacement = workPlacements.Items[0]
					Expect(workPlacement.Spec.Workloads).To(HaveLen(2))
					Expect(workPlacement.Spec.Workloads).To(ContainElement(Workload{
						Content: string(fakeCompressedContent),
					}))

					newResourceVersion, err := strconv.Atoi(workPlacement.ResourceVersion)
					Expect(err).ToNot(HaveOccurred())
					Expect(newResourceVersion).To(BeNumerically(">", previousResourceVersion))
				})
			})

			When("an update to the resource Work deletes the only WorkloadGroup", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					// set WorkloadGroups to an empty list and reconcile again
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork)).To(Succeed())
					resourceWork.Spec.WorkloadGroups = []WorkloadGroup{}
					_, err = scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())
				})

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
					Expect(workPlacements.Items).To(HaveLen(0))
				})
			})

			When("an update to the resource Work adds a new WorkloadGroup", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					// append a new WorkloadGroup to the Work and reconcile again
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork)).To(Succeed())
					resourceWork.Spec.WorkloadGroups = append(resourceWork.Spec.WorkloadGroups, WorkloadGroup{
						Directory: "foo",
						ID:        hash.ComputeHash("foo"),
						Workloads: []Workload{
							{
								Content: string(fakeCompressedContent),
							},
						},
						DestinationSelectors: []WorkloadGroupScheduling{schedulingFor(devDestination)},
					})
					_, err = scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("creates a WorkPlacement for the new WorkloadGroup", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					// expect the new WorkloadGroup to create a new WorkPlacement
					Expect(workPlacements.Items).To(HaveLen(2))

					var newWorkPlacement WorkPlacement
					for _, wp := range workPlacements.Items {
						if wp.Spec.ID == hash.ComputeHash("foo") {
							newWorkPlacement = wp
						}
					}
					Expect(newWorkPlacement.Namespace).To(Equal("default"))
					Expect(newWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name"))
					Expect(newWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(hash.ComputeHash("foo")))
					Expect(newWorkPlacement.Name).To(Equal("rr-work-name." + newWorkPlacement.Spec.TargetDestinationName + "-" + hash.ComputeHash("foo")[0:5]))
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
					_, err := scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					// replace the WorkloadGroup in the Work and reconcile again
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork)).To(Succeed())
					resourceWork.Spec.WorkloadGroups = []WorkloadGroup{
						{
							Directory: "foo",
							ID:        hash.ComputeHash("foo"),
							Workloads: []Workload{
								{
									Content: string(fakeCompressedContent),
								},
							},
						},
					}
					_, err = scheduler.ReconcileWork(&resourceWork)
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
						_, err := scheduler.ReconcileWork(&resourceWorkWithMultipleGroups)
						Expect(err).ToNot(HaveOccurred())
					})

					It("creates a WorkPlacement per WorkloadGroup", func() {
						Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(2))

						var pciWorkPlacement, devOrProdWorkPlacement WorkPlacement
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
					})
				})

				When("an update to the resource Work deletes one of the WorkloadGroups", func() {
					var pciWorkPlacement WorkPlacement

					BeforeEach(func() {
						_, err := scheduler.ReconcileWork(&resourceWorkWithMultipleGroups)
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

						_, err = scheduler.ReconcileWork(&resourceWorkWithMultipleGroups)
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

						unschedulable, err = scheduler.ReconcileWork(&resourceWorkWithMultipleGroups)
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
					})

					It("returns an error indicating what was unschedulable", func() {
						Expect(unschedulable).To(ConsistOf(hash.ComputeHash(".")))
					})
				})
			})

			When("the scheduling changes such that the WorkPlacement is not on a correct Destination", func() {
				var preUpdateDestination string

				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					preUpdateDestination = workPlacements.Items[0].Spec.TargetDestinationName

					// change the scheduling on the resource work from devDestination to prodDestination
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork))
					resourceWork.Spec.WorkloadGroups[0].DestinationSelectors[0] = schedulingFor(prodDestination)
					_, err = scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("does not reschedule the WorkPlacement", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))

					workPlacement := workPlacements.Items[0]
					Expect(workPlacement.Spec.TargetDestinationName).To(Equal(preUpdateDestination))
					Expect(workPlacement.GetLabels()).To(HaveKeyWithValue("kratix.io/misscheduled", "true"))
					Expect(workPlacement.GetLabels()).To(HaveKeyWithValue("kratix.io/workload-group-id", hash.ComputeHash(".")))
					Expect(workPlacement.Status.Conditions).To(HaveLen(1))
					//ignore time for assertion
					workPlacement.Status.Conditions[0].LastTransitionTime = v1.Time{}
					Expect(workPlacement.Status.Conditions).To(ConsistOf(v1.Condition{
						Message: "Target destination no longer matches destinationSelectors",
						Reason:  "DestinationSelectorMismatch",
						Type:    "Misscheduled",
						Status:  v1.ConditionTrue,
					}))
				})

				It("labels the resource Work to indicate it's misscheduled", func() {
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork))
					Expect(resourceWork.Status.Conditions).To(HaveLen(2))

					Expect(resourceWork.Status.Conditions[0].Type).To(Equal("Scheduled"))
					Expect(resourceWork.Status.Conditions[0].Status).To(Equal(v1.ConditionTrue))

					Expect(resourceWork.Status.Conditions[1].Type).To(Equal("Misscheduled"))
					Expect(resourceWork.Status.Conditions[1].Status).To(Equal(v1.ConditionTrue))
					Expect(resourceWork.Status.Conditions[1].Message).To(Equal("WorkloadGroup(s) not scheduled to correct Destination(s): [" + resourceWork.Spec.WorkloadGroups[0].ID + "]"))
					Expect(resourceWork.Status.Conditions[1].Reason).To(Equal("ScheduledToIncorrectDestinations"))
				})
			})

			When("scheduling is defined in the Promise, Promise Workflow and Resource Workflow", func() {
				BeforeEach(func() {
					resourceWork.Spec.WorkloadGroups[0].DestinationSelectors = []WorkloadGroupScheduling{
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
					_, err := scheduler.ReconcileWork(&resourceWork)
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
			var dependencyWork, dependencyWorkForDev, dependencyWorkForProd Work

			BeforeEach(func() {
				dependencyWork = newWork("work-name", false)
				dependencyWorkForDev = newWork("dev-work-name", false, schedulingFor(devDestination))
				dependencyWorkForProd = newWork("prod-work-name", false, schedulingFor(prodDestination))
			})

			When("the Work matches a single Destination", func() {
				When("the Work is reconciled", func() {
					BeforeEach(func() {
						_, err := scheduler.ReconcileWork(&dependencyWorkForProd)
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
					})
				})

				When("the WorkPlacement is deleted", func() {
					BeforeEach(func() {
						_, err := scheduler.ReconcileWork(&dependencyWorkForProd)
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
						Expect(workPlacements.Items).To(HaveLen(0))
					})

					It("gets recreated on next reconciliation", func() {
						// re-reconcile the Work
						Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWorkForProd), &dependencyWorkForProd))
						_, err := scheduler.ReconcileWork(&dependencyWorkForProd)
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
						_, err := scheduler.ReconcileWork(&dependencyWorkForDev)
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
						_, err := scheduler.ReconcileWork(&dependencyWorkForDev)
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
						Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWorkForDev), &dependencyWorkForDev))
						_, err := scheduler.ReconcileWork(&dependencyWorkForDev)
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
					unschedulable, err = scheduler.ReconcileWork(&dependencyWork)
					Expect(err).NotTo(HaveOccurred())
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork))
				})

				It("creates no workplacements", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(BeEmpty())
				})

				It("marks the Work as unscheduled", func() {
					Expect(dependencyWork.Status.Conditions).To(HaveLen(2))
					Expect(dependencyWork.Status.Conditions[0].Type).To(Equal("Scheduled"))
					Expect(dependencyWork.Status.Conditions[0].Status).To(Equal(v1.ConditionFalse))
					Expect(dependencyWork.Status.Conditions[0].Message).To(Equal("No Destinations available work WorkloadGroups: [" + dependencyWork.Spec.WorkloadGroups[0].ID + "]"))
					Expect(dependencyWork.Status.Conditions[0].Reason).To(Equal("UnscheduledWorkloadGroups"))
				})

				It("does not mark the Work as misscheduled", func() {
					Expect(dependencyWork.Status.Conditions[1].Type).To(Equal("Misscheduled"))
					Expect(dependencyWork.Status.Conditions[1].Status).To(Equal(v1.ConditionFalse))
				})

				It("returns an error indicating what was unschedulable", func() {
					Expect(unschedulable).To(ConsistOf(hash.ComputeHash(".")))
				})
			})

			When("the Work has no selector", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())
				})

				It("creates WorkPlacements for all non-strict label matching registered Destinations", func() {
					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(len(workPlacements.Items)).To(Equal(4))
					for _, workPlacement := range workPlacements.Items {
						Expect(workPlacement.Spec.Workloads).To(ConsistOf(Workload{
							Content: "key: value",
						}))
						Expect(workPlacement.Spec.TargetDestinationName).ToNot(Equal(strictDestination.Name))
					}
				})

				It("updates WorkPlacements for all registered Destinations", func() {
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork))
					dependencyWork.Spec.WorkloadGroups[0].Workloads = append(dependencyWork.Spec.WorkloadGroups[0].Workloads, Workload{
						Content: "fake: new-content",
					})

					_, err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(4))
					for _, workPlacement := range workPlacements.Items {
						Expect(workPlacement.Spec.Workloads).To(ConsistOf(
							Workload{Content: "key: value"},
							Workload{Content: "fake: new-content"},
						))
					}
				})
			})

			When("the Work is updated to no longer match a previous Destination", func() {
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					// add scheduling for the devDestination, so that the WorkPlacements
					// for prod and pci are now misscheduled
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork))
					dependencyWork.Spec.WorkloadGroups[0].DestinationSelectors = []v1alpha1.WorkloadGroupScheduling{
						schedulingFor(devDestination),
					}

					_, err = scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(4))

					// ensure the WorkPlacements are sorted by name, so the tests aren't
					// flaky by varying the order they are returned from the API!
					sort.Slice(workPlacements.Items, func(i, j int) bool {
						return workPlacements.Items[i].Spec.TargetDestinationName < workPlacements.Items[j].Spec.TargetDestinationName
					})
				})

				It("marks existing WorkPlacements which no longer match as misscheduled", func() {
					// the two dev WorkPlacements should not be marked as misscheduled
					Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(Equal("dev-1"))
					Expect(workPlacements.Items[0].GetLabels()).NotTo(HaveKey("kratix.io/misscheduled"))

					Expect(workPlacements.Items[1].Spec.TargetDestinationName).To(Equal("dev-2"))
					Expect(workPlacements.Items[1].GetLabels()).NotTo(HaveKey("kratix.io/misscheduled"))

					// the pci and prod WorkPlacements should be marked as misscheduled

					Expect(workPlacements.Items[2].Spec.TargetDestinationName).To(Equal("pci"))
					Expect(workPlacements.Items[2].GetLabels()).To(HaveKeyWithValue("kratix.io/misscheduled", "true"))
					Expect(workPlacements.Items[2].Status.Conditions).To(HaveLen(1))
					//ignore time for assertion
					workPlacements.Items[2].Status.Conditions[0].LastTransitionTime = v1.Time{}
					Expect(workPlacements.Items[2].Status.Conditions).To(ConsistOf(v1.Condition{
						Message: "Target destination no longer matches destinationSelectors",
						Reason:  "DestinationSelectorMismatch",
						Type:    "Misscheduled",
						Status:  v1.ConditionTrue,
					}))

					Expect(workPlacements.Items[3].Spec.TargetDestinationName).To(Equal("prod"))
					Expect(workPlacements.Items[3].GetLabels()).To(HaveKeyWithValue("kratix.io/misscheduled", "true"))
					Expect(workPlacements.Items[3].Status.Conditions).To(HaveLen(1))
					//ignore time for assertion
					workPlacements.Items[3].Status.Conditions[0].LastTransitionTime = v1.Time{}
					Expect(workPlacements.Items[3].Status.Conditions).To(ConsistOf(v1.Condition{
						Message: "Target destination no longer matches destinationSelectors",
						Reason:  "DestinationSelectorMismatch",
						Type:    "Misscheduled",
						Status:  v1.ConditionTrue,
					}))
				})

				It("keeps the misscheduled WorkPlacements updated", func() {
					// update the Work's WorkloadGroup with an extra Workload
					Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork))
					dependencyWork.Spec.WorkloadGroups[0].Workloads = append(dependencyWork.Spec.WorkloadGroups[0].Workloads, Workload{
						Content: "fake: new-content",
					})

					_, err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(fakeK8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(4))

					// check that all WorkPlacements have been updated, including the two
					// misscheduled ones
					for _, workPlacement := range workPlacements.Items {
						Expect(workPlacement.Spec.Workloads).To(ConsistOf(
							Workload{Content: "key: value"},
							Workload{Content: "fake: new-content"},
						))
					}
				})
			})
		})
	})
})

func newDestination(name string, labels map[string]string) Destination {
	return Destination{
		ObjectMeta: v1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}

func newWork(name string, isResource bool, scheduling ...WorkloadGroupScheduling) Work {
	namespace := "default"
	if !isResource {
		namespace = SystemNamespace
	}
	w := &Work{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name),
			Labels: map[string]string{
				"kratix.io/work":          name,
				"kratix.io/pipeline-name": fmt.Sprintf("workflow-%s", uuid.New().String()[0:8]),
			},
		},
		Spec: WorkSpec{
			PromiseName: "promise",
			WorkloadGroups: []WorkloadGroup{
				{
					Workloads: []Workload{
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

	//sets the APIVersion, Kind, and ResourceVersion
	workWithDefaultFields := &Work{}
	Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(w), workWithDefaultFields)).To(Succeed())

	return *workWithDefaultFields
}

func newWorkWithTwoWorkloadGroups(name string, isResource bool, promiseScheduling, directoryOverrideScheduling WorkloadGroupScheduling) Work {
	namespace := "default"
	if !isResource {
		namespace = SystemNamespace
	}

	newFakeCompressedContent, err := compression.CompressContent([]byte(string("key: value")))
	Expect(err).ToNot(HaveOccurred())
	additionalFakeCompressedContent, err := compression.CompressContent([]byte(string("foo: bar")))
	Expect(err).ToNot(HaveOccurred())

	w := &Work{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: WorkSpec{
			PromiseName: "promise",
			WorkloadGroups: []WorkloadGroup{
				{
					Workloads: []Workload{
						{Content: string(newFakeCompressedContent)},
					},
					Directory:            ".",
					ID:                   hash.ComputeHash("."),
					DestinationSelectors: []WorkloadGroupScheduling{promiseScheduling},
				},
				{
					Workloads: []Workload{
						{Content: string(additionalFakeCompressedContent)},
					},
					DestinationSelectors: []WorkloadGroupScheduling{directoryOverrideScheduling},
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

	workWithDefaultFields := &Work{}
	Expect(fakeK8sClient.Get(context.Background(), client.ObjectKeyFromObject(w), workWithDefaultFields)).To(Succeed())

	return *workWithDefaultFields
}

func schedulingFor(destination Destination) WorkloadGroupScheduling {
	if len(destination.GetLabels()) == 0 {
		return WorkloadGroupScheduling{}
	}
	return WorkloadGroupScheduling{
		MatchLabels: destination.GetLabels(),
		Source:      "promise",
	}
}
