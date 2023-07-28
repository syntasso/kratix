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

	var devCluster, devCluster2, prodCluster Cluster
	var resourcesWork, prodResourcesWork, devResourcesWork, resRequestWork Work
	var workPlacements WorkPlacementList
	var scheduler *Scheduler

	BeforeEach(func() {
		devCluster = newCluster("dev-1", map[string]string{"environment": "dev"})
		devCluster2 = newCluster("dev-2", map[string]string{"environment": "dev"})
		prodCluster = newCluster("prod", map[string]string{"environment": "prod"})

		resourcesWork = newWork("work-name", WorkerResourceReplicas)
		prodResourcesWork = newWork("prod-work-name", WorkerResourceReplicas, schedulingFor(prodCluster))
		devResourcesWork = newWork("dev-work-name", WorkerResourceReplicas, schedulingFor(devCluster))

		resRequestWork = newWork("rr-work-name", ResourceRequestReplicas)

		scheduler = &Scheduler{
			Client: k8sClient,
			Log:    ctrl.Log.WithName("controllers").WithName("Scheduler"),
		}

		Expect(k8sClient.Create(context.Background(), &devCluster)).To(Succeed())
		Expect(k8sClient.Create(context.Background(), &devCluster2)).To(Succeed())
		Expect(k8sClient.Create(context.Background(), &prodCluster)).To(Succeed())
	})

	AfterEach(func() {
		cleanEnvironment()
	})

	Describe("#ReconcileCluster", func() {
		var devCluster3 Cluster
		BeforeEach(func() {
			// register new cluster dev
			devCluster3 = newCluster("dev3", map[string]string{"environment": "dev"})

			Expect(k8sClient.Create(context.Background(), &devCluster3)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), &prodResourcesWork)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), &devResourcesWork)).To(Succeed())
			scheduler.ReconcileCluster()
		})

		When("A new cluster is added", func() {
			It("schedules Works with matching labels to the new cluster", func() {
				ns := types.NamespacedName{
					Namespace: KratixSystemNamespace,
					Name:      "dev-work-name.dev3",
				}
				actualWorkPlacement := WorkPlacement{}
				Expect(k8sClient.Get(context.Background(), ns, &actualWorkPlacement)).To(Succeed())
				Expect(actualWorkPlacement.Spec.TargetClusterName).To(Equal(devCluster3.Name))
				Expect(actualWorkPlacement.Spec.Workload.Manifests).To(Equal(devResourcesWork.Spec.Workload.Manifests))
			})

			It("does not schedule Works with un-matching labels to the new cluster", func() {
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
			Expect(workPlacement.Name).To(Equal("rr-work-name." + workPlacement.Spec.TargetClusterName))
			Expect(workPlacement.Spec.Workload.Manifests).To(Equal(resRequestWork.Spec.Workload.Manifests))
			Expect(workPlacement.Spec.TargetClusterName).To(MatchRegexp("prod|dev\\-\\d"))
			Expect(workPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
			Expect(workPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
		})

		When("the Work has no selector", func() {
			It("creates Workplacement for all registered clusters", func() {
				err := scheduler.ReconcileWork(&resourcesWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(len(workPlacements.Items)).To(Equal(3))
			})
		})

		When("the Work matches a single cluster", func() {
			It("creates a single WorkPlacement", func() {
				err := scheduler.ReconcileWork(&prodResourcesWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(HaveLen(1))
				Expect(workPlacements.Items[0].Spec.TargetClusterName).To(Equal(prodCluster.Name))
				Expect(workPlacements.Items[0].ObjectMeta.Labels["kratix.io/work"]).To(Equal(prodResourcesWork.Name))
			})
		})

		When("the Work matches multiple clusters", func() {
			It("creates WorkPlacements for the clusters with the label", func() {
				err := scheduler.ReconcileWork(&devResourcesWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(HaveLen(2))

				devWorkPlacement := workPlacements.Items[0]
				Expect(devWorkPlacement.Spec.TargetClusterName).To(Equal(devCluster.Name))
				Expect(devWorkPlacement.ObjectMeta.Labels["kratix.io/work"]).To(Equal(devResourcesWork.Name))

				devWorkPlacement2 := workPlacements.Items[1]
				Expect(devWorkPlacement2.Spec.TargetClusterName).To(Equal(devCluster2.Name))
				Expect(devWorkPlacement2.ObjectMeta.Labels["kratix.io/work"]).To(Equal(devResourcesWork.Name))
			})
		})

		When("the Work selector matches no workers", func() {
			BeforeEach(func() {
				resourcesWork.Spec.Scheduling = WorkScheduling{
					Promise: []SchedulingConfig{
						{
							Target: Target{
								MatchLabels: map[string]string{"environment": "staging"},
							},
						},
					},
				}
			})

			It("creates no workplacements", func() {
				err := scheduler.ReconcileWork(&resourcesWork)
				Expect(err).To(MatchError("no workers can be selected for scheduling"))

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(BeEmpty())
			})
		})
	})
})

func newCluster(name string, labels map[string]string) Cluster {
	return Cluster{
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
	if workType == WorkerResourceReplicas {
		namespace = KratixSystemNamespace
	}
	return Work{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name),
		},
		Spec: WorkSpec{
			Replicas:   workType,
			Scheduling: workScheduling,
		},
	}
}

func schedulingFor(cluster Cluster) WorkScheduling {
	if len(cluster.GetLabels()) == 0 {
		return WorkScheduling{}
	}
	return WorkScheduling{
		Promise: []SchedulingConfig{
			{
				Target: Target{
					MatchLabels: cluster.GetLabels(),
				},
			},
		},
	}
}
