package controllers_test

import (
	"context"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/syntasso/kratix/api/v1alpha1"
	. "github.com/syntasso/kratix/controllers"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("Controllers/Scheduler", func() {

	Context("Two Clusters, one with environment=dev, one with environment=prod", func() {
		var devCluster, devCluster2, prodCluster Cluster
		var work Work
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
				},
				Spec: WorkSpec{
					Replicas: WORKER_RESOURCE_REPLICAS,
				},
			}

			scheduler = &Scheduler{
				Client: k8sClient,
				Log:    ctrl.Log.WithName("controllers").WithName("Scheduler"),
			}

			Expect(k8sClient.Create(context.Background(), &devCluster)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), &devCluster2)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), &prodCluster)).To(Succeed())
			Expect(k8sClient.Create(context.Background(), &work)).To(Succeed())
		})

		When("the Work has no selector", func() {
			It("creates Workplacement for all registered clusters", func() {
				err := scheduler.ReconcileWork(&work)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(len(workPlacements.Items)).To(Equal(3))
			})
		})

		When("the Work has a clusterSelector environment=dev", func() {
			BeforeEach(func() {
				work.Spec.ClusterSelector = map[string]string{
					"environment": "dev",
				}
			})

			It("creates WorkPlacement for the clusters with the label", func() {
				err := scheduler.ReconcileWork(&work)
				Expect(err).ToNot(HaveOccurred())

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(HaveLen(2))

				devWorkPlacement := workPlacements.Items[0]
				Expect(devWorkPlacement.Spec.TargetClusterName).To(Equal(devCluster.Name))
				Expect(devWorkPlacement.Spec.WorkName).To(Equal(work.Name))

				devWorkPlacement2 := workPlacements.Items[1]
				Expect(devWorkPlacement2.Spec.TargetClusterName).To(Equal(devCluster2.Name))
				Expect(devWorkPlacement2.Spec.WorkName).To(Equal(work.Name))
			})
		})

		When("the Work selector matches no clusters", func() {
			BeforeEach(func() {
				work.Spec.ClusterSelector = map[string]string{"environment": "staging"}
			})

			It("creates no workplacements", func() {
				err := scheduler.ReconcileWork(&work)
				Expect(err).To(MatchError("no Clusters registered"))

				Expect(k8sClient.List(context.Background(), &workPlacements)).To(Succeed())
				Expect(workPlacements.Items).To(BeEmpty())
			})
		})
	})
})
