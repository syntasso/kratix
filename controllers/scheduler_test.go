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

		dependencyWork = newWork("work-name", DependencyReplicas)
		dependencyWorkForProd = newWork("prod-work-name", DependencyReplicas, schedulingFor(prodDestination))
		dependencyWorkForDev = newWork("dev-work-name", DependencyReplicas, schedulingFor(devDestination))

		resourceWork = newWork("rr-work-name", ResourceRequestReplicas, schedulingFor(devDestination))
		resourceWorkWithMultipleGroup = newWorkWithTwoWorkloadGroups("rr-work-name", ResourceRequestReplicas, schedulingFor(devDestination), map[string]string{"pci": "true"})

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
			//creating 1 workplacementList
			Expect(k8sClient.Create(context.Background(), &dependencyWorkForProd)).To(Succeed())
			//creating 3 workpalcements
			Expect(k8sClient.Create(context.Background(), &dependencyWorkForDev)).To(Succeed())
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
				err := scheduler.ReconcileWork(&resourceWork)
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
			})

			It("updates workplacements for existing works", func() {
				err := scheduler.ReconcileWork(&resourceWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

				Expect(workPlacements.Items).To(HaveLen(1))
				workPlacement := workPlacements.Items[0]
				Expect(workPlacement.Spec.Workloads).To(HaveLen(1))

				resourceWork.Spec.WorkloadGroups[0].Workloads = append(resourceWork.Spec.WorkloadGroups[0].Workloads, Workload{
					Content: "fake: content",
				})
				err = scheduler.ReconcileWork(&resourceWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

				Expect(workPlacements.Items).To(HaveLen(1))
				workPlacement = workPlacements.Items[0]
				Expect(workPlacement.Spec.Workloads).To(HaveLen(2))
				Expect(workPlacement.Spec.Workloads).To(ContainElement(Workload{
					Content: "fake: content",
				}))
			})

			When("the Work needs to schedule to multiple destinations", func() {
				It("creates a WorkPlacement per workloadGroup", func() {
					err := scheduler.ReconcileWork(&resourceWorkWithMultipleGroup)
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
					Expect(devOrProdWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name"))
					Expect(devOrProdWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(workloadGroupID))
					Expect(devOrProdWorkPlacement.Name).To(Equal("rr-work-name." + devOrProdWorkPlacement.Spec.TargetDestinationName + "-" + workloadGroupID[0:5]))
					Expect(devOrProdWorkPlacement.Spec.Workloads).To(Equal(resourceWorkWithMultipleGroup.Spec.WorkloadGroups[0].Workloads))
					Expect(devOrProdWorkPlacement.Spec.TargetDestinationName).To(MatchRegexp("prod|dev\\-\\d"))
					Expect(devOrProdWorkPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
					Expect(devOrProdWorkPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
					Expect(devOrProdWorkPlacement.Spec.PromiseName).To(Equal("promise"))
					Expect(devOrProdWorkPlacement.Spec.ResourceName).To(Equal("resource"))

					//TODO deterministic ordering? pci sucks
					workloadGroupID = hash.ComputeHash("foo")
					Expect(pciWorkPlacement.Namespace).To(Equal("default"))
					Expect(pciWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name"))
					Expect(pciWorkPlacement.ObjectMeta.Labels["kratix.io/workload-group-id"]).To(Equal(workloadGroupID))
					Expect(pciWorkPlacement.Name).To(Equal("rr-work-name." + pciWorkPlacement.Spec.TargetDestinationName + "-" + workloadGroupID[0:5]))
					Expect(pciWorkPlacement.Spec.TargetDestinationName).To(Equal("pci"))
					Expect(pciWorkPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
					Expect(pciWorkPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
					Expect(pciWorkPlacement.Spec.Workloads).To(Equal(resourceWorkWithMultipleGroup.Spec.WorkloadGroups[1].Workloads))
					Expect(pciWorkPlacement.Spec.PromiseName).To(Equal("promise"))
					Expect(pciWorkPlacement.Spec.ResourceName).To(Equal("resource"))
				})
			})

			When("the scheduling changes such that the workplacement is not on a correct destination", func() {
				var preUpdateDestination string
				BeforeEach(func() {
					err := scheduler.ReconcileWork(&resourceWork)
					Expect(err).ToNot(HaveOccurred())
					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))
					preUpdateDestination = workPlacements.Items[0].Spec.TargetDestinationName
				})

				It("does not reschedule the worklacement but does label the resource to indicate its misscheduled", func() {
					resourceWork.Spec.DestinationSelectors = schedulingFor(prodDestination)
					err := scheduler.ReconcileWork(&resourceWork)
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
						Status:  "True",
					}))
				})
			})
		})

		Describe("Scheduling Dependencies (replicas=-1)", func() {
			When("the Work has no selector", func() {
				It("creates and updates Workplacement for all registered Destinations", func() {
					err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(len(workPlacements.Items)).To(Equal(4))
					for _, workPlacement := range workPlacements.Items {
						Expect(workPlacement.Spec.Workloads).To(ConsistOf(Workload{
							Content: "key: value",
						}))
					}

					dependencyWork.Spec.WorkloadGroups[0].Workloads = append(dependencyWork.Spec.WorkloadGroups[0].Workloads, Workload{
						Content: "fake: new-content",
					})

					err = scheduler.ReconcileWork(&dependencyWork)
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
						err := scheduler.ReconcileWork(&dependencyWork)
						Expect(err).ToNot(HaveOccurred())

						Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(len(workPlacements.Items)).To(Equal(4))
						for _, workPlacement := range workPlacements.Items {
							Expect(workPlacement.Spec.Workloads).To(ConsistOf(Workload{
								Content: "key: value",
							}))
						}

						dependencyWork.Spec.DestinationSelectors = v1alpha1.WorkScheduling{
							Promise: []Selector{
								{
									MatchLabels: map[string]string{"environment": "dev"},
								},
							},
						}

						dependencyWork.Spec.WorkloadGroups[0].Workloads = append(dependencyWork.Spec.WorkloadGroups[0].Workloads, Workload{
							Content: "fake: new-content",
						})

						err = scheduler.ReconcileWork(&dependencyWork)
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
							Status:  "True",
						}))

						Expect(workPlacements.Items[3].GetLabels()).To(HaveKeyWithValue("kratix.io/misscheduled", "true"))
						Expect(workPlacements.Items[3].Status.Conditions).To(HaveLen(1))
						//ignore time for assertion
						workPlacements.Items[3].Status.Conditions[0].LastTransitionTime = v1.Time{}
						Expect(workPlacements.Items[3].Status.Conditions).To(ConsistOf(v1.Condition{
							Message: "Target destination no longer matches destinationSelectors",
							Reason:  "DestinationSelectorMismatch",
							Type:    "Misscheduled",
							Status:  "True",
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
					err := scheduler.ReconcileWork(&dependencyWorkForProd)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(1))
					Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(Equal(prodDestination.Name))
					Expect(workPlacements.Items[0].ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWorkForProd.Name))
				})
			})

			When("the Work matches multiple Destinations", func() {
				It("creates WorkPlacements for the Destinations with the label", func() {
					err := scheduler.ReconcileWork(&dependencyWorkForDev)
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
						err := scheduler.ReconcileWork(&dependencyWorkForDev)
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
						err = scheduler.ReconcileWork(&dependencyWorkForDev)
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
					dependencyWork.Spec.DestinationSelectors = WorkScheduling{
						Promise: []Selector{
							{
								MatchLabels: map[string]string{"environment": "staging"},
							},
						},
					}
				})

				It("creates no workplacements", func() {
					err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).To(MatchError("no Destinations can be selected for scheduling"))

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(BeEmpty())
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

func newWork(name string, workType int, scheduling ...WorkScheduling) Work {
	workScheduling := WorkScheduling{}
	if len(scheduling) > 0 {
		workScheduling = scheduling[0]
	}

	namespace := "default"
	if workType == DependencyReplicas {
		namespace = KratixSystemNamespace
	}
	return Work{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name),
		},
		Spec: WorkSpec{
			Replicas:             workType,
			DestinationSelectors: workScheduling,
			WorkloadCoreFields: WorkloadCoreFields{
				PromiseName:  "promise",
				ResourceName: "resource",
				WorkloadGroups: []WorkloadGroup{
					{
						Workloads: []Workload{
							{Content: "key: value"},
						},
						Directory: ".",
						ID:        hash.ComputeHash("."),
					},
				},
			},
		},
	}
}

func newWorkWithTwoWorkloadGroups(name string, workType int, promiseScheduling WorkScheduling, directoryOverrideScheduling map[string]string) Work {
	namespace := "default"
	if workType == DependencyReplicas {
		namespace = KratixSystemNamespace
	}
	return Work{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name),
		},
		Spec: WorkSpec{
			Replicas:             workType,
			DestinationSelectors: promiseScheduling,
			WorkloadCoreFields: WorkloadCoreFields{
				PromiseName:  "promise",
				ResourceName: "resource",
				WorkloadGroups: []WorkloadGroup{
					{
						Workloads: []Workload{
							{Content: "key: value"},
						},
						Directory: ".",
						ID:        hash.ComputeHash("."),
					},
					{
						Workloads: []Workload{
							{Content: "foo: bar"},
						},
						DestinationSelectors: directoryOverrideScheduling,
						Directory:            "foo",
						ID:                   hash.ComputeHash("foo"),
					},
				},
			},
		},
	}
}

func schedulingFor(destination Destination) WorkScheduling {
	if len(destination.GetLabels()) == 0 {
		return WorkScheduling{}
	}
	return WorkScheduling{
		Promise: []Selector{
			{
				MatchLabels: destination.GetLabels(),
			},
		},
	}
}
