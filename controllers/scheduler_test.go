package controllers_test

import (
	"context"

	. "github.com/onsi/ginkgo"
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
		devDestination = newDestination("dev-1", map[string]string{"environment": "dev"})
		devDestination2 = newDestination("dev-2", map[string]string{"environment": "dev"})
		prodDestination = newDestination("prod", map[string]string{"environment": "prod"})

		dependencyWork = newWork("work-name", DependencyReplicas)
		dependencyWorkForProd = newWork("prod-work-name", DependencyReplicas, schedulingFor(prodDestination))
		dependencyWorkForDev = newWork("dev-work-name", DependencyReplicas, schedulingFor(devDestination))

		resourceWork = newWork("rr-work-name", ResourceRequestReplicas)

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
			scheduler.ReconcileDestination()
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
				Expect(actualWorkPlacement.Spec.Workloads).To(Equal(dependencyWorkForDev.Spec.Workloads))
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
				Expect(workPlacement.Spec.Workloads).To(Equal(resourceWork.Spec.Workloads))
				Expect(workPlacement.Spec.TargetDestinationName).To(MatchRegexp("prod|dev\\-\\d"))
				Expect(workPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
				Expect(workPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
			})

			It("updates workplacements for existing works", func() {
				resourceWork.Spec.Workloads = append(resourceWork.Spec.Workloads, Workload{
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
		})

		Describe("Scheduling Dependencies (replicas=-1)", func() {
			When("the Work has no selector", func() {
				It("creates Workplacement for all registered Destinations", func() {
					err := scheduler.ReconcileWork(&dependencyWork)
					Expect(err).ToNot(HaveOccurred())

					Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
					Expect(len(workPlacements.Items)).To(Equal(3))
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
				Workloads: []Workload{
					{Content: "key: value"},
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
