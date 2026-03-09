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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/internal/controller/controllerfakes"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		repositoryCache     *controllerfakes.FakeRepositoryCache
	)

	BeforeEach(func() {
		eventRecorder = record.NewFakeRecorder(1024)
		repositoryCache = &controllerfakes.FakeRepositoryCache{}
		ctx = context.Background()

		fakeWriter = &writersfakes.FakeStateStoreWriter{}
		reconciler = &controller.DestinationReconciler{
			Client:          fakeK8sClient,
			EventRecorder:   eventRecorder,
			Log:             ctrl.Log.WithName("controllers").WithName("Destination"),
			RepositoryCache: repositoryCache,
		}

		name := "test-destination"
		testDestinationName = types.NamespacedName{
			Name: name,
		}

		testDestination = &v1alpha1.Destination{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Destination",
				APIVersion: "platform.kratix.io/v1alpha1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: v1alpha1.DestinationSpec{
				Path: "test-path",
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

		repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{
			Writer: fakeWriter,
		}, nil)
	})

	When("the destination does not exist", func() {
		It("succeeds and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, testDestination)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
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

					It("creates the init workloads", func() {
						Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
						assertCanaryFilesWereCreated(fakeWriter)
					})

					It("publishes a success event", func() {
						Expect(eventRecorder.Events).To(Receive(ContainSubstring(
							"Destination is Ready"),
						))
					})
				})

				When("the init workloads are disabled", func() {
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
								HaveField("Message", "Destination is ready"),
								HaveField("Reason", "DestinationReady"),
								HaveField("Status", metav1.ConditionTrue),
							)))
						})

						It("does not create any test workloads", func() {
							Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(0))
						})

						It("publishes a success event", func() {
							Expect(eventRecorder.Events).To(Receive(ContainSubstring(
								"Destination is Ready"),
							))
						})
					})

					When("previously it had been enabled", func() {
						BeforeEach(func() {
							result, reconcileErr = t.reconcileUntilCompletion(reconciler, testDestination)
							fakeK8sClient.Get(ctx, testDestinationName, updatedDestination)
							Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
							assertCanaryFilesWereCreated(fakeWriter)

							var dest v1alpha1.Destination
							Expect(fakeK8sClient.Get(ctx, testDestinationName, &dest)).To(Succeed())
							dest.Spec.InitWorkloads.Enabled = false
							Expect(fakeK8sClient.Update(ctx, &dest)).To(Succeed())
						})

						It("removes the condition and deletes the files", func() {
							Expect(reconcileErr).NotTo(HaveOccurred())
							Expect(fakeWriter.DeleteFilesCallCount()).To(Equal(2))
							workPlacementName, workloadsToDelete := fakeWriter.DeleteFilesArgsForCall(0)
							Expect(workPlacementName).To(Equal("kratix-canary"))
							Expect(workloadsToDelete).To(ConsistOf("test-path/kratix-canary-namespace.yaml", "test-path/kratix-canary-configmap.yaml"))

							workPlacementName, workloadsToDelete = fakeWriter.DeleteFilesArgsForCall(1)
							Expect(workPlacementName).To(Equal("kratix-canary"))
							Expect(workloadsToDelete).To(ConsistOf("test-path/kratix-canary-namespace.yaml", "test-path/kratix-canary-configmap.yaml"))
						})

						It("updates the destination status condition", func() {
							Expect(updatedDestination.Status.Conditions).To(ContainElement(SatisfyAll(
								HaveField("Type", "Ready"),
								HaveField("Message", "Destination is ready"),
								HaveField("Reason", "DestinationReady"),
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

					It("tries again later", func() {
						Expect(reconcileErr).To(MatchError(ContainSubstring("reconcile loop detected")))
						Expect(result).To(Equal(ctrl.Result{RequeueAfter: time.Second * 15}))
					})

					It("updates the destination status condition", func() {
						Expect(updatedDestination.Status.Conditions).To(ContainElement(SatisfyAll(
							HaveField("Type", "Ready"),
							HaveField("Message", "Failed to write test documents to State Store: update file error"),
							HaveField("Reason", "StateStoreWriteFailed"),
							HaveField("Status", metav1.ConditionFalse),
						)))
					})

					It("publishes a failure event", func() {
						Expect(eventRecorder.Events).To(Receive(ContainSubstring(
							"Failed to write test documents to State Store: update file error"),
						))
					})
				})

				When("the reposistory is not ready", func() {
					BeforeEach(func() {
						repositoryCache.GetRepositoryByTypeAndNameReturns(nil, controller.ErrCacheMiss)
					})

					It("tries again later", func() {
						Expect(reconcileErr).To(MatchError(ContainSubstring("reconcile loop detected")))
						Expect(result).To(Equal(ctrl.Result{RequeueAfter: time.Second * 15}))
					})

					It("updates the destination status condition", func() {
						Expect(updatedDestination.Status.Conditions).To(ContainElement(SatisfyAll(
							HaveField("Type", "Ready"),
							HaveField("Message", ContainSubstring("not ready")),
							HaveField("Reason", "StateStoreNotReady"),
							HaveField("Status", metav1.ConditionFalse),
						)))
					})

					It("publishes a failure event", func() {
						Expect(eventRecorder.Events).To(Receive(ContainSubstring(
							"not ready"),
						))
					})
				})

				Describe("cleanup mode", func() {
					var workPlacement v1alpha1.WorkPlacement
					BeforeEach(func() {
						workPlacement = v1alpha1.WorkPlacement{
							ObjectMeta: metav1.ObjectMeta{
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
								Expect(fakeWriter.DeleteFilesCallCount()).To(Equal(1))
								workPlacementName, workloadsToDelete := fakeWriter.DeleteFilesArgsForCall(0)
								Expect(workPlacementName).To(Equal("kratix-canary"))
								Expect(workloadsToDelete).To(ConsistOf("test-path/kratix-canary-namespace.yaml", "test-path/kratix-canary-configmap.yaml"))
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
								Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
								Expect(fakeWriter.DeleteFilesCallCount()).To(Equal(0))
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
}

var stateStoreSetups = map[string]StateStoreSetup{
	"BucketStateStore": {
		CreateSecret: func() *corev1.Secret {
			return &corev1.Secret{
				TypeMeta:   metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
				ObjectMeta: metav1.ObjectMeta{Name: "test-secret", Namespace: "default"},
				Data: map[string][]byte{
					"accessKeyID":     []byte("test-access"),
					"secretAccessKey": []byte("test-secret"),
				},
			}
		},
		CreateStateStore: func() client.Object {
			return &v1alpha1.BucketStateStore{
				TypeMeta:   metav1.TypeMeta{Kind: "BucketStateStore", APIVersion: "platform.kratix.io/v1alpha1"},
				ObjectMeta: metav1.ObjectMeta{Name: "test-state-store"},
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
	},
	"GitStateStore": {
		CreateSecret: func() *corev1.Secret {
			return &corev1.Secret{
				TypeMeta:   metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
				ObjectMeta: metav1.ObjectMeta{Name: "test-git-secret", Namespace: "default"},
				Data: map[string][]byte{
					"username": []byte("test-username"),
					"password": []byte("test-password"),
				},
			}
		},
		CreateStateStore: func() client.Object {
			return &v1alpha1.GitStateStore{
				TypeMeta:   metav1.TypeMeta{Kind: "GitStateStore", APIVersion: "platform.kratix.io/v1alpha1"},
				ObjectMeta: metav1.ObjectMeta{Name: "test-git-state-store"},
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
	},
}

func assertCanaryFilesWereCreated(fakeWriter *writersfakes.FakeStateStoreWriter) {
	dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
	ExpectWithOffset(1, dir).To(Equal(""))
	ExpectWithOffset(1, workPlacementName).To(Equal("kratix-canary"))
	ExpectWithOffset(1, workloadsToCreate).To(ConsistOf(
		v1alpha1.Workload{
			Filepath: "test-path/kratix-canary-namespace.yaml",
			Content:  "apiVersion: v1\nkind: Namespace\nmetadata:\n  creationTimestamp: null\n  name: kratix-worker-system\nspec: {}\nstatus: {}\n",
		},
		v1alpha1.Workload{
			Filepath: "test-path/kratix-canary-configmap.yaml",
			Content:  "apiVersion: v1\ndata:\n  canary: this confirms your infrastructure is reading from Kratix state stores\nkind: ConfigMap\nmetadata:\n  creationTimestamp: null\n  name: kratix-info\n  namespace: kratix-worker-system\n",
		},
	))

	ExpectWithOffset(1, workloadsToDelete).To(BeNil())
}
