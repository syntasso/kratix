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
	var resourcesWork, prodResourcesWork, devResourcesWork, resRequestWork Work
	var workPlacements WorkPlacementList
	var scheduler *Scheduler

	BeforeEach(func() {
		devDestination = newDestination("dev-1", map[string]string{"environment": "dev"})
		devDestination2 = newDestination("dev-2", map[string]string{"environment": "dev"})
		prodDestination = newDestination("prod", map[string]string{"environment": "prod"})

		resourcesWork = newWork("work-name", DependencyReplicas)
		prodResourcesWork = newWork("prod-work-name", DependencyReplicas, schedulingFor(prodDestination))
		devResourcesWork = newWork("dev-work-name", DependencyReplicas, schedulingFor(devDestination))

		resRequestWork = newWork("rr-work-name", ResourceRequestReplicas)

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
			Expect(k8sClient.Create(context.Background(), &prodResourcesWork)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), &devResourcesWork)).To(Succeed())
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
				Expect(actualWorkPlacement.Spec.Workloads).To(Equal(devResourcesWork.Spec.Workloads))
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
		It("creates a WorkPlacement for a given Work", func() {
			err := scheduler.ReconcileWork(&resRequestWork)
			Expect(err).ToNot(HaveOccurred())

			Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())

			Expect(workPlacements.Items).To(HaveLen(1))
			workPlacement := workPlacements.Items[0]
			Expect(workPlacement.Namespace).To(Equal("default"))
			Expect(workPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal("rr-work-name"))
			Expect(workPlacement.Name).To(Equal("rr-work-name." + workPlacement.Spec.TargetDestinationName))
			Expect(workPlacement.Spec.Workloads).To(Equal(resRequestWork.Spec.Workloads))
			Expect(workPlacement.Spec.TargetDestinationName).To(MatchRegexp("prod|dev\\-\\d"))
			Expect(workPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
			Expect(workPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
		})

		When("the Work has no selector", func() {
			It("creates Workplacement for all registered Destinations", func() {
				err := scheduler.ReconcileWork(&resourcesWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(len(workPlacements.Items)).To(Equal(3))
			})
		})

		When("the Work matches a single Destination", func() {
			It("creates a single WorkPlacement", func() {
				err := scheduler.ReconcileWork(&prodResourcesWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(HaveLen(1))
				Expect(workPlacements.Items[0].Spec.TargetDestinationName).To(Equal(prodDestination.Name))
				Expect(workPlacements.Items[0].ObjectMeta.Labels["kratix.io/work"]).To(Equal(prodResourcesWork.Name))
			})
		})

		When("the Work matches multiple Destinations", func() {
			It("creates WorkPlacements for the Destinations with the label", func() {
				err := scheduler.ReconcileWork(&devResourcesWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(HaveLen(2))

				devWorkPlacement := workPlacements.Items[0]
				Expect(devWorkPlacement.Spec.TargetDestinationName).To(Equal(devDestination.Name))
				Expect(devWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal(devResourcesWork.Name))

				devWorkPlacement2 := workPlacements.Items[1]
				Expect(devWorkPlacement2.Spec.TargetDestinationName).To(Equal(devDestination2.Name))
				Expect(devWorkPlacement2.ObjectMeta.Labels["kratix.io/work"]).To(Equal(devResourcesWork.Name))
			})
		})

		When("the Work selector matches no Destinations", func() {
			BeforeEach(func() {
				resourcesWork.Spec.DestinationSelectors = WorkScheduling{
					Promise: []Selector{
						{
							MatchLabels: map[string]string{"environment": "staging"},
						},
					},
				}
			})

			It("creates no workplacements", func() {
				err := scheduler.ReconcileWork(&resourcesWork)
				Expect(err).To(MatchError("no Destinations can be selected for scheduling"))

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(BeEmpty())
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
