// /*
// Copyright 2021 Syntasso.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package controller_test

// import (
// 	. "github.com/onsi/ginkgo/v2"
// 	. "github.com/onsi/gomega"
// )

// var _ = Describe("PromiseRevision Controller", func() {
// 	// Context("When reconciling a resource", func() {
// 	// 	const resourceName = "test-resource"

// 	// 	ctx := context.Background()

// 	// 	typeNamespacedName := types.NamespacedName{
// 	// 		Name:      resourceName,
// 	// 		Namespace: "default", // TODO(user):Modify as needed
// 	// 	}
// 	// 	promiserevision := &platformv1alpha1.PromiseRevision{}

// 	// 	BeforeEach(func() {
// 	// 		By("creating the custom resource for the Kind PromiseRevision")
// 	// 		err := k8sClient.Get(ctx, typeNamespacedName, promiserevision)
// 	// 		if err != nil && errors.IsNotFound(err) {
// 	// 			resource := &platformv1alpha1.PromiseRevision{
// 	// 				ObjectMeta: metav1.ObjectMeta{
// 	// 					Name:      resourceName,
// 	// 					Namespace: "default",
// 	// 				},
// 	// 				// TODO(user): Specify other spec details if needed.
// 	// 			}
// 	// 			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
// 	// 		}
// 	// 	})

// 	// 	AfterEach(func() {
// 	// 		// TODO(user): Cleanup logic after each test, like removing the resource instance.
// 	// 		resource := &platformv1alpha1.PromiseRevision{}
// 	// 		err := k8sClient.Get(ctx, typeNamespacedName, resource)
// 	// 		Expect(err).NotTo(HaveOccurred())

// 	// 		By("Cleanup the specific resource instance PromiseRevision")
// 	// 		Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
// 	// 	})
// 	// 	It("should successfully reconcile the resource", func() {
// 	// 		By("Reconciling the created resource")
// 	// 		controllerReconciler := &PromiseRevisionReconciler{
// 	// 			Client: k8sClient,
// 	// 			Scheme: k8sClient.Scheme(),
// 	// 		}

// 	// 		_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
// 	// 			NamespacedName: typeNamespacedName,
// 	// 		})
// 	// 		Expect(err).NotTo(HaveOccurred())
// 	// 		// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
// 	// 		// Example: If you expect a certain status condition after reconciliation, verify it here.
// 	// 	})
// 	// })
// })
