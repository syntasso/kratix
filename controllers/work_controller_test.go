/*
Copyright 2021 Syntasso.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers_test

import (
	"context"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/syntasso/kratix/controllers"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/rand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

var destination *platformv1alpha1.Destination
var work *platformv1alpha1.Work

var _ = Context("WorkReconciler.Reconcile()", func() {

	var workReconciler *WorkReconciler

	setupControllersOnce := func() {
		if workReconciler == nil {
			scheduler := &Scheduler{
				Client: k8sClient,
				Log:    ctrl.Log.WithName("controllers").WithName("Scheduler"),
			}
			workReconciler = &WorkReconciler{
				Client:    k8sClient,
				Scheduler: scheduler,
				Log:       ctrl.Log.WithName("controllers").WithName("WorkReconciler"),
			}
			err := workReconciler.SetupWithManager(k8sManager)
			Expect(err).ToNot(HaveOccurred())
		}
	}

	BeforeEach(func() {
		setupControllersOnce()
		destination = &platformv1alpha1.Destination{}
		destination.Name = "worker-1"
		destination.Namespace = "default"
		Expect(k8sClient.Create(context.Background(), destination)).To(Succeed())
	})

	AfterEach(func() {
		err := k8sClient.Delete(context.Background(), destination)
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("On Work Creation", func() {
		It("creates a WorkPlacement", func() {
			var timeout = "30s"
			var interval = "3s"

			work = &platformv1alpha1.Work{}
			work.Name = "work-controller-test-resource-request"
			work.Namespace = "default"
			work.Spec.Replicas = platformv1alpha1.ResourceRequestReplicas
			err := k8sClient.Create(context.Background(), work)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func(g Gomega) {
				workPlacementList := workPlacementsFor(work)
				g.Expect(workPlacementList.Items).To(HaveLen(1), "expected one WorkPlacement")

				var createdWork platformv1alpha1.Work
				err = k8sClient.Get(context.Background(), client.ObjectKeyFromObject(work), &createdWork)
				g.Expect(err).ToNot(HaveOccurred())

				workPlacement := workPlacementList.Items[0]
				g.Expect(workPlacement.GetOwnerReferences()[0].UID).To(Equal(createdWork.GetUID()))

			}, timeout, interval).Should(Succeed(), "WorkPlacement was not created")
		})
	})

	Describe("On Work updates", func() {
		var work *platformv1alpha1.Work
		var workPlacementGenID int64
		BeforeEach(func() {
			work = createWork()
			workPlacementList := workPlacementsFor(work)
			Expect(workPlacementList.Items).To(HaveLen(1), "expected one WorkPlacement")
			workPlacementGenID = workPlacementList.Items[0].GetGeneration()
		})

		AfterEach(func() {
			deleteWork(work)
		})

		When("the work.spec.workload changes", func() {
			It("updates the workplacement workloads", func() {
				work.Spec.Workloads = append(work.Spec.Workloads, platformv1alpha1.Workload{
					Content: "{someApi: newApi, someValue: newValue}",
				})
				Expect(k8sClient.Update(context.Background(), work)).To(Succeed())

				Eventually(func(g Gomega) {
					workPlacementList := workPlacementsFor(work)
					g.Expect(workPlacementList.Items).To(HaveLen(1), "expected one WorkPlacement")

					workPlacement := workPlacementList.Items[0]
					g.Expect(workPlacement.Spec.Workloads).To(HaveLen(3), "expected three Workloads")

					g.Expect(workPlacement.GetGeneration()).ToNot(Equal(workPlacementGenID))
				})
			})
		})

		When("the work.spec.workload does not change", func() {
			It("updates the workplacement workloads", func() {
				work.Spec.ResourceName = "newResourceName"
				Expect(k8sClient.Update(context.Background(), work)).To(Succeed())

				Consistently(func() bool {
					workPlacementList := workPlacementsFor(work)
					return workPlacementList.Items[0].GetGeneration() == workPlacementGenID
				}).Should(BeTrue())
			})
		})
	})
})

func workPlacementsFor(work *platformv1alpha1.Work) *platformv1alpha1.WorkPlacementList {
	workPlacementList := &platformv1alpha1.WorkPlacementList{}
	workPlacementListOptions := &client.ListOptions{
		Namespace:     "default",
		FieldSelector: fields.OneTermEqualSelector("metadata.name", "work-controller-test-resource-request.worker-1"),
	}
	err := k8sClient.List(context.Background(), workPlacementList, workPlacementListOptions)
	Expect(err).ToNot(HaveOccurred())

	return workPlacementList
}

func createWork() *platformv1alpha1.Work {
	work = &platformv1alpha1.Work{}
	work.Name = "work-" + rand.String(10)
	work.Spec.ResourceName = "someName"
	work.Namespace = "default"
	work.Spec.Replicas = platformv1alpha1.ResourceRequestReplicas
	work.Spec.Workloads = []platformv1alpha1.Workload{
		{
			Content: "{someApi: foo, someValue: bar}",
		},
		{
			Content: "{someApi: baz, someValue: bat}",
		},
	}
	err := k8sClient.Create(context.Background(), work)
	Expect(err).ToNot(HaveOccurred())
	return work
}

func deleteWork(work *platformv1alpha1.Work) {
	err := k8sClient.Delete(context.Background(), work)
	Expect(err).ToNot(HaveOccurred())
}
