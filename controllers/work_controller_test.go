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
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

var worker *platformv1alpha1.Cluster
var work *platformv1alpha1.Work

var _ = Context("WorkReconciler.Reconcile()", func() {
	BeforeEach(func() {
		scheduler := &Scheduler{
			Client: k8sClient,
			Log:    ctrl.Log.WithName("controllers").WithName("Scheduler"),
		}

		err := (&WorkReconciler{
			Client:    k8sClient,
			Scheme:    k8sManager.GetScheme(),
			Scheduler: scheduler,
			Log:       ctrl.Log.WithName("controllers").WithName("WorkReconciler"),
		}).SetupWithManager(k8sManager)
		Expect(err).ToNot(HaveOccurred())

		worker = &platformv1alpha1.Cluster{}
		worker.Name = "worker-1"
		worker.Namespace = "default"
		err = k8sClient.Create(context.Background(), worker)
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
				workPlacementList := &platformv1alpha1.WorkPlacementList{}
				workPlacementListOptions := &client.ListOptions{
					Namespace:     "default",
					FieldSelector: fields.OneTermEqualSelector("metadata.name", "work-controller-test-resource-request.worker-1"),
				}
				err := k8sClient.List(context.Background(), workPlacementList, workPlacementListOptions)
				g.Expect(err).ToNot(HaveOccurred())
				g.Expect(workPlacementList.Items).To(HaveLen(1), "expected one WorkPlacement")

				var createdWork platformv1alpha1.Work
				err = k8sClient.Get(context.Background(), types.NamespacedName{
					Namespace: work.Namespace,
					Name:      work.Name,
				}, &createdWork)
				g.Expect(err).ToNot(HaveOccurred())

				workPlacement := workPlacementList.Items[0]
				g.Expect(workPlacement.GetOwnerReferences()[0].UID).To(Equal(createdWork.GetUID()))

			}, timeout, interval).Should(Succeed(), "WorkPlacement was not created")
		})
	})

})
