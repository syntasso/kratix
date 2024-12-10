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

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("DestinationReconciler", func() {
	var (
		ctx                 context.Context
		testDestination     *v1alpha1.Destination
		testDestinationName client.ObjectKey
		reconciler          *controllers.DestinationReconciler
		fakeWriter          *writersfakes.FakeStateStoreWriter
		bucketStateStore    v1alpha1.BucketStateStore
	)

	BeforeEach(func() {
		ctx = context.Background()
		fakeWriter = &writersfakes.FakeStateStoreWriter{}
		reconciler = &controllers.DestinationReconciler{
			Client: fakeK8sClient,
			Log:    ctrl.Log.WithName("controllers").WithName("Destination"),
		}

		name := "test-destination"
		testDestinationName = types.NamespacedName{
			Name: name,
		}

		testDestination = &v1alpha1.Destination{
			TypeMeta: v1.TypeMeta{
				Kind:       "Destination",
				APIVersion: "platform.kratix.io/v1alpha1",
			},
			ObjectMeta: v1.ObjectMeta{
				Name: name,
			},
			Spec: v1alpha1.DestinationSpec{
				Filepath: v1alpha1.Filepath{
					Mode: v1alpha1.FilepathModeNone,
				},
				StateStoreRef: &v1alpha1.StateStoreReference{},
			},
		}
	})

	When("the resource does not exist", func() {
		It("succeeds and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, testDestination)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	When("deleting a destination", func() {
		BeforeEach(func() {
			Expect(fakeK8sClient.Create(ctx, &corev1.Secret{
				TypeMeta: v1.TypeMeta{
					Kind:       "Secret",
					APIVersion: "v1",
				},
				ObjectMeta: v1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"accessKeyID":     []byte("test-access"),
					"secretAccessKey": []byte("test-secret"),
				},
			})).To(Succeed())

			bucketStateStore = v1alpha1.BucketStateStore{
				TypeMeta: v1.TypeMeta{
					Kind:       "BucketStateStore",
					APIVersion: "platform.kratix.io/v1alpha1",
				},
				ObjectMeta: v1.ObjectMeta{
					Name: "test-state-store",
				},
				Spec: v1alpha1.BucketStateStoreSpec{
					BucketName: "test-bucket",
					StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
						SecretRef: &corev1.SecretReference{
							Name:      "test-secret",
							Namespace: "default",
						},
					},
					Endpoint: "localhost:9000",
				},
			}
			Expect(fakeK8sClient.Create(ctx, &bucketStateStore)).To(Succeed())

			testDestination.Spec.StateStoreRef.Kind = "BucketStateStore"
			testDestination.Spec.StateStoreRef.Name = "test-state-store"

			controllers.SetNewS3Writer(func(logger logr.Logger, stateStoreSpec v1alpha1.BucketStateStoreSpec, destination v1alpha1.Destination,
				creds map[string][]byte) (writers.StateStoreWriter, error) {
				return fakeWriter, nil
			})
		})
		When("cleanup is not set", func() {
			BeforeEach(func() {
				Expect(fakeK8sClient.Create(ctx, testDestination)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, testDestinationName, testDestination)).To(Succeed())
			})
			It("should not contain cleanup finalizer", func() {
				result, err := t.reconcileUntilCompletion(reconciler, testDestination)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				destination := &v1alpha1.Destination{}
				Expect(fakeK8sClient.Get(ctx, testDestinationName, destination)).
					To(Succeed())
				Expect(destination.GetFinalizers()).NotTo(ContainElement("kratix.io/destination-cleanup"))

				Expect(fakeK8sClient.Delete(ctx, destination)).To(Succeed())
				_, err = t.reconcileUntilCompletion(reconciler, destination)
				Expect(err).NotTo(HaveOccurred())
			})
		})
		When("cleanup is set to all", func() {
			BeforeEach(func() {
				testDestination.Spec.Cleanup = v1alpha1.DestinationCleanupAll
				Expect(fakeK8sClient.Create(ctx, testDestination)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, testDestinationName, testDestination)).To(Succeed())
			})
			It("should delete the workplacement and statestore contents", func() {
				result, err := t.reconcileUntilCompletion(reconciler, testDestination)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				By("setting the finalizer on testDestination on creation")
				destination := &v1alpha1.Destination{}
				Expect(fakeK8sClient.Get(ctx, testDestinationName, destination)).
					To(Succeed())
				Expect(destination.GetFinalizers()).To(ContainElement("kratix.io/destination-cleanup"))

				By("cleaning up workplacement with matching label on deletion")
				workPlacement := v1alpha1.WorkPlacement{
					ObjectMeta: v1.ObjectMeta{
						Name:      "test-workplacement",
						Namespace: "default",
						Labels:    map[string]string{v1alpha1.KratixPrefix + "targetDestinationName": destination.Name},
					},
					Spec: v1alpha1.WorkPlacementSpec{TargetDestinationName: destination.Name},
				}
				Expect(fakeK8sClient.Create(ctx, &workPlacement)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "test-workplacement", Namespace: "default"},
					&v1alpha1.WorkPlacement{})).To(Succeed())

				Expect(fakeK8sClient.Delete(ctx, testDestination)).To(Succeed())
				_, err = t.reconcileUntilCompletion(reconciler, testDestination)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeK8sClient.Get(ctx, testDestinationName, destination)).To(MatchError(ContainSubstring("not found")))
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "test-workplacement", Namespace: "default"},
					&v1alpha1.WorkPlacement{})).To(MatchError(ContainSubstring("not found")))

				By("cleaning up statestore contents")
				Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(4))
				dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(2)
				Expect(dir).To(Equal("dependencies"))
				Expect(workPlacementName).To(Equal("kratix-canary"))
				Expect(workloadsToCreate).To(BeNil())
				Expect(workloadsToDelete).To(BeNil())

				dir, workPlacementName, workloadsToCreate, workloadsToDelete = fakeWriter.UpdateFilesArgsForCall(3)
				Expect(dir).To(Equal("resources"))
				Expect(workPlacementName).To(Equal("kratix-canary"))
				Expect(workloadsToCreate).To(BeNil())
				Expect(workloadsToDelete).To(BeNil())
			})
		})
	})

})
