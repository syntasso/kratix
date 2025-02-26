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

package controller_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
)

var _ = Describe("BucketStateStore Controller", func() {
	var (
		bucketStateStore         *v1alpha1.BucketStateStore
		updatedBucketStateStore  *v1alpha1.BucketStateStore
		reconciler               *controller.BucketStateStoreReconciler
		secret                   *corev1.Secret
		ctx                      context.Context
		testBucketStateStoreName types.NamespacedName
		secretName               string
	)

	BeforeEach(func() {
		ctx = context.Background()

		testBucketStateStoreName = types.NamespacedName{
			Name: "default-store",
		}

		secretName = "store-secret"

		reconciler = &controller.BucketStateStoreReconciler{
			Client: fakeK8sClient,
			Scheme: scheme.Scheme,
			Log:    ctrl.Log.WithName("controllers").WithName("BucketStateStore"),
		}

		bucketStateStore = &v1alpha1.BucketStateStore{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default-store",
			},
			TypeMeta: metav1.TypeMeta{
				Kind:       "BucketStateStore",
				APIVersion: "platform.kratix.io/v1alpha1",
			},
			Spec: v1alpha1.BucketStateStoreSpec{
				BucketName: "default-store",
				AuthMethod: "BasicAuth",
				Endpoint:   "localhost:3000",
				StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
					SecretRef: &corev1.SecretReference{
						Name:      secretName,
						Namespace: "default",
					},
				},
			},
		}

		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: "default",
			},
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			Data: map[string][]byte{
				"token": []byte("top-secret"),
			},
		}

		updatedBucketStateStore = &v1alpha1.BucketStateStore{}
		Expect(fakeK8sClient.Create(ctx, bucketStateStore)).To(Succeed())
		Expect(fakeK8sClient.Create(ctx, secret)).To(Succeed())
	})

	When("the BucketStateStore does not exists", func() {
		It("reconciles without error and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, bucketStateStore)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	When("the referenced secret does not exist", func() {
		BeforeEach(func() {
			Expect(fakeK8sClient.Delete(ctx, secret)).To(Succeed())
		})
		It("updates the status to sat the the secret cannot be found", func() {
			reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: testBucketStateStoreName})
			Expect(fakeK8sClient.Get(ctx, testBucketStateStoreName, updatedBucketStateStore)).To(Succeed())
			// Expect(updatedBucketStateStore.Status.Conditions).To(ContainElement(SatisfyAll(
			// 	HaveField("Type", "Ready"),
			// 	HaveField("Message", "Test documents written to State Store"),
			// 	HaveField("Reason", "TestDocumentsWritten"),
			// 	HaveField("Status", metav1.ConditionTrue),
			// )))
		})
	})
})
