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

	. "github.com/syntasso/kratix/controllers"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/fields"
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

	AfterEach(func() {
		err := k8sClient.Delete(context.Background(), worker, &client.DeleteOptions{})
		Expect(err).ToNot(HaveOccurred())
		err = k8sClient.Delete(context.Background(), work, &client.DeleteOptions{})
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("On Work Creation", func() {
		It("creates a WorkPlacement when other WorkPlacements exist", func() {
			var timeout = "30s"
			var interval = "3s"

			workPlacement := platformv1alpha1.WorkPlacement{}
			workPlacement.Name = "some-other-work-placement"
			workPlacement.Namespace = "default"
			workPlacement.Spec.WorkName = "some-other-work"
			workPlacement.Spec.TargetClusterName = worker.Name
			err := k8sClient.Create(context.Background(), &workPlacement)
			Expect(err).ToNot(HaveOccurred())

			work = &platformv1alpha1.Work{}
			work.Name = "work-controller-test-resource-request"
			work.Namespace = "default"
			work.Spec.Replicas = platformv1alpha1.RESOURCE_REQUEST_REPLICAS
			err = k8sClient.Create(context.Background(), work)
			Expect(err).ToNot(HaveOccurred())

			workPlacementList := &platformv1alpha1.WorkPlacementList{}
			Eventually(func() int {
				workPlacementListOptions := &client.ListOptions{
					Namespace:     "default",
					FieldSelector: fields.OneTermEqualSelector("metadata.name", "work-controller-test-resource-request.worker-1"),
				}
				err := k8sClient.List(context.Background(), workPlacementList, workPlacementListOptions)
				Expect(err).ToNot(HaveOccurred())
				return len(workPlacementList.Items)
			}, timeout, interval).Should(Equal(1))

			for _, workPlacement := range workPlacementList.Items {
				err := k8sClient.Delete(context.Background(), &workPlacement, &client.DeleteOptions{})
				Expect(err).ToNot(HaveOccurred())
			}
		})
	})
})
