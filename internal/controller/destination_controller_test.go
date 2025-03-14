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
	"fmt"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("DestinationReconciler", func() {
	var (
		ctx                 context.Context
		testDestination     *v1alpha1.Destination
		testDestinationName client.ObjectKey
		reconciler          *controller.DestinationReconciler
		fakeWriter          *writersfakes.FakeStateStoreWriter
		stateStoreSecret    *corev1.Secret
		stateStore          client.Object
		eventRecorder       *record.FakeRecorder
		updatedDestination  *v1alpha1.Destination
	)

	BeforeEach(func() {
		eventRecorder = record.NewFakeRecorder(1024)
		ctx = context.Background()

		fakeWriter = &writersfakes.FakeStateStoreWriter{}
		reconciler = &controller.DestinationReconciler{
			Client:        fakeK8sClient,
			EventRecorder: eventRecorder,
			Log:           ctrl.Log.WithName("controllers").WithName("Destination"),
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
				InitWorkloads: v1alpha1.InitWorkloads{
					Enabled: true,
				},
			},
		}
		updatedDestination = &v1alpha1.Destination{}
	})

	When("the destination does not exist", func() {
		It("succeeds and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, testDestination)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	When("the destination does not have the migration annotation", func() {
		BeforeEach(func() {
			testDestination.Spec.Path = "foo/bar"
			Expect(fakeK8sClient.Create(ctx, testDestination)).To(Succeed())

			result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: testDestinationName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			Expect(fakeK8sClient.Get(ctx, testDestinationName, updatedDestination)).To(Succeed())
		})

		It("should patch the path with the destination name", func() {
			Expect(updatedDestination.Spec.Path).To(Equal("foo/bar/" + updatedDestination.Name))
		})

		It("should add the skip annotation", func() {
			Expect(updatedDestination.Annotations).To(
				HaveKeyWithValue(v1alpha1.SkipPathDefaultingAnnotation, "true"),
			)
		})

	})

	Describe("destinations backed by", func() {
		for stateStoreKind, setup := range stateStoreSetups {
			Context(fmt.Sprintf("a %s", stateStoreKind), func() {
				var reconcileErr error
				var result ctrl.Result

				BeforeEach(func() {
					stateStoreSecret = setup.CreateSecret()
					Expect(fakeK8sClient.Create(ctx, stateStoreSecret)).To(Succeed())

					stateStore = setup.CreateStateStore()
					Expect(fakeK8sClient.Create(ctx, stateStore)).To(Succeed())

					setup.SetDestinationStateStoreRef(testDestination)
					setup.SetWriter(fakeWriter, nil)

					fakeWriter.UpdateFilesReturns("", nil)
					Expect(fakeK8sClient.Create(ctx, testDestination)).To(Succeed())
				})

				JustBeforeEach(func() {
					result, reconcileErr = t.reconcileUntilCompletion(reconciler, testDestination)
					fakeK8sClient.Get(ctx, testDestinationName, updatedDestination)
				})

				When("writing the test resources to the destination succeeds", func() {
					It("succeeds the reconciliation", func() {
						Expect(reconcileErr).NotTo(HaveOccurred())
						Expect(result).To(Equal(ctrl.Result{}))
					})

					It("updates the destination status condition", func() {
						Expect(updatedDestination.Status.Conditions).To(ContainElement(SatisfyAll(
							HaveField("Type", "Ready"),
							HaveField("Message", "Test documents written to State Store"),
							HaveField("Reason", "TestDocumentsWritten"),
							HaveField("Status", metav1.ConditionTrue),
						)))
					})

					It("creates the canary resources", func() {
						Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(4))
						assertCanaryFilesWereCreated(fakeWriter)
					})

					It("publishes a success event", func() {
						Expect(eventRecorder.Events).To(Receive(ContainSubstring(
							"Destination %q is ready", testDestination.Name),
						))
					})
				})

				When("the canary resources are disabled", func() {
					When("at the time of creation", func() {
						BeforeEach(func() {
							var dest v1alpha1.Destination
							Expect(fakeK8sClient.Get(ctx, testDestinationName, &dest)).To(Succeed())
							dest.Spec.InitWorkloads.Enabled = false
							Expect(fakeK8sClient.Update(ctx, &dest)).To(Succeed())
						})
						It("succeeds the reconciliation", func() {
							Expect(reconcileErr).NotTo(HaveOccurred())
							Expect(result).To(Equal(ctrl.Result{}))
						})

						It("updates the destination status condition", func() {
							Expect(updatedDestination.Status.Conditions).To(ContainElement(SatisfyAll(
								HaveField("Type", "Ready"),
								HaveField("Message", "Reconciled successfully, no init workloads were written"),
								HaveField("Reason", "ReconciledSuccessfully"),
								HaveField("Status", metav1.ConditionTrue),
							)))
						})

						It("does not create any test files", func() {
							Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
							dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
							Expect(dir).To(Equal(""))
							Expect(workPlacementName).To(Equal("kratix-canary"))
							Expect(workloadsToCreate).To(BeNil())
							Expect(workloadsToDelete).To(ConsistOf("kratix-canary-namespace.yaml", "kratix-canary-configmap.yaml"))
						})

						It("publishes a success event", func() {
							Expect(eventRecorder.Events).To(Receive(ContainSubstring(
								"Destination %q is ready, skipped writing of the init workloads", testDestination.Name),
							))
						})
					})

					When("previously it had been enabled", func() {
						BeforeEach(func() {
							result, reconcileErr = t.reconcileUntilCompletion(reconciler, testDestination)
							fakeK8sClient.Get(ctx, testDestinationName, updatedDestination)
							Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(4))
							assertCanaryFilesWereCreated(fakeWriter)

							var dest v1alpha1.Destination
							Expect(fakeK8sClient.Get(ctx, testDestinationName, &dest)).To(Succeed())
							dest.Spec.InitWorkloads.Enabled = false
							Expect(fakeK8sClient.Update(ctx, &dest)).To(Succeed())
						})

						It("removes the condition and deletes the files", func() {
							Expect(reconcileErr).NotTo(HaveOccurred())
							Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(6))
							dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(4)
							Expect(dir).To(Equal(""))
							Expect(workPlacementName).To(Equal("kratix-canary"))
							Expect(workloadsToCreate).To(BeNil())
							Expect(workloadsToDelete).To(ConsistOf("kratix-canary-namespace.yaml", "kratix-canary-configmap.yaml"))

							dir, workPlacementName, workloadsToCreate, workloadsToDelete = fakeWriter.UpdateFilesArgsForCall(5)
							Expect(dir).To(Equal(""))
							Expect(workPlacementName).To(Equal("kratix-canary"))
							Expect(workloadsToCreate).To(BeNil())
							Expect(workloadsToDelete).To(ConsistOf("kratix-canary-namespace.yaml", "kratix-canary-configmap.yaml"))
						})

						It("updates the destination status condition", func() {
							Expect(updatedDestination.Status.Conditions).To(ContainElement(SatisfyAll(
								HaveField("Type", "Ready"),
								HaveField("Message", "Reconciled successfully, no init workloads were written"),
								HaveField("Reason", "ReconciledSuccessfully"),
								HaveField("Status", metav1.ConditionTrue),
							)))
						})
					})
				})

				When("writing the test resources to the destination fails", func() {
					BeforeEach(func() {
						fakeWriter.UpdateFilesReturns("", errors.New("update file error"))
						Expect(fakeK8sClient.Get(ctx, testDestinationName, updatedDestination)).To(Succeed())
					})

					It("fails the reconciliation", func() {
						Expect(reconcileErr).To(MatchError(ContainSubstring("update file error")))
						Expect(result).To(Equal(ctrl.Result{}))
					})

					It("updates the destination status condition", func() {
						Expect(updatedDestination.Status.Conditions).To(ContainElement(SatisfyAll(
							HaveField("Type", "Ready"),
							HaveField("Message", "Unable to write test documents to State Store"),
							HaveField("Reason", "StateStoreWriteFailed"),
							HaveField("Status", metav1.ConditionFalse),
						)))
					})

					It("publishes a failure event", func() {
						Expect(eventRecorder.Events).To(Receive(ContainSubstring(
							"Failed to write test documents to Destination %q: update file error", testDestination.Name),
						))
					})
				})

				When("the associated secret does not exist", func() {
					BeforeEach(func() {
						Expect(fakeK8sClient.Delete(ctx, stateStoreSecret)).To(Succeed())
					})

					It("fails the reconciliation", func() {
						Expect(reconcileErr).To(MatchError(ContainSubstring("secret %q not found in namespace %q", stateStoreSecret.GetName(), stateStoreSecret.GetNamespace())))
						Expect(result).To(Equal(ctrl.Result{}))
					})

					It("updates the destination status condition", func() {
						Expect(updatedDestination.Status.Conditions).To(ContainElement(SatisfyAll(
							HaveField("Type", "Ready"),
							HaveField("Message", "Unable to write test documents to State Store"),
							HaveField("Reason", "StateStoreWriteFailed"),
							HaveField("Status", metav1.ConditionFalse),
						)))
					})

					It("publishes a failure event", func() {
						Expect(eventRecorder.Events).To(Receive(ContainSubstring(
							"Failed to write test documents to Destination %q: secret %q not found in namespace %q", testDestination.Name, stateStoreSecret.GetName(), stateStoreSecret.GetNamespace()),
						))
					})
				})

				When("instantiating a writer fails", func() {
					BeforeEach(func() {
						setup.SetWriter(fakeWriter, errors.New("writer error"))
					})

					It("raises an error on reconciliation", func() {
						Expect(reconcileErr).To(MatchError(ContainSubstring("writer error")))
						Expect(result).To(Equal(ctrl.Result{}))
					})

					It("updates the destination status condition", func() {
						Expect(updatedDestination.Status.Conditions).To(ContainElement(SatisfyAll(
							HaveField("Type", "Ready"),
							HaveField("Message", "Unable to write test documents to State Store"),
							HaveField("Reason", "StateStoreWriteFailed"),
							HaveField("Status", metav1.ConditionFalse),
						)))
					})

					It("publishes a failure event", func() {
						Expect(eventRecorder.Events).To(Receive(ContainSubstring(
							"Failed to write test documents to Destination %q: writer error", testDestination.Name,
						)))
					})
				})

				Describe("cleanup mode", func() {
					var workPlacement v1alpha1.WorkPlacement
					BeforeEach(func() {
						workPlacement = v1alpha1.WorkPlacement{
							ObjectMeta: v1.ObjectMeta{
								Name:      "test-workplacement",
								Namespace: "default",
								Labels:    map[string]string{v1alpha1.KratixPrefix + "targetDestinationName": testDestination.Name},
							},
							Spec: v1alpha1.WorkPlacementSpec{TargetDestinationName: testDestination.Name},
						}
						Expect(fakeK8sClient.Create(ctx, &workPlacement)).To(Succeed())
					})

					When("it is set to all", func() {
						BeforeEach(func() {
							testDestination.Spec.Cleanup = v1alpha1.DestinationCleanupAll
							Expect(fakeK8sClient.Update(ctx, testDestination)).To(Succeed())
							result, reconcileErr = t.reconcileUntilCompletion(reconciler, testDestination)
							Expect(reconcileErr).NotTo(HaveOccurred())
							Expect(result).To(Equal(ctrl.Result{}))
						})

						It("sets the cleanup finalizer", func() {
							Expect(updatedDestination.GetFinalizers()).To(ContainElement("kratix.io/destination-cleanup"))
						})

						When("the destination is deleted", func() {
							BeforeEach(func() {
								Expect(fakeK8sClient.Delete(ctx, testDestination)).To(Succeed())
							})

							It("should succeed the reconciliation", func() {
								Expect(reconcileErr).NotTo(HaveOccurred())
								Expect(result).To(Equal(ctrl.Result{}))
							})

							It("should remove the destination", func() {
								Expect(fakeK8sClient.Get(ctx, testDestinationName, &v1alpha1.Destination{})).To(MatchError(ContainSubstring("not found")))
							})

							It("should delete the workplacement", func() {
								Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &v1alpha1.WorkPlacement{})).To(MatchError(ContainSubstring("not found")))
							})

							It("should clean up the statestore", func() {
								Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(6))
								dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(4)
								Expect(dir).To(Equal("dependencies"))
								Expect(workPlacementName).To(Equal("kratix-canary"))
								Expect(workloadsToCreate).To(BeNil())
								Expect(workloadsToDelete).To(BeNil())

								dir, workPlacementName, workloadsToCreate, workloadsToDelete = fakeWriter.UpdateFilesArgsForCall(5)
								Expect(dir).To(Equal("resources"))
								Expect(workPlacementName).To(Equal("kratix-canary"))
								Expect(workloadsToCreate).To(BeNil())
								Expect(workloadsToDelete).To(BeNil())
							})
						})
					})

					When("it is set to none", func() {
						BeforeEach(func() {
							testDestination.Spec.Cleanup = v1alpha1.DestinationCleanupNone
							Expect(fakeK8sClient.Update(ctx, testDestination)).To(Succeed())
							result, reconcileErr = t.reconcileUntilCompletion(reconciler, testDestination)
							Expect(reconcileErr).NotTo(HaveOccurred())
							Expect(result).To(Equal(ctrl.Result{}))
						})

						It("does not set the cleanup finalizer", func() {
							Expect(updatedDestination.GetFinalizers()).NotTo(ContainElement("kratix.io/destination-cleanup"))
						})

						When("the destination is deleted", func() {
							BeforeEach(func() {
								Expect(fakeK8sClient.Delete(ctx, testDestination)).To(Succeed())
							})

							It("should succeed the reconciliation", func() {
								Expect(reconcileErr).NotTo(HaveOccurred())
								Expect(result).To(Equal(ctrl.Result{}))
							})

							It("should remove the destination", func() {
								Expect(fakeK8sClient.Get(ctx, testDestinationName, &v1alpha1.Destination{})).To(MatchError(ContainSubstring("not found")))
							})

							It("should not delete the workplacement", func() {
								Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &v1alpha1.WorkPlacement{})).Should(Succeed())
							})

							It("should not clean up the statestore", func() {
								Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(4))
							})
						})

					})
				})

			})
		}
	})
})

