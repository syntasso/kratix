package controllers_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	. "github.com/syntasso/kratix/api/v1alpha1"
	. "github.com/syntasso/kratix/controllers"
	"github.com/syntasso/kratix/lib/hash"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var rootDirectoryWorkloadGroupID = hash.ComputeHash(".")

var _ = Describe("Controllers/Scheduler", func() {

	var devDestination, devDestination2, pciDestination, prodDestination Destination
	var dependencyWork, dependencyWorkForProd, dependencyWorkForDev, resourceWork, resourceWorkWithMultipleGroup Work
	var workPlacements WorkPlacementList
	var scheduler *Scheduler

	BeforeEach(func() {
		devDestination = newDestination("dev-1", map[string]string{"environment": "dev"})
		devDestination2 = newDestination("dev-2", map[string]string{"environment": "dev"})
		pciDestination = newDestination("pci", map[string]string{"pci": "true"})
		prodDestination = newDestination("prod", map[string]string{"environment": "prod"})

		dependencyWork = newWork(k8sClient, "work-name", DependencyReplicas)
		dependencyWorkForProd = newWork(k8sClient, "prod-work-name", DependencyReplicas, schedulingFor(prodDestination))
		dependencyWorkForDev = newWork(k8sClient, "dev-work-name", DependencyReplicas, schedulingFor(devDestination))

		resourceWork = newWork(k8sClient, "rr-work-name", ResourceRequestReplicas, schedulingFor(devDestination))
		resourceWorkWithMultipleGroup = newWorkWithTwoWorkloadGroups("rr-work-name-with-two-groups", ResourceRequestReplicas, schedulingFor(devDestination), map[string]string{"pci": "true"})

		scheduler = &Scheduler{
			Client: k8sClient,
			Log:    ctrl.Log.WithName("controllers").WithName("Scheduler"),
		}

		Expect(k8sClient.Create(context.Background(), &devDestination)).To(Succeed())
		Expect(k8sClient.Create(context.Background(), &devDestination2)).To(Succeed())
		Expect(k8sClient.Create(context.Background(), &pciDestination)).To(Succeed())
		Expect(k8sClient.Create(context.Background(), &prodDestination)).To(Succeed())
	})

	AfterEach(func() {
		cleanEnvironment()
	})

	Describe("#ReconcileDestination", func() {
		var devDestination3 Destination
		BeforeEach(func() {
			// register new destination dev
			devDestination3 = newDestination("dev3", map[string]string{"environment": "dev"})
			Expect(k8sClient.Create(context.Background(), &devDestination3)).To(Succeed())
			scheduler.ReconcileDestination()
		})

		When("A new destination is added", func() {
			It("schedules Works with matching labels to the new destination that match the labels", func() {
				workplacementList := WorkPlacementList{}
				lo := &client.ListOptions{}
				selector, err := labels.Parse(labels.FormatLabels(map[string]string{"kratix.io/work": "dev-work-name"}))
				Expect(err).NotTo(HaveOccurred())

				lo.LabelSelector = selector
				Expect(k8sClient.List(context.Background(), &workplacementList, lo)).To(Succeed())
				for _, wp := range workplacementList.Items {
					fmt.Println(wp.Name)
				}
				Expect(workplacementList.Items).To(HaveLen(3))
				Expect(workplacementList.Items[0].Name).To(HavePrefix("dev-work-name.dev-1"))
				Expect(workplacementList.Items[0].Namespace).To(Equal(KratixSystemNamespace))
				Expect(workplacementList.Items[0].Spec.TargetDestinationName).To(Equal(devDestination.Name))
				Expect(workplacementList.Items[0].Spec.Workloads).To(Equal(dependencyWorkForDev.Spec.WorkloadGroups[0].Workloads))

				Expect(workplacementList.Items[1].Name).To(HavePrefix("dev-work-name.dev-2"))
				Expect(workplacementList.Items[1].Namespace).To(Equal(KratixSystemNamespace))
				Expect(workplacementList.Items[1].Spec.TargetDestinationName).To(Equal(devDestination2.Name))
				Expect(workplacementList.Items[1].Spec.Workloads).To(Equal(dependencyWorkForDev.Spec.WorkloadGroups[0].Workloads))

				Expect(workplacementList.Items[2].Name).To(HavePrefix("dev-work-name.dev3"))
				Expect(workplacementList.Items[2].Namespace).To(Equal(KratixSystemNamespace))
				Expect(workplacementList.Items[2].Spec.TargetDestinationName).To(Equal(devDestination3.Name))
				Expect(workplacementList.Items[2].Spec.Workloads).To(Equal(dependencyWorkForDev.Spec.WorkloadGroups[0].Workloads))
			})
		})
	})

	Describe("#ReconcileWork", func() {
		Describe("Scheduling Resources (replicas=1)", func() {
			It("creates a WorkPlacement for a given Work", func() {
				_, err := scheduler.ReconcileWork(&resourceWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

				Expect(workPlacements.Items).To(HaveLen(1))
				workPlacement := workPlacements.Items[0]
				Expect(workPlacement.Namespace).To(Equal("default"))
				Expect(workPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name"))
				Expect(workPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(rootDirectoryWorkloadGroupID))
				Expect(workPlacement.Name).To(Equal("rr-work-name." + workPlacement.Spec.TargetDestinationName + "-" + rootDirectoryWorkloadGroupID[0:5]))
				Expect(workPlacement.Spec.Workloads).To(Equal(resourceWork.Spec.WorkloadGroups[0].Workloads))
				Expect(workPlacement.Spec.TargetDestinationName).To(MatchRegexp("prod|dev\\-\\d"))
				Expect(workPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
				Expect(workPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
				Expect(workPlacement.Spec.PromiseName).To(Equal("promise"))
				Expect(workPlacement.Spec.ResourceName).To(Equal("resource"))

				Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork))
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

			It("updates workplacements for existing works", func() {
				_, err := scheduler.ReconcileWork(&resourceWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

				Expect(workPlacements.Items).To(HaveLen(1))
				workPlacement := workPlacements.Items[0]
				Expect(workPlacement.Spec.Workloads).To(HaveLen(1))

				Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork))
				resourceWork.Spec.WorkloadGroups[0].Workloads = append(resourceWork.Spec.WorkloadGroups[0].Workloads, Workload{
					Content: "fake: content",
				})

				_, err = scheduler.ReconcileWork(&resourceWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

				Expect(workPlacements.Items).To(HaveLen(1))
				workPlacement = workPlacements.Items[0]
				Expect(workPlacement.Spec.Workloads).To(HaveLen(2))
				Expect(workPlacement.Spec.Workloads).To(ContainElement(Workload{
					Content: "fake: content",
				}))
			})
			When("An update removes a workload group", func() {
				It("removes the workplacements for the deleted workloadgroup", func() {
					_, err := scheduler.ReconcileWork(&resourceWork)
					Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork))
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					Expect(workPlacements.Items).To(HaveLen(1))
					workPlacement := workPlacements.Items[0]
					Expect(workPlacement.Spec.Workloads).To(HaveLen(1))

					preexistingWorkplacementName := workPlacement.Name

					resourceWork.Spec.WorkloadGroups[0].Directory = "foo"
					resourceWork.Spec.WorkloadGroups[0].ID = hash.ComputeHash("foo")
					resourceWork.Spec.WorkloadGroups[0].Workloads = append(resourceWork.Spec.WorkloadGroups[0].Workloads, Workload{
						Content: "fake: content",
					})

					//Remove finalizers so it can be deleted
					workPlacement.Finalizers = nil
					Expect(k8sClient.Update(context.Background(), &workPlacement)).To(Succeed())
					_, err = scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					Expect(workPlacements.Items).To(HaveLen(1))
					workPlacement = workPlacements.Items[0]
					Expect(workPlacement.Name).NotTo(Equal(preexistingWorkplacementName))
					Expect(workPlacement.Spec.Workloads).To(HaveLen(2))
					Expect(workPlacement.Spec.Workloads).To(ContainElement(Workload{
						Content: "fake: content",
					}))
				})
			})

			When("the Work needs to schedule to multiple destinations", func() {
				It("creates a WorkPlacement per workloadGroup", func() {
					_, err := scheduler.ReconcileWork(&resourceWorkWithMultipleGroup)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(2))

					var pciWorkPlacement, devOrProdWorkPlacement WorkPlacement
					for _, wp := range workPlacements.Items {
						if wp.Spec.TargetDestinationName == "pci" {
							pciWorkPlacement = wp
						} else {
							devOrProdWorkPlacement = wp
						}
					}

					workloadGroupID := rootDirectoryWorkloadGroupID
					Expect(devOrProdWorkPlacement.Namespace).To(Equal("default"))
					Expect(devOrProdWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name-with-two-groups"))
					Expect(devOrProdWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(workloadGroupID))
					Expect(devOrProdWorkPlacement.Name).To(Equal("rr-work-name-with-two-groups." + devOrProdWorkPlacement.Spec.TargetDestinationName + "-" + workloadGroupID[0:5]))
					Expect(devOrProdWorkPlacement.Spec.Workloads).To(Equal(resourceWorkWithMultipleGroup.Spec.WorkloadGroups[0].Workloads))
					Expect(devOrProdWorkPlacement.Spec.TargetDestinationName).To(MatchRegexp("prod|dev\\-\\d"))
					Expect(devOrProdWorkPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
					Expect(devOrProdWorkPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
					Expect(devOrProdWorkPlacement.Spec.PromiseName).To(Equal("promise"))
					Expect(devOrProdWorkPlacement.Spec.ResourceName).To(Equal("resource"))

					workloadGroupID = hash.ComputeHash("foo")
					Expect(pciWorkPlacement.Namespace).To(Equal("default"))
					Expect(pciWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name-with-two-groups"))
					Expect(pciWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(workloadGroupID))
					Expect(pciWorkPlacement.Name).To(Equal("rr-work-name-with-two-groups." + pciWorkPlacement.Spec.TargetDestinationName + "-" + workloadGroupID[0:5]))
					Expect(pciWorkPlacement.Spec.TargetDestinationName).To(Equal("pci"))
					Expect(pciWorkPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
					Expect(pciWorkPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
					Expect(pciWorkPlacement.Spec.Workloads).To(Equal(resourceWorkWithMultipleGroup.Spec.WorkloadGroups[1].Workloads))
					Expect(pciWorkPlacement.Spec.PromiseName).To(Equal("promise"))
					Expect(pciWorkPlacement.Spec.ResourceName).To(Equal("resource"))
				})

				When("one of the workloadgroups is unschedulable", func() {
					It("still schedules the remaining workload groups, and returns an error indicating what was unschedulable", func() {
						resourceWorkWithMultipleGroup.Spec.WorkloadGroups[0].DestinationSelectors[0].MatchLabels = map[string]string{"not": "scheduable"}
						unschedulable, err := scheduler.ReconcileWork(&resourceWorkWithMultipleGroup)
						Expect(err).NotTo(HaveOccurred())
						Expect(unschedulable).To(ConsistOf(hash.ComputeHash(".")))

						Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(1))

						pciWorkPlacement := workPlacements.Items[0]
						workloadGroupID := hash.ComputeHash("foo")
						Expect(pciWorkPlacement.Namespace).To(Equal("default"))
						Expect(pciWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name-with-two-groups"))
						Expect(pciWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(workloadGroupID))
						Expect(pciWorkPlacement.Name).To(Equal("rr-work-name-with-two-groups." + pciWorkPlacement.Spec.TargetDestinationName + "-" + workloadGroupID[0:5]))
						Expect(pciWorkPlacement.Spec.TargetDestinationName).To(Equal("pci"))
						Expect(pciWorkPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
						Expect(pciWorkPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
						Expect(pciWorkPlacement.Spec.Workloads).To(Equal(resourceWorkWithMultipleGroup.Spec.WorkloadGroups[1].Workloads))
						Expect(pciWorkPlacement.Spec.PromiseName).To(Equal("promise"))
						Expect(pciWorkPlacement.Spec.ResourceName).To(Equal("resource"))
					})
				})
			})

			When("the scheduling changes such that the workplacement is not on a correct destination", func() {
				var preUpdateDestination string
				BeforeEach(func() {
					_, err := scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())
					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))
					preUpdateDestination = workPlacements.Items[0].Spec.TargetDestinationName
				})

				It("does not reschedule the worklacement but does label the resource to indicate its misscheduled", func() {
					Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork))
					resourceWork.Spec.WorkloadGroups[0].DestinationSelectors[0] = schedulingFor(prodDestination)
					_, err := scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))
					workPlacement := workPlacements.Items[0]
					Expect(workPlacement.Spec.TargetDestinationName).To(Equal(preUpdateDestination))
					Expect(workPlacement.GetLabels()).To(HaveKeyWithValue("kratix.io/misscheduled", "true"))
					Expect(workPlacement.GetLabels()).To(HaveKeyWithValue("kratix.io/workload-group-id", rootDirectoryWorkloadGroupID))
					Expect(workPlacement.Status.Conditions).To(HaveLen(1))
					//ignore time for assertion
					workPlacement.Status.Conditions[0].LastTransitionTime = v1.Time{}
					Expect(workPlacement.Status.Conditions).To(ConsistOf(v1.Condition{
						Message: "Target destination no longer matches destinationSelectors",
						Reason:  "DestinationSelectorMismatch",
						Type:    "Misscheduled",
						Status:  v1.ConditionTrue,
					}))

					Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&resourceWork), &resourceWork))
					Expect(resourceWork.Status.Conditions).To(HaveLen(2))

					Expect(resourceWork.Status.Conditions[0].Type).To(Equal("Scheduled"))
					Expect(resourceWork.Status.Conditions[0].Status).To(Equal(v1.ConditionTrue))

					Expect(resourceWork.Status.Conditions[1].Type).To(Equal("Misscheduled"))
					Expect(resourceWork.Status.Conditions[1].Status).To(Equal(v1.ConditionTrue))
					Expect(resourceWork.Status.Conditions[1].Message).To(Equal("WorkloadGroup(s) not scheduled to correct Destination(s): [" + resourceWork.Spec.WorkloadGroups[0].ID + "]"))
					Expect(resourceWork.Status.Conditions[1].Reason).To(Equal("ScheduledToIncorrectDestinations"))
				})
			})

			When("scheduling is defined in the promise and workflows", func() {
				It("prioritises promise scheduling", func() {
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
					_, err := scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					Expect(workPlacements.Items).To(HaveLen(1))
					workPlacement := workPlacements.Items[0]
					Expect(workPlacement.Spec.TargetDestinationName).To(Equal("prod"))
				})
			})
		})

		Describe("Scheduling Dependencies (replicas=-1)", func() {
			When("the Work has no selector", func() {
				It("creates and updates Workplacement for all registered Destinations", func() {
					_, err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(len(workPlacements.Items)).To(Equal(4))
					for _, workPlacement := range workPlacements.Items {
						Expect(workPlacement.Spec.Workloads).To(ConsistOf(Workload{
							Content: "key: value",
						}))
					}

					Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork))
					dependencyWork.Spec.WorkloadGroups[0].Workloads = append(dependencyWork.Spec.WorkloadGroups[0].Workloads, Workload{
						Content: "fake: new-content",
					})

					_, err = scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					Expect(workPlacements.Items).To(HaveLen(4))
					for _, workPlacement := range workPlacements.Items {
						Expect(workPlacement.Spec.Workloads).To(ConsistOf(
							Workload{Content: "key: value"},
							Workload{Content: "fake: new-content"},
						))
					}
				})

				When("Work is updated to no longer match a previous destination", func() {
					It("marks the old workplacement as misscheduled but keeps it updated", func() {
						_, err := scheduler.ReconcileWork(&dependencyWork)
						Expect(err).ToNot(HaveOccurred())

						Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(len(workPlacements.Items)).To(Equal(4))
						for _, workPlacement := range workPlacements.Items {
							Expect(workPlacement.Spec.Workloads).To(ConsistOf(Workload{
								Content: "key: value",
							}))
						}

						Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork))
						dependencyWork.Spec.WorkloadGroups[0].DestinationSelectors = []v1alpha1.WorkloadGroupScheduling{
							{
								MatchLabels: map[string]string{"environment": "dev"},
							},
						}

						dependencyWork.Spec.WorkloadGroups[0].Workloads = append(dependencyWork.Spec.WorkloadGroups[0].Workloads, Workload{
							Content: "fake: new-content",
						})

						_, err = scheduler.ReconcileWork(&dependencyWork)
						Expect(err).ToNot(HaveOccurred())

						Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

						Expect(workPlacements.Items).To(HaveLen(4))
						Expect(workPlacements.Items[0].GetLabels()).NotTo(HaveKey("kratix.io/misscheduled"))
						Expect(workPlacements.Items[1].GetLabels()).NotTo(HaveKey("kratix.io/misscheduled"))
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

						for _, workPlacement := range workPlacements.Items {
							Expect(workPlacement.Spec.Workloads).To(ConsistOf(
								Workload{Content: "key: value"},
								Workload{Content: "fake: new-content"},
							))
						}
					})
				})

			})

			When("the Work matches a single Destination", func() {
				It("creates a single WorkPlacement", func() {
					_, err := scheduler.ReconcileWork(&dependencyWorkForProd)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))
					Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(Equal(prodDestination.Name))
					Expect(workPlacements.Items[0].ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForProd.Name))
				})
			})

			When("the Work matches multiple Destinations", func() {
				It("creates WorkPlacements for the Destinations with the label", func() {
					_, err := scheduler.ReconcileWork(&dependencyWorkForDev)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(2))

					devWorkPlacement := workPlacements.Items[0]
					Expect(devWorkPlacement.Spec.TargetDestinationName).To(Equal(devDestination.Name))
					Expect(devWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForDev.Name))

					devWorkPlacement2 := workPlacements.Items[1]
					Expect(devWorkPlacement2.Spec.TargetDestinationName).To(Equal(devDestination2.Name))
					Expect(devWorkPlacement2.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForDev.Name))
				})

				When("A workplacement is deleted", func() {
					It("gets recreated on next reconciliation", func() {
						//1st reconciliation
						_, err := scheduler.ReconcileWork(&dependencyWorkForDev)
						Expect(err).ToNot(HaveOccurred())

						Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(2))

						devWorkPlacement := workPlacements.Items[0]
						Expect(devWorkPlacement.Spec.TargetDestinationName).To(Equal(devDestination.Name))
						Expect(devWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForDev.Name))

						devWorkPlacement2 := workPlacements.Items[1]
						Expect(devWorkPlacement2.Spec.TargetDestinationName).To(Equal(devDestination2.Name))
						Expect(devWorkPlacement2.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForDev.Name))

						//Remove finalizer
						devWorkPlacement.Finalizers = nil
						Expect(k8sClient.Update(context.Background(), &devWorkPlacement)).To(Succeed())
						//manually delete workPlacement
						Expect(k8sClient.Delete(context.Background(), &devWorkPlacement)).To(Succeed())

						//re-reconcile
						Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWorkForDev), &dependencyWorkForDev))
						_, err = scheduler.ReconcileWork(&dependencyWorkForDev)
						Expect(err).ToNot(HaveOccurred())

						Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(2))

						devWorkPlacement = workPlacements.Items[0]
						Expect(devWorkPlacement.Spec.TargetDestinationName).To(Equal(devDestination.Name))
						Expect(devWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForDev.Name))

						devWorkPlacement2 = workPlacements.Items[1]
						Expect(devWorkPlacement2.Spec.TargetDestinationName).To(Equal(devDestination2.Name))
						Expect(devWorkPlacement2.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForDev.Name))
					})
				})
			})

			When("the Work selector matches no Destinations", func() {
				BeforeEach(func() {
					dependencyWork.Spec.WorkloadGroups[0].DestinationSelectors = []v1alpha1.WorkloadGroupScheduling{
						{
							MatchLabels: map[string]string{"environment": "staging"},
						},
					}
				})

				It("creates no workplacements", func() {
					unschedulable, err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).NotTo(HaveOccurred())
					Expect(unschedulable).To(ConsistOf(hash.ComputeHash(".")))

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(BeEmpty())

					Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(&dependencyWork), &dependencyWork))
					Expect(dependencyWork.Status.Conditions).To(HaveLen(2))
					Expect(dependencyWork.Status.Conditions[0].Type).To(Equal("Scheduled"))
					Expect(dependencyWork.Status.Conditions[0].Status).To(Equal(v1.ConditionFalse))
					Expect(dependencyWork.Status.Conditions[0].Message).To(Equal("No Destinations available work WorkloadGroups: [" + dependencyWork.Spec.WorkloadGroups[0].ID + "]"))
					Expect(dependencyWork.Status.Conditions[0].Reason).To(Equal("UnscheduledWorkloadGroups"))

					Expect(dependencyWork.Status.Conditions[1].Type).To(Equal("Misscheduled"))
					Expect(dependencyWork.Status.Conditions[1].Status).To(Equal(v1.ConditionFalse))
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

func newWork(k8sClient client.Client, name string, workType int, scheduling ...WorkloadGroupScheduling) Work {
	namespace := "default"
	if workType == DependencyReplicas {
		namespace = KratixSystemNamespace
	}
	work := &Work{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name),
		},
		Spec: WorkSpec{
			Replicas: workType,
			WorkloadCoreFields: WorkloadCoreFields{
				PromiseName:  "promise",
				ResourceName: "resource",
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
		},
	}

	Expect(k8sClient.Create(context.Background(), work)).To(Succeed())

	//sets object UID etc
	workWithDefaultFields := &Work{}
	Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(work), workWithDefaultFields)).To(Succeed())
	return *workWithDefaultFields
}

func newWorkWithTwoWorkloadGroups(name string, workType int, promiseScheduling WorkloadGroupScheduling, directoryOverrideScheduling map[string]string) Work {
	namespace := "default"
	if workType == DependencyReplicas {
		namespace = KratixSystemNamespace
	}

	work := &Work{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: WorkSpec{
			Replicas: workType,
			WorkloadCoreFields: WorkloadCoreFields{
				PromiseName:  "promise",
				ResourceName: "resource",
				WorkloadGroups: []WorkloadGroup{
					{
						Workloads: []Workload{
							{Content: "key: value"},
						},
						Directory:            ".",
						ID:                   hash.ComputeHash("."),
						DestinationSelectors: []WorkloadGroupScheduling{promiseScheduling},
					},
					{
						Workloads: []Workload{
							{Content: "foo: bar"},
						},
						DestinationSelectors: []WorkloadGroupScheduling{{MatchLabels: directoryOverrideScheduling}},
						Directory:            "foo",
						ID:                   hash.ComputeHash("foo"),
					},
				},
			},
		},
	}

	Expect(k8sClient.Create(context.Background(), work)).To(Succeed())

	workWithDefaultFields := &Work{}
	Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(work), workWithDefaultFields)).To(Succeed())
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
