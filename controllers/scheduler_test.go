package controllers_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/syntasso/kratix/api/v1alpha1"
	. "github.com/syntasso/kratix/controllers"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("Controllers/Scheduler", func() {

	var devDestination, devDestination2, prodDestination Destination
	var dependencyWork, dependencyWorkForProd, dependencyWorkForDev, resourceWork Work
	var workPlacements WorkPlacementList
	var scheduler *Scheduler

	BeforeEach(func() {
		devDestination = newDestination("dev-1", map[string]string{"environment": "dev", "region": "eu"})
		devDestination2 = newDestination("dev-2", map[string]string{"environment": "dev"})
		prodDestination = newDestination("prod", map[string]string{"environment": "prod"})

		dependencyWork = newWork("work-name", DependencyReplicas)
		dependencyWorkForProd = newWork("prod-work-name", DependencyReplicas, schedulingFor(prodDestination))
		dependencyWorkForDev = newWork("dev-work-name", DependencyReplicas, schedulingFor(devDestination2))

		resourceWork = newWork("rr-work-name", ResourceRequestReplicas, schedulingFor(devDestination))

		scheduler = &Scheduler{
			Client: k8sClient,
			Log:    ctrl.Log.WithName("controllers").WithName("Scheduler"),
		}

		Expect(k8sClient.Create(context.Background(), &devDestination)).To(Succeed())
		Expect(k8sClient.Create(context.Background(), &devDestination2)).To(Succeed())
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
			Expect(k8sClient.Create(context.Background(), &dependencyWorkForProd)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), &dependencyWorkForDev)).To(Succeed())
			err := scheduler.ReconcileDestination()
			Expect(err).NotTo(HaveOccurred())
		})

		When("A new destination is added", func() {
			It("schedules Works with matching labels to the new destination", func() {
				ns := types.NamespacedName{
					Namespace: KratixSystemNamespace,
					Name:      "dev-work-name.dev3",
				}
				actualWorkPlacement := WorkPlacement{}
				Expect(k8sClient.Get(context.Background(), ns, &actualWorkPlacement)).To(Succeed())
				Expect(actualWorkPlacement.Spec.TargetDestinationName).To(Equal(devDestination3.Name))
				Expect(actualWorkPlacement.Spec.Workloads).To(Equal(dependencyWorkForDev.Spec.WorkloadGroups[0].Workloads))
			})

			It("does not schedule Works with un-matching labels to the new Destination", func() {
				ns := types.NamespacedName{
					Namespace: "default",
					Name:      "prod-work-name.dev3",
				}
				actualWorkPlacement := WorkPlacement{}
				Expect(k8sClient.Get(context.Background(), ns, &actualWorkPlacement)).ToNot(Succeed())
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
				Expect(workPlacement.Name).To(Equal("rr-work-name." + workPlacement.Spec.TargetDestinationName))
				Expect(workPlacement.Spec.Workloads).To(Equal(resourceWork.Spec.WorkloadGroups[0].Workloads))
				Expect(workPlacement.Spec.TargetDestinationName).To(MatchRegexp("prod|dev\\-\\d"))
				Expect(workPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
				Expect(workPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
			})

			It("updates workplacements for existing works", func() {
				resourceWork.Spec.WorkloadGroups[0].Workloads = append(resourceWork.Spec.WorkloadGroups[0].Workloads, Workload{
					Content: "fake: content",
				})
				err := scheduler.ReconcileWork(&resourceWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

				Expect(workPlacements.Items).To(HaveLen(1))
				workPlacement := workPlacements.Items[0]
				Expect(workPlacement.Spec.Workloads).To(HaveLen(2))
				Expect(workPlacement.Spec.Workloads).To(ContainElement(Workload{
					Content: "fake: content",
				}))
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
			When("the dependency work has no top-level destination selectors", func() {
				BeforeEach(func() {
					Expect(scheduler.ReconcileWork(&dependencyWork)).To(Succeed())
					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				})

				It("creates a single workplacement for each destination", func() {
					Expect(len(workPlacements.Items)).To(Equal(3))
				})

				It("labels the workplacement with the dependency work name", func() {
					for _, workPlacement := range workPlacements.Items {
						Expect(workPlacement.GetLabels()).To(HaveKeyWithValue("kratix.io/work", dependencyWork.GetName()))
					}
				})

				It("schedules workloads with no overrides to all destinations", func() {
					destinations := destinationForContent(workPlacements.Items, "key: value")
					Expect(destinations).To(HaveLen(3))
					Expect(destinations).To(ConsistOf("dev-1", "dev-2", "prod"))
				})

				It("schedules workloads with overrides to the matching destinations", func() {
					destinations := destinationForContent(workPlacements.Items, "override: true")
					Expect(destinations).To(HaveLen(1))
					Expect(destinations).To(ConsistOf("prod"))
				})

			})

			When("the dependency work has top-level destination selectors", func() {
				BeforeEach(func() {
					dependencyWork.Spec.DestinationSelectors = WorkScheduling{
						Promise: []Selector{
							{
								MatchLabels: map[string]string{"environment": "dev"},
							},
						},
					}
					Expect(scheduler.ReconcileWork(&dependencyWork)).To(Succeed())
					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				})

				It("schedules workloads with no overrides to the top-level matching destinations", func() {
					destinations := destinationForContent(workPlacements.Items, "key: value")
					Expect(destinations).To(HaveLen(2))
					Expect(destinations).To(ConsistOf("dev-1", "dev-2"))
				})

				It("ignores the top-level, schedules workloads to the destinations mathcing the overrides", func() {
					destinations := destinationForContent(workPlacements.Items, "override: true")
					Expect(destinations).To(HaveLen(1))
					Expect(destinations).To(ConsistOf("prod"))
				})
			})

			When("the work gets updated", func() {
				BeforeEach(func() {
					err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())
					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(len(workPlacements.Items)).To(Equal(3))
				})

				It("updates Workplacement for all registered Destinations", func() {
					dependencyWork.Spec.WorkloadGroups[0].Workloads = append(dependencyWork.Spec.WorkloadGroups[0].Workloads, Workload{
						Content: "key: new-content",
					})
					dependencyWork.Spec.WorkloadGroups[1].Workloads = append(dependencyWork.Spec.WorkloadGroups[1].Workloads, Workload{
						Content: "override: new-content",
					})

					err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())
					Expect(len(workPlacements.Items)).To(Equal(3))

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

					Expect(destinationForContent(workPlacements.Items, "key: value", "key: new-content")).To(ConsistOf("dev-1", "dev-2", "prod"))
					Expect(destinationForContent(workPlacements.Items, "override: true", "override: new-content")).To(ConsistOf("prod"))
				})

				When("Work is updated to no longer match a previous destination", func() {
					var (
						matchingWorkplacements     []WorkPlacement
						misscheduledWorkplacements []WorkPlacement
					)

					BeforeEach(func() {
						matchingWorkplacements = []WorkPlacement{}
						misscheduledWorkplacements = []WorkPlacement{}
						dependencyWork.Spec.DestinationSelectors = WorkScheduling{
							Promise: []Selector{
								{MatchLabels: map[string]string{"region": "eu"}},
							},
						}

						dependencyWork.Spec.WorkloadGroups[0].Workloads = append(dependencyWork.Spec.WorkloadGroups[0].Workloads, Workload{
							Content: "fake: new-content",
						})

						err := scheduler.ReconcileWork(&dependencyWork)
						Expect(err).ToNot(HaveOccurred())
						Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
						Expect(workPlacements.Items).To(HaveLen(3))

						for _, wp := range workPlacements.Items {
							if wp.GetLabels()["kratix.io/misscheduled"] == "true" {
								misscheduledWorkplacements = append(misscheduledWorkplacements, wp)
							} else {
								matchingWorkplacements = append(matchingWorkplacements, wp)
							}
						}
					})

					It("updates the existing workplacement appropriately", func() {
						By("adding a misscheduled label and condition for the workplacements that no longer match", func() {
							Expect(misscheduledWorkplacements).To(HaveLen(1))
							wp := misscheduledWorkplacements[0]
							Expect(wp.GetLabels()).To(HaveKeyWithValue("kratix.io/misscheduled", "true"))
							Expect(wp.Status.Conditions).To(HaveLen(1))
							//ignore time for assertion
							wp.Status.Conditions[0].LastTransitionTime = v1.Time{}
							Expect(wp.Status.Conditions).To(ConsistOf(v1.Condition{
								Message: "Target destination no longer matches destinationSelectors",
								Reason:  "DestinationSelectorMismatch",
								Type:    "Misscheduled",
								Status:  "True",
							}))
						})

						By("not adding the label for the matching workplacements", func() {
							Expect(matchingWorkplacements).To(HaveLen(2))
							for _, wp := range matchingWorkplacements {
								Expect(wp.GetLabels()).ToNot(HaveKey("kratix.io/misscheduled"))
							}
						})

						By("updating the workloads only for the matching workplacements", func() {
							Expect(destinationForContent(workPlacements.Items, "override: true")).To(ConsistOf("prod"))
							Expect(destinationForContent(workPlacements.Items, "key: value")).To(ConsistOf("dev-1", "dev-2"))
							Expect(destinationForContent(workPlacements.Items, "fake: new-content")).To(ConsistOf("dev-1"))
						})
					})
				})
			})

			When("A workplacement is deleted", func() {
				BeforeEach(func() {
					Expect(scheduler.ReconcileWork(&dependencyWork)).To(Succeed())
					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(3))
				})

				It("gets recreated on next reconciliation", func() {
					for _, wp := range workPlacements.Items {
						Expect(wp.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWork.GetName()))
						//Remove finalizer
						wp.Finalizers = nil
						Expect(k8sClient.Update(context.Background(), &wp)).To(Succeed())
						//manually delete workPlacement
						Expect(k8sClient.Delete(context.Background(), &wp)).To(Succeed())
					}

					//re-reconcile
					Expect(scheduler.ReconcileWork(&dependencyWork)).To(Succeed())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(workPlacements.Items).To(HaveLen(3))

					for _, wp := range workPlacements.Items {
						Expect(wp.ObjectMeta.Labels["kratix.io/work"]).To(Equal(dependencyWork.GetName()))
					}
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
	workloadGroups := []WorkloadGroup{
		{
			WorkloadCoreFields: WorkloadCoreFields{
				Workloads: []Workload{
					{Content: "key: value"},
				},
			},
		},
	}

	if workType == DependencyReplicas {
		workloadGroups = append(workloadGroups, WorkloadGroup{
			DestinationSelectorsOverride: []Selector{
				{
					MatchLabels: map[string]string{"environment": "prod"},
				},
			},
			WorkloadCoreFields: WorkloadCoreFields{
				Workloads: []Workload{
					{
						Content: "override: true",
					},
				},
			},
		})
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
			WorkloadGroups:       workloadGroups,
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

func destinationForContent(workPlacements []WorkPlacement, contents ...string) []string {
	destinations := []string{}
	for _, workPlacement := range workPlacements {
		for _, workload := range workPlacement.Spec.Workloads {
			found := false
			for _, content := range contents {
				if workload.Content == content {
					destinations = append(destinations, workPlacement.Spec.TargetDestinationName)
					found = true
					break
				}
			}
			if found {
				break
			}
		}

	}
	return destinations
}