type StateStoreSetup struct {
	CreateSecret                func() *corev1.Secret
	CreateStateStore            func() client.Object
	SetDestinationStateStoreRef func(destination *v1alpha1.Destination)
	SetWriter                   func(writer writers.StateStoreWriter, err error)
}

var stateStoreSetups = map[string]StateStoreSetup{
	"BucketStateStore": {
		CreateSecret: func() *corev1.Secret {
			return &corev1.Secret{
				TypeMeta:   v1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
				ObjectMeta: v1.ObjectMeta{Name: "test-secret", Namespace: "default"},
				Data: map[string][]byte{
					"accessKeyID":     []byte("test-access"),
					"secretAccessKey": []byte("test-secret"),
				},
			}
		},
		CreateStateStore: func() client.Object {
			return &v1alpha1.BucketStateStore{
				TypeMeta:   v1.TypeMeta{Kind: "BucketStateStore", APIVersion: "platform.kratix.io/v1alpha1"},
				ObjectMeta: v1.ObjectMeta{Name: "test-state-store"},
				Spec: v1alpha1.BucketStateStoreSpec{
					BucketName: "test-bucket",
					StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
						SecretRef: &corev1.SecretReference{Name: "test-secret", Namespace: "default"},
					},
					Endpoint: "localhost:9000",
				},
			}
		},
		SetDestinationStateStoreRef: func(destination *v1alpha1.Destination) {
			destination.Spec.StateStoreRef.Kind = "BucketStateStore"
			destination.Spec.StateStoreRef.Name = "test-state-store"
		},
		SetWriter: func(writer writers.StateStoreWriter, err error) {
			controller.SetNewS3Writer(
				func(l logr.Logger, s v1alpha1.BucketStateStoreSpec, d string, c map[string][]byte) (writers.StateStoreWriter, error) {
					return writer, err
				},
			)
		},
	},
	"GitStateStore": {
		CreateSecret: func() *corev1.Secret {
			return &corev1.Secret{
				TypeMeta:   v1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
				ObjectMeta: v1.ObjectMeta{Name: "test-git-secret", Namespace: "default"},
				Data: map[string][]byte{
					"username": []byte("test-username"),
					"password": []byte("test-password"),
				},
			}
		},
		CreateStateStore: func() client.Object {
			return &v1alpha1.GitStateStore{
				TypeMeta:   v1.TypeMeta{Kind: "GitStateStore", APIVersion: "platform.kratix.io/v1alpha1"},
				ObjectMeta: v1.ObjectMeta{Name: "test-git-state-store"},
				Spec: v1alpha1.GitStateStoreSpec{
					URL:        "https://github.com/test/repo",
					Branch:     "main",
					AuthMethod: "basicAuth",
					StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
						SecretRef: &corev1.SecretReference{Name: "test-git-secret", Namespace: "default"},
					},
				},
			}
		},
		SetDestinationStateStoreRef: func(destination *v1alpha1.Destination) {
			destination.Spec.StateStoreRef.Kind = "GitStateStore"
			destination.Spec.StateStoreRef.Name = "test-git-state-store"
		},
		SetWriter: func(writer writers.StateStoreWriter, err error) {
			controller.SetNewGitWriter(
				func(l logr.Logger, s v1alpha1.GitStateStoreSpec, d string, c map[string][]byte) (writers.StateStoreWriter, error) {
					return writer, err
				},
			)
		},
	},
}

