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
	"errors"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
)

var _ = FDescribe("BucketStateStore Controller", func() {
	var (
		bucketStateStore         *v1alpha1.BucketStateStore
		updatedBucketStateStore  *v1alpha1.BucketStateStore
		reconciler               *controller.BucketStateStoreReconciler
		fakeWriter               *writersfakes.FakeStateStoreWriter
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

		fakeWriter = &writersfakes.FakeStateStoreWriter{}
		controller.SetNewS3Writer(
			func(l logr.Logger, s v1alpha1.BucketStateStoreSpec, d string, c map[string][]byte) (writers.StateStoreWriter, error) {
				return fakeWriter, nil
			},
		)
		fakeWriter.UpdateFilesReturns("", nil)

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
				"accessKeyID":     []byte("my-access-key"),
				"secretAccessKey": []byte("my-secret-access-key"),
			},
		}

		updatedBucketStateStore = &v1alpha1.BucketStateStore{}
	})

	When("the BucketStateStore does not exist", func() {
		It("reconciles without error and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, bucketStateStore)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	When("the BucketStateStore exists", func() {
		BeforeEach(func() {
			Expect(fakeK8sClient.Create(ctx, bucketStateStore)).To(Succeed())
			Expect(fakeK8sClient.Create(ctx, secret)).To(Succeed())
		})

		It("reconciles without error and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, bucketStateStore)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
			subDir, workPlacementName, workloads, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
			Expect(subDir).To(Equal(""))
			Expect(workPlacementName).To(Equal("kratix-write-probe"))
			Expect(workloads).To(HaveLen(1))
			Expect(workloads[0].Filepath).To(Equal("kratix-write-probe.txt"))
			Expect(workloads[0].Content).To(ContainSubstring("This file tests that Kratix can write to this state store. Last write time:"))
			Expect(workloadsToDelete).To(BeEmpty())

			Expect(fakeK8sClient.Get(ctx, testBucketStateStoreName, updatedBucketStateStore)).To(Succeed())
			Expect(updatedBucketStateStore.Status.Status).To(Equal(controller.StatusReady))
			// Expect(updatedBucketStateStore.Status.Conditions).To(ContainElement(SatisfyAll(
			// 	HaveField("Type", "Ready"),
			// 	HaveField("Message", "Test document written to State Store"),
			// 	HaveField("Reason", "TestDocumentsWritten"),
			// 	HaveField("Status", metav1.ConditionTrue),
			// )))
		})

		When("the referenced secret does not exist", func() {
			BeforeEach(func() {
				Expect(fakeK8sClient.Delete(ctx, secret)).To(Succeed())
			})

			It("updates the status to say the the secret cannot be found", func() {
				reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: testBucketStateStoreName})
				Expect(fakeK8sClient.Get(ctx, testBucketStateStoreName, updatedBucketStateStore)).To(Succeed())
				Expect(updatedBucketStateStore.Status.Status).To(Equal(controller.StatusNotReady))
				// Expect(updatedBucketStateStore.Status.Conditions).To(ContainElement(SatisfyAll(
				// 	HaveField("Type", "Ready"),
				// 	HaveField("Message", "Test document written to State Store"),
				// 	HaveField("Reason", "TestDocumentsWritten"),
				// 	HaveField("Status", metav1.ConditionTrue),
				// )))
			})
		})

		When("the writer fails to write the test files", func() {
			BeforeEach(func() {
				fakeWriter.UpdateFilesReturns("", errors.New("ARGH!"))
			})

			It("updates the status to say the the test files could not be written", func() {
				reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: testBucketStateStoreName})
				Expect(fakeK8sClient.Get(ctx, testBucketStateStoreName, updatedBucketStateStore)).To(Succeed())
				Expect(updatedBucketStateStore.Status.Status).To(Equal(controller.StatusNotReady))
				// Expect(updatedBucketStateStore.Status.Conditions).To(ContainElement(SatisfyAll(
				// 	HaveField("Type", "Ready"),
				// 	HaveField("Message", "Test document written to State Store"),
				// 	HaveField("Reason", "TestDocumentsWritten"),
				// 	HaveField("Status", metav1.ConditionTrue),
				// )))
			})
		})
	})
})
