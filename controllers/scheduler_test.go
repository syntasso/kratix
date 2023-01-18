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
	var work, prodWork, devWork, resRequestWork Work
	var workPlacements WorkPlacementList
	var scheduler *Scheduler

	BeforeEach(func() {
		devCluster = Cluster{
			ObjectMeta: v1.ObjectMeta{
				Name:      "dev-cluster-1",
				Namespace: "default",
				Labels:    map[string]string{"environment": "dev"},
			},
		}

		devCluster2 = Cluster{
			ObjectMeta: v1.ObjectMeta{
				Name:      "dev-cluster-2",
				Namespace: "default",
				Labels:    map[string]string{"environment": "dev"},
			},
		}

		prodCluster = Cluster{
			ObjectMeta: v1.ObjectMeta{
				Name:      "prod-cluster",
				Namespace: "default",
				Labels:    map[string]string{"environment": "prod"},
			},
		}

		work = Work{
			ObjectMeta: v1.ObjectMeta{
				Name:      "work-name",
				Namespace: "default",
				UID:       types.UID("123"),
			},
			Spec: WorkSpec{
				Replicas: WorkerResourceReplicas,
			},
		}

		prodWork = Work{
			ObjectMeta: v1.ObjectMeta{
				Name:      "prod-work-name",
				Namespace: "default",
				UID:       types.UID("456"),
			},
			Spec: WorkSpec{
				Replicas: WorkerResourceReplicas,
				ClusterSelector: map[string]string{
					"environment": "prod",
				},
			},
		}

		devWork = Work{
			ObjectMeta: v1.ObjectMeta{
				Name:      "dev-work-name",
				Namespace: "default",
				UID:       types.UID("789"),
			},
			Spec: WorkSpec{
				Replicas: WorkerResourceReplicas,
				ClusterSelector: map[string]string{
					"environment": "dev",
				},
			},
		}

		resRequestWork = Work{
			ObjectMeta: v1.ObjectMeta{
				Name:      "rr-work-name",
				Namespace: "default",
				UID:       types.UID("abc"),
			},
			Spec: WorkSpec{
				Replicas: ResourceRequestReplicas,
				ClusterSelector: map[string]string{
					"environment": "prod",
				},
			},
		}

		scheduler = &Scheduler{
			Client: k8sClient,
			Log:    ctrl.Log.WithName("controllers").WithName("Scheduler"),
		}

		Expect(k8sClient.Create(context.Background(), &devCluster)).To(Succeed())
		Expect(k8sClient.Create(context.Background(), &devCluster2)).To(Succeed())
		Expect(k8sClient.Create(context.Background(), &prodCluster)).To(Succeed())
	})

	Describe("#ReconcileCluster", func() {
		var devCluster3 Cluster
		BeforeEach(func() {
			// register new cluster dev
			devCluster3 = Cluster{
				ObjectMeta: v1.ObjectMeta{
					Name:      "dev-cluster-3",
					Namespace: "default",
					Labels:    map[string]string{"environment": "dev"},
				},
			}
			Expect(k8sClient.Create(context.Background(), &devCluster3)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), &prodWork)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), &devWork)).To(Succeed())
			scheduler.ReconcileCluster()
		})

		When("A new cluster is added", func() {
			It("schedules Works with matching labels to the new cluster", func() {
				ns := types.NamespacedName{
					Namespace: "default",
					Name:      "dev-work-name.dev-cluster-3",
				}
				actualWorkPlacement := WorkPlacement{}
				Expect(k8sClient.Get(context.Background(), ns, &actualWorkPlacement)).To(Succeed())
				Expect(actualWorkPlacement.Spec.TargetClusterName).To(Equal(devCluster3.Name))
				Expect(actualWorkPlacement.Spec.WorkName).To(Equal(devWork.Name))
			})

			It("does not schedule Works with un-matching labels to the new cluster", func() {
				ns := types.NamespacedName{
					Namespace: "default",
					Name:      "prod-work-name.dev-cluster-3",
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
			Expect(workPlacement.Name).To(Equal("rr-work-name.prod-cluster"))
			Expect(workPlacement.Spec.WorkName).To(Equal("rr-work-name"))
			Expect(workPlacement.Spec.TargetClusterName).To(Equal("prod-cluster"))
			Expect(workPlacement.Finalizers).To(HaveLen(1), "expected one finalizer")
			Expect(workPlacement.Finalizers[0]).To(Equal("finalizers.workplacement.kratix.io/repo-cleanup"))
		})

		When("the Work has no selector", func() {
			It("creates Workplacement for all registered clusters", func() {
				err := scheduler.ReconcileWork(&work)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(len(workPlacements.Items)).To(Equal(3))
			})
		})

		When("the Work matches a single cluster", func() {
			It("creates a single WorkPlacement", func() {
				err := scheduler.ReconcileWork(&prodWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(HaveLen(1))
				Expect(workPlacements.Items[0].Spec.TargetClusterName).To(Equal(prodCluster.Name))
				Expect(workPlacements.Items[0].Spec.WorkName).To(Equal(prodWork.Name))
			})
		})

		When("the Work matches multiple clusters", func() {
			It("creates WorkPlacements for the clusters with the label", func() {
				err := scheduler.ReconcileWork(&devWork)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(HaveLen(2))

				devWorkPlacement := workPlacements.Items[0]
				Expect(devWorkPlacement.Spec.TargetClusterName).To(Equal(devCluster.Name))
				Expect(devWorkPlacement.Spec.WorkName).To(Equal(devWork.Name))

				devWorkPlacement2 := workPlacements.Items[1]
				Expect(devWorkPlacement2.Spec.TargetClusterName).To(Equal(devCluster2.Name))
				Expect(devWorkPlacement2.Spec.WorkName).To(Equal(devWork.Name))
			})
		})

		When("the Work selector matches no clusters", func() {
			BeforeEach(func() {
				work.Spec.ClusterSelector = map[string]string{"environment": "staging"}
			})

			It("creates no workplacements", func() {
				err := scheduler.ReconcileWork(&work)
				Expect(err).To(MatchError("no Clusters can be selected for clusterSelector"))

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(BeEmpty())
			})
		})
	})
})