func assertCanaryFilesWereCreated(fakeWriter *writersfakes.FakeStateStoreWriter) {
	dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
	ExpectWithOffset(1, dir).To(Equal(""))
	ExpectWithOffset(1, workPlacementName).To(Equal("kratix-canary"))
	ExpectWithOffset(1, workloadsToCreate).To(ConsistOf(v1alpha1.Workload{
		Filepath: "kratix-canary-namespace.yaml",
		Content:  "apiVersion: v1\nkind: Namespace\nmetadata:\n  creationTimestamp: null\n  name: kratix-worker-system\nspec: {}\nstatus: {}\n",
	}))
	ExpectWithOffset(1, workloadsToDelete).To(BeNil())

	dir, workPlacementName, workloadsToCreate, workloadsToDelete = fakeWriter.UpdateFilesArgsForCall(1)
	ExpectWithOffset(1, dir).To(Equal(""))
	ExpectWithOffset(1, workPlacementName).To(Equal("kratix-canary"))
	ExpectWithOffset(1, workloadsToCreate).To(ConsistOf(v1alpha1.Workload{
		Filepath: "kratix-canary-configmap.yaml",
		Content:  "apiVersion: v1\ndata:\n  canary: the confirms your infrastructure is reading from Kratix state stores\nkind: ConfigMap\nmetadata:\n  creationTimestamp: null\n  name: kratix-info\n  namespace: kratix-worker-system\n",
	}))
	ExpectWithOffset(1, workloadsToDelete).To(BeNil())
}
