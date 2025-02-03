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
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	//+kubebuilder:scaffold:imports
)

var _ = Describe("WorkPlacementReconciler", func() {
	var (
		ctx                   context.Context
		workloads             []v1alpha1.Workload
		decompressedWorkloads []v1alpha1.Workload
		destination           v1alpha1.Destination
		gitStateStore         v1alpha1.GitStateStore
		bucketStateStore      v1alpha1.BucketStateStore

		workPlacementName = "test-work-placement"
		workPlacement     v1alpha1.WorkPlacement
		reconciler        *controllers.WorkPlacementReconciler
		fakeWriter        *writersfakes.FakeStateStoreWriter

		argBucketStateStoreSpec v1alpha1.BucketStateStoreSpec
		argGitStateStoreSpec    v1alpha1.GitStateStoreSpec
		argDestination          v1alpha1.Destination
		argCreds                map[string][]byte
	)

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = &controllers.WorkPlacementReconciler{
			Client:       fakeK8sClient,
			Log:          ctrl.Log.WithName("controllers").WithName("Work"),
			VersionCache: make(map[string]string),
		}

		compressedContent, err := compression.CompressContent([]byte("{someApi: foo, someValue: bar}"))
		Expect(err).ToNot(HaveOccurred())

		workloads = []v1alpha1.Workload{
			{
				Filepath: "fruit.yaml",
				Content:  string(compressedContent),
			},
		}

		decompressedWorkloads = []v1alpha1.Workload{
			{
				Filepath: "fruit.yaml",
				Content:  "{someApi: foo, someValue: bar}",
			},
		}

		workPlacement = v1alpha1.WorkPlacement{
			TypeMeta: v1.TypeMeta{
				Kind:       "WorkPlacement",
				APIVersion: "platform.kratix.io/v1alpha1",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      workPlacementName,
				Namespace: "default",
			},
			Spec: v1alpha1.WorkPlacementSpec{
				TargetDestinationName: "test-destination",
				ID:                    hash.ComputeHash("."),
				Workloads:             workloads,
				PromiseName:           "test-promise",
				ResourceName:          "test-resource",
			},
		}

		Expect(fakeK8sClient.Create(ctx, &workPlacement)).To(Succeed())
		fakeWriter = &writersfakes.FakeStateStoreWriter{}

		destination = v1alpha1.Destination{
			TypeMeta: v1.TypeMeta{
				Kind:       "Destination",
				APIVersion: "platform.kratix.io/v1alpha1",
			},
			ObjectMeta: v1.ObjectMeta{
				Name: "test-destination",
			},
			Spec: v1alpha1.DestinationSpec{
				Filepath: v1alpha1.Filepath{
					Mode: v1alpha1.FilepathModeNone,
				},
				StateStoreRef: &v1alpha1.StateStoreReference{},
			},
		}
	})

	When("the destination statestore is s3", func() {
		When("the destination has filepath mode of none", func() {
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

				destination.Spec.StateStoreRef.Kind = "BucketStateStore"
				destination.Spec.StateStoreRef.Name = "test-state-store"
				Expect(fakeK8sClient.Create(ctx, &destination)).To(Succeed())

				controllers.SetNewS3Writer(func(_ logr.Logger,
					stateStoreSpec v1alpha1.BucketStateStoreSpec,
					destination v1alpha1.Destination,
					creds map[string][]byte,
				) (writers.StateStoreWriter, error) {
					argBucketStateStoreSpec = stateStoreSpec
					argDestination = destination
					argCreds = creds
					return fakeWriter, nil
				})
			})

			It("reconciles", func() {
				result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				By("calling UpdateFiles()")
				Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
				dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
				Expect(workPlacementName).To(Equal(workPlacement.Name))
				Expect(dir).To(Equal(""))

				By("writing workloads files and kratix state file")
				Expect(workloadsToCreate).To(ConsistOf(append(decompressedWorkloads, v1alpha1.Workload{
					Filepath: fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name),
					Content: `files:
- fruit.yaml
`,
				})))
				Expect(workloadsToDelete).To(BeNil())

				By("constructing the writer using the statestore and destination")
				Expect(argCreds).To(Equal(map[string][]byte{
					"accessKeyID":     []byte("test-access"),
					"secretAccessKey": []byte("test-secret"),
				}))
				Expect(argDestination).To(Equal(destination))
				Expect(argBucketStateStoreSpec).To(Equal(bucketStateStore.Spec))

				By("setting the finalizer")
				workPlacement := &v1alpha1.WorkPlacement{}
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: workPlacementName, Namespace: "default"}, workPlacement)).
					To(Succeed())
				Expect(workPlacement.GetFinalizers()).To(ConsistOf(
					"finalizers.workplacement.kratix.io/repo-cleanup",
					"finalizers.workplacement.kratix.io/kratix-dot-files-cleanup",
				))
			})

			When("deleting a work placement", func() {
				BeforeEach(func() {
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
				})

				It("calls UpdateFiles()", func() {
					fakeWriter.ReadFileReturns([]byte(`
files:
  - fruit.yaml`), nil)
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					kratixStateFile := fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name)
					Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(4))
					Expect(fakeWriter.ReadFileCallCount()).To(Equal(3))
					Expect(fakeWriter.ReadFileArgsForCall(1)).To(Equal(kratixStateFile))

					dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(2)
					Expect(workPlacementName).To(Equal(workPlacement.Name))
					Expect(workloadsToCreate).To(BeNil())
					Expect(workloadsToDelete).To(ConsistOf("fruit.yaml"))
					Expect(dir).To(Equal(""))

					dir, workPlacementName, workloadsToCreate, workloadsToDelete = fakeWriter.UpdateFilesArgsForCall(3)
					Expect(workPlacementName).To(Equal(workPlacement.Name))
					Expect(workloadsToCreate).To(BeNil())
					Expect(workloadsToDelete).To(ConsistOf(kratixStateFile))
					Expect(dir).To(Equal(""))
				})

				When("the Destination does not exists", func() {
					It("removes the repo-cleanup and kratix-dot-files-cleanup finalizers", func() {
						Expect(fakeK8sClient.Delete(ctx, &destination)).To(Succeed())
						Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())

						_, err := reconciler.Reconcile(ctx,
							ctrl.Request{NamespacedName: types.NamespacedName{Name: workPlacement.GetName(),
								Namespace: workPlacement.GetNamespace()}},
						)
						Expect(err).ToNot(HaveOccurred())

						err = fakeK8sClient.Get(
							ctx,
							types.NamespacedName{
								Name:      workPlacement.GetName(),
								Namespace: "default",
							},
							&workPlacement)
						Expect(errors.IsNotFound(err)).To(BeTrue())
					})
				})
			})

			When("statestore and workplacement.spec.workloads has diverged", func() {
				It("reflects workplacement.spec.workloads", func() {
					fakeWriter.ReadFileReturns([]byte(`
files:
  - banana.yaml
  - apple.yaml
  - fruit.yaml`), nil)

					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(fakeWriter.ReadFileCallCount()).To(Equal(2))
					Expect(fakeWriter.ReadFileArgsForCall(0)).To(Equal(fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name)))

					Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
					dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
					Expect(workPlacementName).To(Equal(workPlacement.Name))
					Expect(workloadsToCreate).To(ConsistOf(append(decompressedWorkloads, v1alpha1.Workload{
						Filepath: fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name),
						Content: `files:
- fruit.yaml
`,
					})))
					Expect(workloadsToDelete).To(ConsistOf("banana.yaml", "apple.yaml"))
					Expect(dir).To(Equal(""))
				})
			})
		})
	})

	When("the destination statestore is git", func() {
		When("the destination has filepath mode of nestedByMetadata", func() {
			BeforeEach(func() {
				setupGitDestination(&gitStateStore, &destination)
				controllers.SetNewGitWriter(func(_ logr.Logger,
					stateStoreSpec v1alpha1.GitStateStoreSpec,
					destination v1alpha1.Destination,
					creds map[string][]byte,
				) (writers.StateStoreWriter, error) {
					argGitStateStoreSpec = stateStoreSpec
					argDestination = destination
					argCreds = creds
					return fakeWriter, nil
				})
			})

			It("calls the writer with a directory nested by metadata", func() {
				result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
				dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
				Expect(dir).To(Equal("resources/default/test-promise/test-resource/5058f"))
				Expect(workPlacementName).To(Equal(workPlacement.Name))
				Expect(workloadsToCreate).To(Equal(decompressedWorkloads))
				Expect(workloadsToDelete).To(BeEmpty())
			})

			It("constructs the writer using the statestore and destination", func() {
				result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(argCreds).To(Equal(map[string][]byte{
					"username": []byte("test-username"),
					"password": []byte("test-password"),
				}))
				Expect(argDestination).To(Equal(destination))
				Expect(argGitStateStoreSpec).To(Equal(gitStateStore.Spec))
			})

			When("the work placement is for a promise", func() {
				It("uses the promise directory structure", func() {
					workPlacement.Spec.ResourceName = ""
					Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
					dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
					Expect(dir).To(Equal("dependencies/test-promise/5058f"))
					Expect(workPlacementName).To(Equal(workPlacement.Name))
					Expect(workloadsToCreate).To(Equal(decompressedWorkloads))
					Expect(workloadsToDelete).To(BeEmpty())
				})
			})
		})
	})

	Describe("WorkPlacement Status", func() {
		BeforeEach(func() {
			setupGitDestination(&gitStateStore, &destination)
			controllers.SetNewGitWriter(func(
				_ logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec,
				destination v1alpha1.Destination,
				creds map[string][]byte,
			) (writers.StateStoreWriter, error) {
				argGitStateStoreSpec = stateStoreSpec
				argDestination = destination
				argCreds = creds
				return fakeWriter, nil
			})
		})

		It("is updated with the last VersionID", func() {
			fakeWriter.UpdateFilesReturns("an-amazing-version-id", nil)

			result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updatedWorkPlacement := v1alpha1.WorkPlacement{}
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
				Name:      workPlacement.GetName(),
				Namespace: workPlacement.GetNamespace(),
			}, &updatedWorkPlacement)).To(Succeed())
			Expect(updatedWorkPlacement.Status.VersionID).To(Equal("an-amazing-version-id"))
		})

		It("won't update the versionid when no new version is generated", func() {
			workPlacement.Status.VersionID = "an-amazing-version-id"
			Expect(fakeK8sClient.Status().Update(ctx, &workPlacement)).To(Succeed())

			fakeWriter.UpdateFilesReturns("", nil)

			result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			updatedWorkPlacement := v1alpha1.WorkPlacement{}
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
				Name:      workPlacement.GetName(),
				Namespace: workPlacement.GetNamespace(),
			}, &updatedWorkPlacement)).To(Succeed())

			Expect(updatedWorkPlacement.Status.VersionID).To(Equal("an-amazing-version-id"))
		})

		When("updating the status fails", func() {
			It("applies the Version ID on the next reconcile", func() {
				errSubResourceUpdate = fmt.Errorf("an-error")

				fakeWriter.UpdateFilesReturnsOnCall(0, "an-amazing-version-id", nil)

				result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).To(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))
				Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(1))

				errSubResourceUpdate = nil

				result, err = t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))
				Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(3))

				latestWP := v1alpha1.WorkPlacement{}
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
					Name:      workPlacement.GetName(),
					Namespace: workPlacement.GetNamespace(),
				}, &latestWP)).To(Succeed())

				Expect(latestWP.Status.VersionID).To(Equal("an-amazing-version-id"))
			})
		})
	})
})

func setupGitDestination(gitStateStore *v1alpha1.GitStateStore, destination *v1alpha1.Destination) {
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
			"username": []byte("test-username"),
			"password": []byte("test-password"),
		},
	})).To(Succeed())
	*gitStateStore = v1alpha1.GitStateStore{
		TypeMeta: v1.TypeMeta{
			Kind:       "GitStateStore",
			APIVersion: "platform.kratix.io/v1alpha1",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: "test-state-store",
		},
		Spec: v1alpha1.GitStateStoreSpec{
			StateStoreCoreFields: v1alpha1.StateStoreCoreFields{
				SecretRef: &corev1.SecretReference{
					Name:      "test-secret",
					Namespace: "default",
				},
			},
			URL:        "",
			Branch:     "main",
			AuthMethod: v1alpha1.BasicAuthMethod,
		},
	}
	Expect(fakeK8sClient.Create(ctx, gitStateStore)).To(Succeed())

	destination.Spec.StateStoreRef.Kind = "GitStateStore"
	destination.Spec.StateStoreRef.Name = "test-state-store"
	destination.Spec.Filepath.Mode = v1alpha1.FilepathModeNestedByMetadata
	Expect(fakeK8sClient.Create(ctx, destination)).To(Succeed())
}
