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
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/internal/controller/controllerfakes"
	"github.com/syntasso/kratix/internal/telemetry"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/delivery"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
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
		workplacementRecorder *events.FakeRecorder

		workPlacementName = "test-work-placement"
		workPlacement     v1alpha1.WorkPlacement
		reconciler        *controller.WorkPlacementReconciler
		fakeWriter        *writersfakes.FakeStateStoreWriter
		recorder          *recordingWriter

		metricsReader        *sdkmetric.ManualReader
		restoreMeterProvider func()
		repositoryCache      *controllerfakes.FakeRepositoryCache
	)

	BeforeEach(func() {
		telemetry.ResetWorkPlacementMetricsForTest()
		var restore func()
		metricsReader, restore = setupTestMeterProvider()
		restoreMeterProvider = restore
		repositoryCache = &controllerfakes.FakeRepositoryCache{}

		ctx = context.Background()
		workplacementRecorder = events.NewFakeRecorder(1024)
		reconciler = &controller.WorkPlacementReconciler{
			Client:          fakeK8sClient,
			APIReader:       fakeK8sClient,
			Log:             ctrl.Log.WithName("controllers").WithName("Workplacement"),
			VersionCache:    make(map[string]string),
			EventRecorder:   workplacementRecorder,
			RepositoryCache: repositoryCache,
		}

		compressedContent, err := compression.CompressContent([]byte("{someApi: foo, someValue: bar}"))
		Expect(err).ToNot(HaveOccurred())

		compressedContent2, err := compression.CompressContent([]byte("{someOtherApi: fooz, someOtherValue: barz}"))
		Expect(err).ToNot(HaveOccurred())

		workloads = []v1alpha1.Workload{
			{
				Filepath: "fruit.yaml",
				Content:  string(compressedContent),
			},
			{
				Filepath: "file2.yaml",
				Content:  string(compressedContent2),
			},
		}

		decompressedWorkloads = []v1alpha1.Workload{
			{
				Filepath: "fruit.yaml",
				Content:  "{someApi: foo, someValue: bar}",
			},
			{
				Filepath: "file2.yaml",
				Content:  "{someOtherApi: fooz, someOtherValue: barz}",
			},
		}

		workPlacement = createWorkPlacement(workPlacementName, workloads)

		Expect(fakeK8sClient.Create(ctx, &workPlacement)).To(Succeed())
		fakeWriter = &writersfakes.FakeStateStoreWriter{}

		destination = v1alpha1.Destination{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Destination",
				APIVersion: "platform.kratix.io/v1alpha1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-destination",
			},
			Spec: v1alpha1.DestinationSpec{
				Filepath: v1alpha1.Filepath{
					Mode: v1alpha1.FilepathModeNone,
				},
				StateStoreRef: &v1alpha1.StateStoreReference{},
				Path:          "test-path",
			},
		}

		recorder = &recordingWriter{Writer: delivery.Direct(fakeWriter)}
		repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{
			Writer: recorder,
		}, nil)
	})

	removals := func() []deliverCall {
		var found []deliverCall
		for _, call := range recorder.delivered {
			if call.target.Removing {
				found = append(found, call)
			}
		}
		return found
	}

	AfterEach(func() {
		if restoreMeterProvider != nil {
			restoreMeterProvider()
		}
	})

	When("the destination statestore is s3", func() {
		When("the destination has filepath mode of none", func() {
			BeforeEach(func() {
				Expect(fakeK8sClient.Create(ctx, &corev1.Secret{
					TypeMeta: metav1.TypeMeta{
						Kind:       "Secret",
						APIVersion: "metav1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-secret",
						Namespace: "default",
					},
					Data: map[string][]byte{
						"accessKeyID":     []byte("test-access"),
						"secretAccessKey": []byte("test-secret"),
					},
				})).To(Succeed())

				bucketStateStore = v1alpha1.BucketStateStore{
					TypeMeta: metav1.TypeMeta{
						Kind:       "BucketStateStore",
						APIVersion: "platform.kratix.io/v1alpha1",
					},
					ObjectMeta: metav1.ObjectMeta{
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

				By("writing workloads files and kratix state file in destination path with no extra dir")
				pathPrefix := destination.Spec.Path + "/"
				expectedWorkloads := []v1alpha1.Workload{
					{Filepath: pathPrefix + "fruit.yaml", Content: "{someApi: foo, someValue: bar}"},
					{Filepath: pathPrefix + "file2.yaml", Content: "{someOtherApi: fooz, someOtherValue: barz}"},
					{
						Filepath: fmt.Sprintf("%s.kratix/%s-%s.yaml", pathPrefix, workPlacement.Namespace, workPlacement.Name),
						Content: `files:
- test-path/fruit.yaml
- test-path/file2.yaml
`,
					},
				}
				Expect(workloadsToCreate).To(ConsistOf(expectedWorkloads))
				Expect(workloadsToDelete).To(BeNil())

				By("writing the state file before the workloads")
				Expect(workloadsToCreate[0].Filepath).To(
					Equal(fmt.Sprintf("%s.kratix/%s-%s.yaml", pathPrefix, workPlacement.Namespace, workPlacement.Name)))

				By("fetching the right repository")
				Expect(repositoryCache.GetRepositoryByTypeAndNameCallCount()).To(BeNumerically(">=", 1))
				stateStoreType, stateStoreName := repositoryCache.GetRepositoryByTypeAndNameArgsForCall(0)
				Expect(stateStoreType).To(Equal("BucketStateStore"))
				Expect(stateStoreName).To(Equal("test-state-store"))

				By("setting the finalizer")
				workPlacement := &v1alpha1.WorkPlacement{}
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: workPlacementName, Namespace: "default"}, workPlacement)).
					To(Succeed())
				Expect(workPlacement.GetFinalizers()).To(ConsistOf(
					"finalizers.workplacement.kratix.io/repo-cleanup",
					"finalizers.workplacement.kratix.io/kratix-dot-files-cleanup",
				))

				counts := collectWorkPlacementWriteMetrics(ctx, metricsReader)
				Expect(counts).To(HaveKeyWithValue(telemetry.WorkPlacementWriteResultSuccess, int64(fakeWriter.UpdateFilesCallCount())))
				Expect(counts).NotTo(HaveKey(telemetry.WorkPlacementWriteResultFailure))
			})

			It("requeues after the configured reconciliation interval so files are re-written periodically", func() {
				reconciler.StateStoreReconciliationInterval = controller.DefaultReconciliationInterval

				result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{RequeueAfter: controller.DefaultReconciliationInterval}))
			})

			It("records a failure metric when the state store write fails", func() {
				fakeWriter.UpdateFilesReturns("", fmt.Errorf("boom"))

				_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).To(MatchError(ContainSubstring("boom")))

				counts := collectWorkPlacementWriteMetrics(ctx, metricsReader)
				Expect(counts).To(HaveKeyWithValue(telemetry.WorkPlacementWriteResultFailure, int64(1)))
				Expect(counts).NotTo(HaveKey(telemetry.WorkPlacementWriteResultSuccess))
			})

			When("the work placement is deleted before its first successful write", func() {
				It("cleans up without requiring the .kratix state file", func() {
					_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{
						Name: workPlacement.GetName(), Namespace: workPlacement.GetNamespace()}})
					Expect(err).NotTo(HaveOccurred())

					fakeWriter.ReadFileReturns(nil, writers.ErrFileNotFound)
					fakeWriter.DeleteFilesReturns(fmt.Errorf("deletes go through Deliver"))
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())

					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(removals()).To(HaveLen(1))
					Expect(removals()[0].target.Dir).To(BeEmpty())
					Expect(removals()[0].target.WorkPlacement).To(Equal(workPlacement.Name))
					Expect(removals()[0].create).To(BeNil())
					Expect(removals()[0].remove).To(ConsistOf(
						fmt.Sprintf("%s/fruit.yaml", destination.Spec.Path),
						fmt.Sprintf("%s/file2.yaml", destination.Spec.Path),
						fmt.Sprintf("%s/.kratix/%s-%s.yaml", destination.Spec.Path, workPlacement.Namespace, workPlacement.Name),
					))
					Expect(fakeWriter.DeleteFilesCallCount()).To(BeZero())

					err = fakeK8sClient.Get(ctx, types.NamespacedName{
						Name: workPlacement.GetName(), Namespace: workPlacement.GetNamespace()}, &v1alpha1.WorkPlacement{})
					Expect(errors.IsNotFound(err)).To(BeTrue(), "work placement should be gone")
				})
			})

			When("the .kratix state file goes missing after a successful write", func() {
				It("removes the WorkPlacement's workloads and the marker", func() {
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					fakeWriter.ReadFileReturns(nil, writers.ErrFileNotFound)
					fakeWriter.DeleteFilesReturns(fmt.Errorf("deletes go through Deliver"))
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())

					result, err = t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(removals()).To(HaveLen(1))
					Expect(removals()[0].target.Dir).To(BeEmpty())
					Expect(removals()[0].target.WorkPlacement).To(Equal(workPlacement.Name))
					Expect(removals()[0].create).To(BeNil())
					Expect(removals()[0].remove).To(ConsistOf(
						fmt.Sprintf("%s/fruit.yaml", destination.Spec.Path),
						fmt.Sprintf("%s/file2.yaml", destination.Spec.Path),
						fmt.Sprintf("%s/.kratix/%s-%s.yaml", destination.Spec.Path, workPlacement.Namespace, workPlacement.Name),
					))
					Expect(fakeWriter.DeleteFilesCallCount()).To(BeZero())

					err = fakeK8sClient.Get(ctx, types.NamespacedName{
						Name: workPlacement.GetName(), Namespace: workPlacement.GetNamespace()}, &v1alpha1.WorkPlacement{})
					Expect(errors.IsNotFound(err)).To(BeTrue(), "work placement should be gone")
				})
			})

			When("deleting a work placement", func() {
				BeforeEach(func() {
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

				})

				It("removes the state file's files and the state file through the writer", func() {
					fakeWriter.ReadFileReturns([]byte(fmt.Sprintf(`
files:
  - %s/fruit.yaml
  - %s/file2.yaml`, destination.Spec.Path, destination.Spec.Path)), nil)
					fakeWriter.DeleteFilesReturns(fmt.Errorf("deletes go through Deliver"))
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					kratixStateFile := fmt.Sprintf("%s/.kratix/%s-%s.yaml", destination.Spec.Path, workPlacement.Namespace, workPlacement.Name)
					Expect(removals()).To(HaveLen(1))
					Expect(removals()[0].target.Dir).To(BeEmpty())
					Expect(removals()[0].create).To(BeNil())
					Expect(removals()[0].remove).To(ConsistOf(
						fmt.Sprintf("%s/fruit.yaml", destination.Spec.Path),
						fmt.Sprintf("%s/file2.yaml", destination.Spec.Path),
						kratixStateFile,
					))
					Expect(fakeWriter.DeleteFilesCallCount()).To(BeZero())
				})

				It("also removes the WorkPlacement's workloads that the state file does not list", func() {
					fakeWriter.ReadFileReturns([]byte(fmt.Sprintf(`
files:
  - %s/fruit.yaml`, destination.Spec.Path)), nil)
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())

					_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)

					Expect(err).NotTo(HaveOccurred())
					Expect(removals()).To(HaveLen(1))
					Expect(removals()[0].remove).To(ConsistOf(
						fmt.Sprintf("%s/fruit.yaml", destination.Spec.Path),
						fmt.Sprintf("%s/file2.yaml", destination.Spec.Path),
						fmt.Sprintf("%s/.kratix/%s-%s.yaml", destination.Spec.Path, workPlacement.Namespace, workPlacement.Name),
					))
				})

				It("delivers nothing and keeps the finalizers when the state file cannot be read", func() {
					fakeWriter.ReadFileReturns(nil, fmt.Errorf("bucket unavailable"))
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())

					_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&workPlacement)})

					Expect(err).NotTo(HaveOccurred())
					Expect(removals()).To(BeEmpty())
					latest := &v1alpha1.WorkPlacement{}
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), latest)).To(Succeed())
					Expect(latest.GetFinalizers()).To(ConsistOf(
						"finalizers.workplacement.kratix.io/repo-cleanup",
						"finalizers.workplacement.kratix.io/kratix-dot-files-cleanup",
					))
				})

				It("removes only the state file once the repo-cleanup finalizer is gone", func() {
					fakeWriter.ReadFileReturns([]byte(fmt.Sprintf(`
files:
  - %s/fruit.yaml`, destination.Spec.Path)), nil)
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					workPlacement.SetFinalizers([]string{"finalizers.workplacement.kratix.io/kratix-dot-files-cleanup"})
					Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())

					_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)

					Expect(err).NotTo(HaveOccurred())
					Expect(removals()).To(HaveLen(1))
					Expect(removals()[0].remove).To(ConsistOf(
						fmt.Sprintf("%s/.kratix/%s-%s.yaml", destination.Spec.Path, workPlacement.Namespace, workPlacement.Name),
					))
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
						Expect(err).To(HaveOccurred())
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
					Expect(fakeWriter.ReadFileArgsForCall(0)).To(Equal(fmt.Sprintf("%s/.kratix/%s-%s.yaml", destination.Spec.Path, workPlacement.Namespace, workPlacement.Name)))

					Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
					dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
					Expect(workPlacementName).To(Equal(workPlacement.Name))
					pathPrefix := destination.Spec.Path + "/"
					expectedWorkloads := []v1alpha1.Workload{
						{Filepath: pathPrefix + "fruit.yaml", Content: "{someApi: foo, someValue: bar}"},
						{Filepath: pathPrefix + "file2.yaml", Content: "{someOtherApi: fooz, someOtherValue: barz}"},
						{
							Filepath: fmt.Sprintf("%s.kratix/%s-%s.yaml", pathPrefix, workPlacement.Namespace, workPlacement.Name),
							Content: `files:
- test-path/fruit.yaml
- test-path/file2.yaml
`,
						},
					}
					Expect(workloadsToCreate).To(ConsistOf(expectedWorkloads))
					Expect(workloadsToDelete).To(ConsistOf(
						fmt.Sprintf("%s/banana.yaml", destination.Spec.Path),
						fmt.Sprintf("%s/apple.yaml", destination.Spec.Path),
					))
					Expect(dir).To(Equal(""))
				})
			})

			When("multiple workplacements share the destination", func() {
				It("each workplacement only deletes its own files on update", func() {
					_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())

					otherContent, err := compression.CompressContent([]byte("{other: content}"))
					Expect(err).ToNot(HaveOccurred())
					workPlacementB := createWorkPlacement("test-work-placement-b", []v1alpha1.Workload{{
						Filepath: "other.yaml",
						Content:  string(otherContent),
					}})
					Expect(fakeK8sClient.Create(ctx, &workPlacementB)).To(Succeed())

					_, err = t.reconcileUntilCompletion(reconciler, &workPlacementB)
					Expect(err).NotTo(HaveOccurred())

					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					workPlacement.Spec.Workloads = []v1alpha1.Workload{workloads[0]}
					Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())

					fakeWriter.ReadFileReturns([]byte(fmt.Sprintf(`
files:
  - %s/fruit.yaml
  - %s/file2.yaml`, destination.Spec.Path, destination.Spec.Path)), nil)

					_, err = t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())

					callCount := fakeWriter.UpdateFilesCallCount()
					Expect(callCount).To(BeNumerically(">=", 3))
					_, wpName, _, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(callCount - 1)
					Expect(wpName).To(Equal(workPlacement.Name))
					Expect(workloadsToDelete).To(ConsistOf(fmt.Sprintf("%s/file2.yaml", destination.Spec.Path)))
					Expect(workloadsToDelete).NotTo(ContainElement(fmt.Sprintf("%s/other.yaml", destination.Spec.Path)))
				})
			})
		})
	})

	When("the destination statestore is git", func() {
		When("the destination has filepath mode of nestedByMetadata", func() {
			BeforeEach(func() {
				destination.Spec.Filepath.Mode = v1alpha1.FilepathModeNestedByMetadata
				setupGitDestination(&gitStateStore, &destination)
				repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{
					Writer: recorder,
				}, nil)
			})

			It("creates files with directory nesting to make each workplacement unique", func() {
				result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
				dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
				Expect(dir).To(ContainSubstring("test-path/resources/default/test-promise/test-resource"))
				Expect(dir).To(HaveSuffix("/"))
				Expect(workPlacementName).To(Equal(workPlacement.Name))
				Expect(workloadsToCreate).To(Equal(decompressedWorkloads))
				Expect(workloadsToDelete).To(BeEmpty())
			})

			It("fetches the right repository", func() {
				result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(repositoryCache.GetRepositoryByTypeAndNameCallCount()).To(BeNumerically(">=", 1))
				stateStoreType, stateStoreName := repositoryCache.GetRepositoryByTypeAndNameArgsForCall(0)
				Expect(stateStoreType).To(Equal("GitStateStore"))
				Expect(stateStoreName).To(Equal("test-state-store"))
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
					Expect(dir).To(Equal("test-path/dependencies/test-promise/5058f/"))
					Expect(workPlacementName).To(Equal(workPlacement.Name))
					Expect(workloadsToCreate).To(Equal(decompressedWorkloads))
					Expect(workloadsToDelete).To(BeEmpty())
				})
			})

			When("deleting a work placement", func() {
				It("names the directory as the delivery's Dir, with nothing to create or remove", func() {
					_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					fakeWriter.DeleteFilesReturns(fmt.Errorf("deletes go through Deliver"))
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())

					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(removals()).To(HaveLen(1))
					Expect(removals()[0].target).To(Equal(delivery.Target{
						Dir:           "test-path/resources/default/test-promise/test-resource/5058f/",
						Namespace:     "default",
						WorkPlacement: workPlacement.GetName(),
						Promise:       "test-promise",
						Resource:      "test-resource",
						Pipeline:      workPlacement.PipelineName(),
						Destination:   "test-destination",
						Removing:      true,
					}))
					Expect(removals()[0].create).To(BeNil())
					Expect(removals()[0].remove).To(BeNil())
					Expect(fakeWriter.DeleteFilesCallCount()).To(BeZero())

					err = fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &v1alpha1.WorkPlacement{})
					Expect(errors.IsNotFound(err)).To(BeTrue(), "work placement should be gone")
				})
			})
		})

		When("the destination has filepath mode of none", func() {
			BeforeEach(func() {
				setupGitDestination(&gitStateStore, &destination)
				repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{
					Writer: recorder,
				}, nil)
			})

			It("reconciles with dir empty and full paths for per-file delete", func() {
				result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
				dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
				Expect(workPlacementName).To(Equal(workPlacement.Name))
				Expect(dir).To(Equal(""))

				pathPrefix := destination.Spec.Path + "/"
				expectedWorkloads := []v1alpha1.Workload{
					{Filepath: pathPrefix + "fruit.yaml", Content: "{someApi: foo, someValue: bar}"},
					{Filepath: pathPrefix + "file2.yaml", Content: "{someOtherApi: fooz, someOtherValue: barz}"},
					{
						Filepath: fmt.Sprintf("%s.kratix/%s-%s.yaml", pathPrefix, workPlacement.Namespace, workPlacement.Name),
						Content: `files:
- test-path/fruit.yaml
- test-path/file2.yaml
`,
					},
				}
				Expect(workloadsToCreate).To(ConsistOf(expectedWorkloads))
				Expect(workloadsToDelete).To(BeNil())
			})

			It("fetches the right repository", func() {
				result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(repositoryCache.GetRepositoryByTypeAndNameCallCount()).To(BeNumerically(">=", 1))
				stateStoreType, stateStoreName := repositoryCache.GetRepositoryByTypeAndNameArgsForCall(0)
				Expect(stateStoreType).To(Equal("GitStateStore"))
				Expect(stateStoreName).To(Equal("test-state-store"))
			})

			When("deleting a work placement", func() {
				BeforeEach(func() {
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
				})

				It("removes only this workplacement's files through the writer", func() {
					fakeWriter.ReadFileReturns([]byte(fmt.Sprintf(`
files:
  - %s/fruit.yaml
  - %s/file2.yaml`, destination.Spec.Path, destination.Spec.Path)), nil)
					fakeWriter.DeleteFilesReturns(fmt.Errorf("deletes go through Deliver"))
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					kratixStateFile := fmt.Sprintf("%s/.kratix/%s-%s.yaml", destination.Spec.Path, workPlacement.Namespace, workPlacement.Name)
					Expect(removals()).To(HaveLen(1))
					Expect(removals()[0].target.Dir).To(BeEmpty())
					Expect(removals()[0].target.WorkPlacement).To(Equal(workPlacement.Name))
					Expect(removals()[0].create).To(BeNil())
					Expect(removals()[0].remove).To(ConsistOf(
						fmt.Sprintf("%s/fruit.yaml", destination.Spec.Path),
						fmt.Sprintf("%s/file2.yaml", destination.Spec.Path),
						kratixStateFile,
					))
					Expect(fakeWriter.DeleteFilesCallCount()).To(BeZero())
				})
			})

			When("statestore and workplacement.spec.workloads has diverged", func() {
				It("only deletes files belonging to this workplacement", func() {
					fakeWriter.ReadFileReturns([]byte(`
files:
  - banana.yaml
  - apple.yaml
  - fruit.yaml`), nil)

					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(fakeWriter.ReadFileCallCount()).To(Equal(2))
					Expect(fakeWriter.ReadFileArgsForCall(0)).To(Equal(fmt.Sprintf("%s/.kratix/%s-%s.yaml", destination.Spec.Path, workPlacement.Namespace, workPlacement.Name)))

					Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
					dir, wpName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
					Expect(wpName).To(Equal(workPlacement.Name))
					pathPrefix := destination.Spec.Path + "/"
					expectedWorkloads := []v1alpha1.Workload{
						{Filepath: pathPrefix + "fruit.yaml", Content: "{someApi: foo, someValue: bar}"},
						{Filepath: pathPrefix + "file2.yaml", Content: "{someOtherApi: fooz, someOtherValue: barz}"},
						{
							Filepath: fmt.Sprintf("%s.kratix/%s-%s.yaml", pathPrefix, workPlacement.Namespace, workPlacement.Name),
							Content: `files:
- test-path/fruit.yaml
- test-path/file2.yaml
`,
						},
					}
					Expect(workloadsToCreate).To(ConsistOf(expectedWorkloads))
					Expect(workloadsToDelete).To(ConsistOf(
						fmt.Sprintf("%s/banana.yaml", destination.Spec.Path),
						fmt.Sprintf("%s/apple.yaml", destination.Spec.Path),
					))
					Expect(dir).To(Equal(""))
				})
			})
		})

		When("the destination has filepath mode of aggregatedYAML", func() {
			var secondWorkPlacement v1alpha1.WorkPlacement

			BeforeEach(func() {
				destination.Spec.Filepath.Mode = v1alpha1.FilepathModeAggregatedYAML
				destination.Spec.Filepath.Filename = "workloads.yaml"

				setupGitDestination(&gitStateStore, &destination)

				fileContent := `{kratix: is-good}`
				compressedContent, err := compression.CompressContent([]byte(fileContent))
				Expect(err).ToNot(HaveOccurred())

				secondWorkPlacement = createWorkPlacement(workPlacementName+"-2", []v1alpha1.Workload{{
					Filepath: "some-file.yaml",
					Content:  string(compressedContent),
				}})

				Expect(fakeK8sClient.Create(ctx, &secondWorkPlacement)).To(Succeed())

				result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))
			})

			It("creates a single file in destination.Spec.Path with the user-provided filename", func() {
				mergedWorkloads := []v1alpha1.Workload{
					{
						Filepath: "test-path/workloads.yaml",
						Content:  "{someApi: foo, someValue: bar}\n---\n{someOtherApi: fooz, someOtherValue: barz}\n---\n{kratix: is-good}",
					},
				}

				Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(2))
				dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(0)
				Expect(dir).To(Equal(""))
				Expect(workPlacementName).To(Equal(workPlacement.Name))
				Expect(workloadsToCreate).To(Equal(mergedWorkloads))
				Expect(workloadsToDelete).To(BeEmpty())
			})

			When("the user does not provide a filename", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&destination), &destination)).To(Succeed())
					destination.Spec.Filepath.Filename = ""
					Expect(fakeK8sClient.Update(ctx, &destination)).To(Succeed())
					_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
				})

				It("uses the default filename aggregated.yaml", func() {
					callCount := fakeWriter.UpdateFilesCallCount()
					Expect(callCount).To(BeNumerically(">=", 1))
					_, _, workloadsToCreate, _ := fakeWriter.UpdateFilesArgsForCall(callCount - 1)
					Expect(workloadsToCreate).To(HaveLen(1))
					Expect(workloadsToCreate[0].Filepath).To(Equal("test-path/aggregated.yaml"))
				})
			})

			When("one of the workplacements is deleted", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())
				})

				It("removes the workloads of the deleted workplacement from the file", func() {
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					mergedWorkloads := []v1alpha1.Workload{
						{
							Filepath: "test-path/workloads.yaml",
							Content:  "{kratix: is-good}",
						},
					}

					Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(3))
					dir, workPlacementName, workloadsToCreate, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(2)
					Expect(dir).To(Equal(""))
					Expect(workPlacementName).To(Equal(workPlacement.Name))
					Expect(workloadsToCreate).To(Equal(mergedWorkloads))
					Expect(workloadsToDelete).To(BeEmpty())

					Expect(fakeK8sClient.Get(ctx, client.ObjectKey{
						Name:      workPlacement.GetName(),
						Namespace: workPlacement.GetNamespace(),
					}, &workPlacement)).To(HaveOccurred())

					for i := range 3 {
						_, _, _, workloadsToDelete := fakeWriter.UpdateFilesArgsForCall(i)
						Expect(workloadsToDelete).To(BeEmpty())
					}

					Expect(removals()).To(HaveLen(1))
					Expect(removals()[0].target.Dir).To(BeEmpty())
					Expect(removals()[0].create).To(Equal(mergedWorkloads))
					Expect(removals()[0].remove).To(BeNil())
				})

				It("delivers nothing and keeps the finalizer when the aggregated file cannot be built", func() {
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&secondWorkPlacement), &secondWorkPlacement)).To(Succeed())
					secondWorkPlacement.Spec.Workloads[0].Content = "not compressed"
					Expect(fakeK8sClient.Update(ctx, &secondWorkPlacement)).To(Succeed())

					_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&workPlacement)})

					Expect(err).NotTo(HaveOccurred())
					Expect(removals()).To(BeEmpty())
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					Expect(workPlacement.GetFinalizers()).To(ContainElement("finalizers.workplacement.kratix.io/repo-cleanup"))
				})
			})

			When("all workplacements are deleted", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())
					Expect(fakeK8sClient.Delete(ctx, &secondWorkPlacement)).To(Succeed())
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
				})

				It("removes the aggregated file through the writer", func() {
					Expect(removals()).To(HaveLen(1))
					Expect(removals()[0].target.Dir).To(BeEmpty())
					Expect(removals()[0].target.WorkPlacement).To(Equal(workPlacement.Name))
					Expect(removals()[0].create).To(BeNil())
					Expect(removals()[0].remove).To(ConsistOf("test-path/workloads.yaml"))
					Expect(fakeWriter.DeleteFilesCallCount()).To(BeZero())
				})
			})
		})
	})

	Describe("WorkPlacement Status", func() {
		Context("VersionID", func() {
			BeforeEach(func() {
				setupGitDestination(&gitStateStore, &destination)
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

			When("updating the status fails on updating the versionID", func() {
				It("applies the Version ID on the next X reconciliations", func() {
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
						Name:      workPlacement.Name,
						Namespace: workPlacement.Namespace,
					}, &workPlacement)).To(Succeed())
					workPlacement.Status.Conditions = []metav1.Condition{
						{
							Type:    "WriteSucceeded",
							Reason:  "WorkloadsWrittenToStateStore",
							Status:  metav1.ConditionTrue,
							Message: "",
						},
					}
					workPlacement.Status.DeliveryMode = ptr.To("")
					Expect(fakeK8sClient.Status().Update(ctx, &workPlacement)).To(Succeed())

					errSubResourceUpdate = fmt.Errorf("an-error")
					fakeWriter.UpdateFilesReturnsOnCall(0, "an-amazing-version-id", nil)

					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).To(MatchError("reconcile loop detected"))
					Expect(result).To(Equal(ctrl.Result{RequeueAfter: 15 * time.Second}))

					errSubResourceUpdate = nil
					result, err = t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).ToNot(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					latestWP := v1alpha1.WorkPlacement{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
						Name:      workPlacement.GetName(),
						Namespace: workPlacement.GetNamespace(),
					}, &latestWP)).To(Succeed())

					Expect(latestWP.Status.VersionID).To(Equal("an-amazing-version-id"))
				})
			})

			When("updating the status fails on updating the conditions", func() {
				It("applies the Version ID on the next reconcile", func() {
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					workPlacement.Status.DeliveryMode = ptr.To("")
					Expect(fakeK8sClient.Status().Update(ctx, &workPlacement)).To(Succeed())
					errSubResourceUpdate = fmt.Errorf("an-error")
					fakeWriter.UpdateFilesReturnsOnCall(0, "an-amazing-version-id", nil)

					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).To(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{RequeueAfter: 15 * time.Second}))

					errSubResourceUpdate = nil

					result, err = t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).ToNot(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					latestWP := v1alpha1.WorkPlacement{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
						Name:      workPlacement.GetName(),
						Namespace: workPlacement.GetNamespace(),
					}, &latestWP)).To(Succeed())

					Expect(latestWP.Status.VersionID).To(Equal("an-amazing-version-id"))
				})
			})
		})

		Context("Conditions", func() {
			BeforeEach(func() {
				setupGitDestination(&gitStateStore, &destination)
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
					Name:      workPlacement.Name,
					Namespace: workPlacement.Namespace,
				}, &workPlacement)).To(Succeed())
				workPlacement.Status.Conditions = nil
				Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
			})

			When("write to statestore has succeeded", func() {
				It("sets WriteSucceeded to true and publishes the right event", func() {
					fakeWriter.UpdateFilesReturns("an-id", nil)
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
						Name:      workPlacement.GetName(),
						Namespace: workPlacement.GetNamespace(),
					}, &workPlacement)).To(Succeed())

					for i := range workPlacement.Status.Conditions {
						workPlacement.Status.Conditions[i].LastTransitionTime = metav1.Time{}
					}

					Expect(workPlacement.Status.Conditions).To(ConsistOf(
						metav1.Condition{
							Type:    "WriteSucceeded",
							Status:  metav1.ConditionTrue,
							Message: "Workloads written to State Store",
							Reason:  "WorkloadsWrittenToStateStore"},
						metav1.Condition{
							Type:    "Ready",
							Status:  metav1.ConditionTrue,
							Reason:  "WorkloadsWrittenToTargetDestination",
							Message: "Ready"}))

					Eventually(workplacementRecorder.Events).Should(Receive(ContainSubstring(
						"successfully written to Destination: test-destination with versionID: an-id")))
				})
			})

			When("write to statestore has failed", func() {
				It("sets WriteSucceeded to false and publishes the right event", func() {
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
						Name:      workPlacement.Name,
						Namespace: workPlacement.Namespace,
					}, &workPlacement)).To(Succeed())
					workPlacement.Status.Conditions = nil
					Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())

					fakeWriter.UpdateFilesReturns("", fmt.Errorf("whatever error"))
					_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).To(HaveOccurred())

					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
						Name:      workPlacement.GetName(),
						Namespace: workPlacement.GetNamespace(),
					}, &workPlacement)).To(Succeed())
					for i := range workPlacement.Status.Conditions {
						workPlacement.Status.Conditions[i].LastTransitionTime = metav1.Time{}
					}
					Expect(workPlacement.Status.Conditions).To(ConsistOf(
						metav1.Condition{
							Type:    "WriteSucceeded",
							Status:  metav1.ConditionFalse,
							Reason:  "WorkloadsFailedWrite",
							Message: "whatever error"},
						metav1.Condition{
							Type:    "Ready",
							Status:  metav1.ConditionFalse,
							Reason:  "WorkloadsFailedWrite",
							Message: "Failing"}))
					Eventually(workplacementRecorder.Events).Should(Receive(ContainSubstring(
						"error writing to destination; check kubectl get destination for more info: whatever error")))
				})
			})

			When("the destination is not ready", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&destination), &destination)).To(Succeed())
					destination.SetNotReadyStatus("StateStoreNotReady", "GitStateStore/test-state-store: permission denied")
					Expect(fakeK8sClient.Status().Update(ctx, &destination)).To(Succeed())
				})

				It("does not write and sets WriteSucceeded to false with the destination's message", func() {
					result, err := t.reconcileUntilCompletion(reconciler, &workPlacement, &opts{requeueExpected: true})
					Expect(err).NotTo(HaveOccurred())
					Expect(result.RequeueAfter).To(Equal(15 * time.Second))

					Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(0))

					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					for i := range workPlacement.Status.Conditions {
						workPlacement.Status.Conditions[i].LastTransitionTime = metav1.Time{}
					}
					Expect(workPlacement.Status.Conditions).To(ConsistOf(
						metav1.Condition{
							Type:    "WriteSucceeded",
							Status:  metav1.ConditionFalse,
							Reason:  "DestinationNotReady",
							Message: `Destination "test-destination" is not ready: GitStateStore/test-state-store: permission denied`},
						metav1.Condition{
							Type:    "Ready",
							Status:  metav1.ConditionFalse,
							Reason:  "DestinationNotReady",
							Message: "Failing"}))
				})

				It("still cleans up when the work placement is deleted", func() {
					workPlacement.SetFinalizers([]string{
						"finalizers.workplacement.kratix.io/repo-cleanup",
						"finalizers.workplacement.kratix.io/kratix-dot-files-cleanup",
					})
					Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
					Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())
					fakeWriter.ReadFileReturns(nil, writers.ErrFileNotFound)

					_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
					Expect(err).NotTo(HaveOccurred())

					Expect(removals()).To(HaveLen(1))
					err = fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &v1alpha1.WorkPlacement{})
					Expect(errors.IsNotFound(err)).To(BeTrue())
				})
			})
		})

	})

	When("the built-in writer removes files from a real state store", func() {
		const remoteURL = "https://kratix.test/state.git"

		var bare string

		useGitRemote := func() {
			bare = newLocalGitRemote(remoteURL)
			setupGitDestination(&gitStateStore, &destination)
			writer, err := writers.NewGitWriter(ctrl.Log, v1alpha1.GitStateStoreSpec{
				StateStoreCoreFields: gitStateStore.Spec.StateStoreCoreFields,
				URL:                  remoteURL,
				Branch:               "main",
				AuthMethod:           v1alpha1.BasicAuthMethod,
				GitAuthor:            v1alpha1.GitAuthor{Name: "kratix", Email: "kratix@example.com"},
			}, "", map[string][]byte{"username": []byte("u"), "password": []byte("p")})
			Expect(err).NotTo(HaveOccurred())
			_, err = writer.Init("main")
			Expect(err).NotTo(HaveOccurred())
			repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{Writer: delivery.Direct(writer)}, nil)
		}

		deleteAndReconcile := func() error {
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
			Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())
			_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
			return err
		}

		expectGone := func() {
			err := fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &v1alpha1.WorkPlacement{})
			Expect(errors.IsNotFound(err)).To(BeTrue(), "work placement should be gone")
		}

		It("removes a nestedByMetadata directory from git in a commit", func() {
			destination.Spec.Filepath.Mode = v1alpha1.FilepathModeNestedByMetadata
			useGitRemote()
			dir := "test-path/resources/default/test-promise/test-resource/5058f/"

			_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
			Expect(err).NotTo(HaveOccurred())
			Expect(gitFiles(bare)).To(ContainElements(dir+"fruit.yaml", dir+"file2.yaml"))

			err = deleteAndReconcile()

			Expect(gitFiles(bare)).To(ConsistOf("seed.txt"))
			Expect(gitOutput(bare, "log", "-1", "--format=%s", "main")).To(Equal("Delete from: " + workPlacementName))
			Expect(err).NotTo(HaveOccurred())
			expectGone()
		})

		It("removes the listed files and the state file from git when filepath mode is none", func() {
			useGitRemote()

			_, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
			Expect(err).NotTo(HaveOccurred())
			Expect(gitFiles(bare)).To(ConsistOf("seed.txt", "test-path/fruit.yaml", "test-path/file2.yaml",
				"test-path/.kratix/default-"+workPlacementName+".yaml"))

			err = deleteAndReconcile()

			Expect(gitFiles(bare)).To(ConsistOf("seed.txt"))
			Expect(gitOutput(bare, "log", "-1", "--format=%s", "main")).To(Equal("Delete from: " + workPlacementName))
			Expect(err).NotTo(HaveOccurred())
			expectGone()
		})

		It("releases a WorkPlacement deleted before its first write when filepath mode is none", func() {
			useGitRemote()
			workPlacement.SetFinalizers([]string{
				"finalizers.workplacement.kratix.io/repo-cleanup",
				"finalizers.workplacement.kratix.io/kratix-dot-files-cleanup",
			})
			Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())

			err := deleteAndReconcile()

			Expect(err).NotTo(HaveOccurred())
			Expect(gitFiles(bare)).To(ConsistOf("seed.txt"))
			expectGone()
		})

		It("removes every object under a nestedByMetadata directory from a bucket", func() {
			bucket := newFakeBucket()
			dir := "test-path/resources/default/test-promise/test-resource/5058f/"
			bucket.objects[dir+"fruit.yaml"] = []byte("a: 1")
			bucket.objects[dir+"nested/file2.yaml"] = []byte("b: 2")
			bucket.objects["test-path/other/keep.yaml"] = []byte("c: 3")

			destination.Spec.Filepath.Mode = v1alpha1.FilepathModeNestedByMetadata
			destination.Spec.StateStoreRef.Kind = "BucketStateStore"
			destination.Spec.StateStoreRef.Name = "test-state-store"
			Expect(fakeK8sClient.Create(ctx, &destination)).To(Succeed())
			writer, err := writers.NewS3Writer(ctrl.Log, v1alpha1.BucketStateStoreSpec{
				Endpoint:   bucket.endpoint,
				Insecure:   true,
				BucketName: "kratix",
				AuthMethod: writers.AuthMethodAccessKey,
			}, "", map[string][]byte{"accessKeyID": []byte("a"), "secretAccessKey": []byte("s")})
			Expect(err).NotTo(HaveOccurred())
			repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{Writer: delivery.Direct(writer)}, nil)

			workPlacement.SetFinalizers([]string{"finalizers.workplacement.kratix.io/repo-cleanup"})
			Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
			Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())

			_, err = t.reconcileUntilCompletion(reconciler, &workPlacement)

			Expect(bucket.keys()).To(ConsistOf("test-path/other/keep.yaml"))
			Expect(err).NotTo(HaveOccurred())
			expectGone()
		})
	})

	When("the destination statestore kind is not managed by Kratix", func() {
		BeforeEach(func() {
			destination.Spec.StateStoreRef.APIVersion = "platform.example.com/v1alpha1"
			destination.Spec.StateStoreRef.Kind = "ExampleStateStore"
			destination.Spec.StateStoreRef.Name = "test-state-store"
			Expect(fakeK8sClient.Create(ctx, &destination)).To(Succeed())

			workPlacement.SetFinalizers([]string{"finalizers.example.com/cleanup"})
			Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
			Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())
		})

		It("leaves the work placement alone", func() {
			result, err := t.reconcileUntilCompletion(reconciler, &workPlacement)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			Expect(repositoryCache.GetRepositoryByTypeAndNameCallCount()).To(Equal(0))
			Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(0))
			Expect(fakeWriter.DeleteFilesCallCount()).To(Equal(0))

			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
			Expect(workPlacement.Status.Conditions).To(BeEmpty())
			Expect(workPlacement.GetFinalizers()).To(ConsistOf("finalizers.example.com/cleanup"))
		})
	})

	When("the state store has no delivery mode", func() {
		reconcileOnce := func() (ctrl.Result, error) {
			return reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: client.ObjectKeyFromObject(&workPlacement),
			})
		}

		writeEvents := func() []string {
			var written []string
			for {
				select {
				case event := <-workplacementRecorder.Events:
					if strings.Contains(event, "WorkloadsWrittenToStateStore") {
						written = append(written, event)
					}
				default:
					return written
				}
			}
		}

		readCondition := func(conditionType string) *metav1.Condition {
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
			return apiMeta.FindStatusCondition(workPlacement.Status.Conditions, conditionType)
		}

		BeforeEach(func() {
			reconciler.StateStoreReconciliationInterval = 7 * time.Minute
			destination.Spec.Filepath.Mode = v1alpha1.FilepathModeNestedByMetadata
			destination.Spec.StateStoreRef.Kind = "BucketStateStore"
			destination.Spec.StateStoreRef.Name = "test-state-store"
			Expect(fakeK8sClient.Create(ctx, &destination)).To(Succeed())

			By("adding the finalizers, which happens before any write")
			_, err := reconcileOnce()
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(0))
		})

		It("records the write once and requeues after the reconciliation interval", func() {
			fakeWriter.UpdateFilesReturns("version-1", nil)

			result, err := reconcileOnce()

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{RequeueAfter: 7 * time.Minute}))
			Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(1))
			Expect(writeEvents()).To(ConsistOf(ContainSubstring(
				"successfully written to Destination: test-destination with versionID: version-1")))
			Expect(collectWorkPlacementWriteMetrics(ctx, metricsReader)).To(Equal(
				map[string]int64{telemetry.WorkPlacementWriteResultSuccess: 1}))
			Expect(reconciler.VersionCache).To(BeEmpty())
			Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionTrue))
			Expect(workPlacement.Status.VersionID).To(Equal("version-1"))
		})

		It("stops reporting an earlier generation's failure while the new write runs", func() {
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
			workPlacement.SetWriteFailedCondition(fmt.Errorf("push rejected"))
			Expect(fakeK8sClient.Status().Update(ctx, &workPlacement)).To(Succeed())
			workPlacement.SetGeneration(workPlacement.GetGeneration() + 1)
			Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())

			var duringWrite *metav1.Condition
			fakeWriter.UpdateFilesStub = func(string, string, []v1alpha1.Workload, []string) (string, error) {
				duringWrite = readCondition(v1alpha1.WriteSucceededConditionType)
				return "", nil
			}

			_, err := reconcileOnce()

			Expect(err).NotTo(HaveOccurred())
			Expect(duringWrite.Status).To(Equal(metav1.ConditionUnknown))
			Expect(duringWrite.Reason).To(Equal("WorkloadsAwaitingDelivery"))
			Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionTrue))
		})

		It("writes directly once the delivery mode is removed while a delivery was outstanding", func() {
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
			workPlacement.Status.DeliveryReceipt = "receipt-from-a-removed-mode"
			workPlacement.Status.DeliveryGeneration = workPlacement.GetGeneration()
			Expect(fakeK8sClient.Status().Update(ctx, &workPlacement)).To(Succeed())

			_, err := reconcileOnce()

			Expect(err).NotTo(HaveOccurred())
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
			Expect(workPlacement.Status.DeliveryReceipt).To(BeEmpty())
			Expect(workPlacement.Status.DeliveryGeneration).To(BeZero())

			By("writing through the state store's own writer on the next reconcile")
			_, err = reconcileOnce()

			Expect(err).NotTo(HaveOccurred())
			Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(1))
		})
	})

	When("the state store writer defers its writes", func() {
		var deferredWriter *fakeDeferredWriter

		reconcileTimes := func(times int) ctrl.Result {
			var result ctrl.Result
			for range times {
				var err error
				result, err = reconciler.Reconcile(ctx, ctrl.Request{
					NamespacedName: types.NamespacedName{
						Name:      workPlacement.GetName(),
						Namespace: workPlacement.GetNamespace(),
					},
				})
				Expect(err).NotTo(HaveOccurred())
			}
			return result
		}

		readCondition := func(conditionType string) *metav1.Condition {
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
			return apiMeta.FindStatusCondition(workPlacement.Status.Conditions, conditionType)
		}

		removalsDelivered := func() []deliverCall {
			var found []deliverCall
			for _, call := range deferredWriter.delivered {
				if call.target.Removing {
					found = append(found, call)
				}
			}
			return found
		}

		BeforeEach(func() {
			deferredWriter = &fakeDeferredWriter{FakeStateStoreWriter: &writersfakes.FakeStateStoreWriter{}}
			deferredWriter.deliverResult = delivery.Result{
				Outcome: delivery.Pending,
				Receipt: "receipt-1",
				Reason:  "waiting for the write to be accepted",
				URL:     "https://h/pr/7",
			}
			repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{Writer: deferredWriter}, nil)

			destination.Spec.Filepath.Mode = v1alpha1.FilepathModeNestedByMetadata
			destination.Spec.StateStoreRef.Kind = "BucketStateStore"
			destination.Spec.StateStoreRef.Name = "test-state-store"
			Expect(fakeK8sClient.Create(ctx, &destination)).To(Succeed())
		})

		When("an earlier generation was rejected", func() {
			BeforeEach(func() {
				reconcileTimes(2)
				deferredWriter.checkResult = delivery.Result{Outcome: delivery.Rejected, Reason: "closed without merging"}
				reconcileTimes(1)

				Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionFalse))

				By("changing the workloads, as an update to the request does")
				updated, err := compression.CompressContent([]byte("{new: true}"))
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				workPlacement.Spec.Workloads = []v1alpha1.Workload{{Filepath: "new.yaml", Content: string(updated)}}
				workPlacement.SetGeneration(workPlacement.GetGeneration() + 1)
				Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
				deferredWriter.checkResult = delivery.Result{}
			})

			It("stops reporting the old failure while the new workloads are delivered", func() {
				deferredWriter.deliverResult = delivery.Result{Outcome: delivery.Pending, Receipt: "receipt-2"}
				var duringDelivery metav1.ConditionStatus
				deferredWriter.onDeliver = func() {
					if written := readCondition(v1alpha1.WriteSucceededConditionType); written != nil {
						duringDelivery = written.Status
					}
				}

				reconcileTimes(1)

				Expect(deferredWriter.deliverCalls).To(BeNumerically(">", 0))
				Expect(duringDelivery).NotTo(Equal(metav1.ConditionFalse),
					"a verdict on superseded workloads reads as a current failure for the whole delivery")
			})
		})

		It("records the delivery mode before the first delivery", func() {
			repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{Writer: deferredWriter, DeliveryMode: "pullRequest"}, nil)
			var modeDuringDelivery *string
			deferredWriter.onDeliver = func() {
				latest := &v1alpha1.WorkPlacement{}
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), latest)).To(Succeed())
				modeDuringDelivery = latest.Status.DeliveryMode
			}

			reconcileTimes(2)

			Expect(deferredWriter.deliverCalls).To(Equal(1))
			Expect(modeDuringDelivery).To(Equal(ptr.To("pullRequest")))
		})

		It("keeps the receipt when a concurrent write conflicts, since the delivery exists", func() {
			errSubResourceUpdateOnce = errors.NewConflict(
				schema.GroupResource{Group: "platform.kratix.io", Resource: "workplacements"},
				workPlacement.GetName(), fmt.Errorf("the object has been modified"))
			DeferCleanup(func() { errSubResourceUpdateOnce = nil })

			reconcileTimes(2)

			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
			Expect(workPlacement.Status.DeliveryReceipt).To(Equal("receipt-1"))
			Expect(workPlacement.Status.DeliveryURL).To(Equal("https://h/pr/7"))
		})

		It("names the WorkPlacement the delivery is for, so a writer can explain it", func() {
			reconcileTimes(2)

			Expect(deferredWriter.deliverTargets).To(ConsistOf(delivery.Target{
				Dir:           "test-path/resources/default/test-promise/test-resource/5058f/",
				Namespace:     "default",
				WorkPlacement: workPlacement.GetName(),
				Promise:       "test-promise",
				Resource:      "test-resource",
				Pipeline:      workPlacement.PipelineName(),
				Destination:   "test-destination",
			}))
		})

		It("delivers the write and records the receipt it was given", func() {
			result := reconcileTimes(2)

			Expect(deferredWriter.deliverCalls).To(Equal(1))
			Expect(deferredWriter.UpdateFilesCallCount()).To(Equal(0))

			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
			Expect(workPlacement.Status.DeliveryReceipt).To(Equal("receipt-1"))
			Expect(workPlacement.Status.VersionID).To(BeEmpty())
			Expect(workPlacement.Status.DeliveryURL).To(Equal("https://h/pr/7"))

			By("leaving the write unresolved rather than failed, so the Work stays pending")
			Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionUnknown))
			Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsAwaitingDelivery"))
			Expect(readCondition("Ready").Status).To(Equal(metav1.ConditionFalse))
			Expect(readCondition("Ready").Message).To(Equal("Pending"))

			By("requeueing after a shorter interval")
			Expect(result.RequeueAfter).To(Equal(30 * time.Second))
		})

		It("checks a pending write again after the CheckAfter the writer returned", func() {
			deferredWriter.deliverResult.CheckAfter = 5 * time.Second

			result := reconcileTimes(2)

			Expect(result.RequeueAfter).To(Equal(5 * time.Second))
		})

		When("the workloads were already written with a delivery URL", func() {
			BeforeEach(func() {
				deferredWriter.deliverResult = delivery.Result{
					Outcome: delivery.Delivered, VersionID: "sha-1", URL: "https://old",
				}
				reconcileTimes(2)
				Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionTrue))
				Expect(workPlacement.Status.VersionID).To(Equal("sha-1"))
				Expect(workPlacement.Status.DeliveryURL).To(Equal("https://old"))
			})

			It("clears the URL when the writer delivers again without one", func() {
				deferredWriter.deliverResult.URL = ""

				reconcileTimes(1)

				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				Expect(workPlacement.Status.DeliveryURL).To(BeEmpty())
			})

			It("clears the URL on a direct write", func() {
				fakeWriter.UpdateFilesReturns("sha-1", nil)
				repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{Writer: delivery.Direct(fakeWriter)}, nil)

				reconcileTimes(1)

				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				Expect(workPlacement.Status.DeliveryURL).To(BeEmpty())
			})
		})

		When("a write is already pending", func() {
			BeforeEach(func() {
				reconcileTimes(2)
			})

			It("verifies the pending write instead of delivering again", func() {
				deferredWriter.checkResult = delivery.Result{Outcome: delivery.Pending, Receipt: "receipt-1"}

				reconcileTimes(1)

				Expect(deferredWriter.checkReceipts).To(ConsistOf("receipt-1"))
				Expect(deferredWriter.deliverCalls).To(Equal(1))
			})

			It("checks a pending write again after the CheckAfter the writer returned", func() {
				deferredWriter.checkResult = delivery.Result{
					Outcome: delivery.Pending, Receipt: "receipt-1", CheckAfter: 5 * time.Second,
				}

				result := reconcileTimes(1)

				Expect(result.RequeueAfter).To(Equal(5 * time.Second))
			})

			It("records a rejection with its reason and URL", func() {
				deferredWriter.checkResult = delivery.Result{
					Outcome: delivery.Rejected,
					Reason:  `Rejected in https://h/pr/8: "not this quarter"`,
					URL:     "https://h/pr/8",
				}

				reconcileTimes(1)

				written := readCondition(v1alpha1.WriteSucceededConditionType)
				Expect(written.Status).To(Equal(metav1.ConditionFalse))
				Expect(written.Reason).To(Equal("WorkloadsRejected"))
				Expect(written.Message).To(Equal(`Rejected in https://h/pr/8: "not this quarter"`))
				Expect(readCondition("Ready").Reason).To(Equal("WorkloadsRejected"))
				Expect(readCondition("Ready").Message).To(Equal("Failing"))
				Expect(workPlacement.Status.DeliveryReceipt).To(BeEmpty())
				Expect(workPlacement.Status.DeliveryURL).To(Equal("https://h/pr/8"))
			})

			It("keeps the rejection's URL on a conflict", func() {
				deferredWriter.checkResult = delivery.Result{
					Outcome: delivery.Rejected, Reason: "closed without merging", URL: "https://h/pr/8",
				}
				errSubResourceUpdateOnce = errors.NewConflict(
					schema.GroupResource{Group: "platform.kratix.io", Resource: "workplacements"},
					workPlacement.GetName(), fmt.Errorf("the object has been modified"))
				DeferCleanup(func() { errSubResourceUpdateOnce = nil })

				reconcileTimes(1)

				Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsRejected"))
				Expect(workPlacement.Status.DeliveryURL).To(Equal("https://h/pr/8"))
			})

			It("records a new URL from a pending check", func() {
				deferredWriter.checkResult = deferredWriter.deliverResult
				deferredWriter.checkResult.URL = "https://h/pr/9"

				reconcileTimes(1)

				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				Expect(workPlacement.Status.DeliveryURL).To(Equal("https://h/pr/9"))
			})

			It("cuts a long reason to 1024 characters", func() {
				deferredWriter.checkResult = deferredWriter.deliverResult
				deferredWriter.checkResult.Reason = strings.Repeat("é", 2000)

				reconcileTimes(1)

				message := readCondition(v1alpha1.WriteSucceededConditionType).Message
				Expect(utf8.RuneCountInString(message)).To(Equal(1024))
				Expect(utf8.ValidString(message)).To(BeTrue())
				Expect(message).To(HaveSuffix("…"))
			})

			It("cuts a long rejection reason too", func() {
				deferredWriter.checkResult = delivery.Result{Outcome: delivery.Rejected, Reason: strings.Repeat("x", 40000)}

				reconcileTimes(1)

				Expect(utf8.RuneCountInString(readCondition(v1alpha1.WriteSucceededConditionType).Message)).To(Equal(1024))
			})

			It("forgets the URL when the delivery mode is removed", func() {
				repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{Writer: delivery.Direct(fakeWriter)}, nil)

				reconcileTimes(1)

				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				Expect(workPlacement.Status.DeliveryReceipt).To(BeEmpty())
				Expect(workPlacement.Status.DeliveryURL).To(BeEmpty())
			})

			It("fails the work placement when the writer rejects the write", func() {
				deferredWriter.checkResult = delivery.Result{Outcome: delivery.Rejected, Reason: "closed without merging"}

				reconcileTimes(1)

				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				Expect(workPlacement.Status.DeliveryReceipt).To(BeEmpty())
				Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionFalse))
				Expect(readCondition(v1alpha1.WriteSucceededConditionType).Message).To(ContainSubstring("closed without merging"))
			})

			When("the writer reports 'Delivered' for a previously pending request", func() {
				It("records the version the writer reports", func() {
					deferredWriter.checkResult = delivery.Result{
						Outcome: delivery.Delivered, VersionID: "merge-sha", URL: "https://h/pr/7/merged",
					}

					reconcileTimes(1)

					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					Expect(workPlacement.Status.VersionID).To(Equal("merge-sha"))
					Expect(workPlacement.Status.DeliveryURL).To(Equal("https://h/pr/7/merged"))
					Expect(workPlacement.Status.DeliveryReceipt).To(BeEmpty())
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionTrue))
				})
			})

			When("the workloads are updated", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					workPlacement.Spec.Workloads = []v1alpha1.Workload{{Filepath: "new.yaml", Content: "new: true"}}
					workPlacement.SetGeneration(workPlacement.GetGeneration() + 1)
					Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
				})

				It("discards the pending write rather than crediting it", func() {
					deferredWriter.checkResult = delivery.Result{Outcome: delivery.Delivered, VersionID: "stale-merge"}

					reconcileTimes(1)

					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					Expect(workPlacement.Status.VersionID).NotTo(Equal("stale-merge"))
					Expect(workPlacement.Status.DeliveryReceipt).To(BeEmpty())
				})
			})

			When("a pipeline run produces the same workloads before the check", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					workPlacement.SetGeneration(workPlacement.GetGeneration() + 1)
					Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
				})

				It("still records the rejection instead of delivering again", func() {
					deferredWriter.checkResult = delivery.Result{Outcome: delivery.Rejected, Reason: "closed without merging"}

					reconcileTimes(2)

					Expect(deferredWriter.checkReceipts).To(ConsistOf("receipt-1"))
					Expect(deferredWriter.deliverCalls).To(Equal(1))
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsRejected"))
				})
			})

			When("the workloads move to a newer generation during the check", func() {
				var deliveredGeneration int64

				moveToNewerGeneration := func(workloads []v1alpha1.Workload) {
					deferredWriter.onCheck = func() {
						latest := &v1alpha1.WorkPlacement{}
						Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), latest)).To(Succeed())
						if workloads != nil {
							latest.Spec.Workloads = workloads
						}
						latest.SetGeneration(latest.GetGeneration() + 1)
						Expect(fakeK8sClient.Update(ctx, latest)).To(Succeed())
					}
				}

				BeforeEach(func() {
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					deliveredGeneration = workPlacement.GetGeneration()
				})

				It("records a pending write against the generation it delivered", func() {
					moveToNewerGeneration(nil)
					deferredWriter.checkResult = delivery.Result{Outcome: delivery.Pending, Receipt: "receipt-1"}

					reconcileTimes(1)

					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					Expect(workPlacement.Status.DeliveryReceipt).To(Equal("receipt-1"))
					Expect(workPlacement.Status.DeliveryGeneration).To(Equal(deliveredGeneration))
				})

				It("drops a rejection when the newer workloads differ", func() {
					moveToNewerGeneration([]v1alpha1.Workload{{Filepath: "new.yaml", Content: "new: true"}})
					deferredWriter.checkResult = delivery.Result{Outcome: delivery.Rejected, Reason: "closed without merging"}

					reconcileTimes(1)

					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).NotTo(Equal("WorkloadsRejected"))
				})

				It("keeps a rejection when the newer workloads are the same", func() {
					moveToNewerGeneration(nil)
					deferredWriter.checkResult = delivery.Result{Outcome: delivery.Rejected, Reason: "closed without merging"}

					reconcileTimes(1)

					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsRejected"))
				})
			})
		})

		When("a delivery was rejected", func() {
			BeforeEach(func() {
				reconcileTimes(2)
				deferredWriter.checkResult = delivery.Result{Outcome: delivery.Rejected, Reason: "closed without merging"}
				reconcileTimes(1)
				Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsRejected"))
				Expect(deferredWriter.deliverCalls).To(Equal(1))
			})

			bumpGeneration := func() {
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				workPlacement.SetGeneration(workPlacement.GetGeneration() + 1)
				Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
			}

			It("does not deliver the same workloads again after a restart", func() {
				result := reconcileTimes(1)

				Expect(deferredWriter.deliverCalls).To(Equal(1))
				Expect(result).To(Equal(ctrl.Result{}))
			})

			It("keeps the rejection when a pipeline run produces the same workloads", func() {
				bumpGeneration()

				reconcileTimes(1)

				Expect(deferredWriter.deliverCalls).To(Equal(1))
				written := readCondition(v1alpha1.WriteSucceededConditionType)
				Expect(written.Reason).To(Equal("WorkloadsRejected"))
				Expect(written.Message).To(Equal("closed without merging"))
				Expect(written.ObservedGeneration).To(Equal(workPlacement.GetGeneration()))
			})

			It("keeps the rejection while the Destination is not ready", func() {
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&destination), &destination)).To(Succeed())
				destination.SetNotReadyStatus("StateStoreNotReady", "permission denied")
				Expect(fakeK8sClient.Status().Update(ctx, &destination)).To(Succeed())

				reconcileTimes(1)

				Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsRejected"))
				Expect(deferredWriter.deliverCalls).To(Equal(1))
			})

			changeFirstFileContent := func() {
				updated, err := compression.CompressContent([]byte("{someApi: foo, someValue: changed}"))
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				workPlacement.Spec.Workloads[0].Content = string(updated)
				workPlacement.SetGeneration(workPlacement.GetGeneration() + 1)
				Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())
			}

			It("delivers again when a file's content changes", func() {
				changeFirstFileContent()

				reconcileTimes(1)

				Expect(deferredWriter.deliverCalls).To(Equal(2))
				Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).NotTo(Equal("WorkloadsRejected"))
			})

			It("delivers again when a file is renamed", func() {
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				workPlacement.Spec.Workloads[0].Filepath = "renamed.yaml"
				workPlacement.SetGeneration(workPlacement.GetGeneration() + 1)
				Expect(fakeK8sClient.Update(ctx, &workPlacement)).To(Succeed())

				reconcileTimes(1)

				Expect(deferredWriter.deliverCalls).To(Equal(2))
			})

			When("the rejection was recorded before Kratix stored the workloads hash", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					workPlacement.Status.DeliveryWorkloadsHash = ""
					Expect(fakeK8sClient.Status().Update(ctx, &workPlacement)).To(Succeed())
				})

				It("keeps the rejection for the workloads at that generation", func() {
					reconcileTimes(1)

					Expect(deferredWriter.deliverCalls).To(Equal(1))
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
					Expect(workPlacement.Status.DeliveryWorkloadsHash).NotTo(BeEmpty())

					bumpGeneration()
					reconcileTimes(1)
					Expect(deferredWriter.deliverCalls).To(Equal(1))
				})

				It("delivers when the rejection is for an older generation", func() {
					bumpGeneration()

					reconcileTimes(1)

					Expect(deferredWriter.deliverCalls).To(Equal(2))
				})
			})

			It("forgets the rejection once the changed workloads are delivered", func() {
				changeFirstFileContent()
				deferredWriter.deliverResult = delivery.Result{Outcome: delivery.Delivered, VersionID: "v2"}

				reconcileTimes(1)

				Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionTrue))
				Expect(workPlacement.Status.DeliveryWorkloadsHash).To(BeEmpty())
			})

			It("delivers the removal when the WorkPlacement is deleted, and releases it once delivered", func() {
				deferredWriter.deliverResult = delivery.Result{Outcome: delivery.Delivered}
				deferredWriter.DeleteFilesReturns(fmt.Errorf("deletes go through Deliver"))
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				Expect(fakeK8sClient.Delete(ctx, &workPlacement)).To(Succeed())

				reconcileTimes(1)

				Expect(removalsDelivered()).To(HaveLen(1))
				Expect(deferredWriter.DeleteFilesCallCount()).To(BeZero())
				err := fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &v1alpha1.WorkPlacement{})
				Expect(errors.IsNotFound(err)).To(BeTrue(), "work placement should be gone")
			})
		})

		When("the WorkPlacement is deleted", func() {
			const repoCleanup = "finalizers.workplacement.kratix.io/repo-cleanup"

			pendingRemoval := delivery.Result{
				Outcome:    delivery.Pending,
				Receipt:    "removal-1",
				Reason:     "Waiting for approval to remove: https://h/pr/9",
				URL:        "https://h/pr/9",
				CheckAfter: 7 * time.Second,
			}

			read := func(name string) *v1alpha1.WorkPlacement {
				latest := &v1alpha1.WorkPlacement{}
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, latest)).To(Succeed())
				return latest
			}

			deleteNamed := func(name string) {
				Expect(fakeK8sClient.Delete(ctx, read(name))).To(Succeed())
			}

			reconcileNamed := func(name string, times int) ctrl.Result {
				var result ctrl.Result
				for range times {
					var err error
					result, err = reconciler.Reconcile(ctx, ctrl.Request{
						NamespacedName: types.NamespacedName{Name: name, Namespace: "default"},
					})
					Expect(err).NotTo(HaveOccurred())
				}
				return result
			}

			bumpGenerationAsTheAPIServerDoes := func() {
				latest := read(workPlacementName)
				latest.SetGeneration(latest.GetGeneration() + 1)
				Expect(fakeK8sClient.Update(ctx, latest)).To(Succeed())
			}

			isGone := func(name string) bool {
				err := fakeK8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &v1alpha1.WorkPlacement{})
				return errors.IsNotFound(err)
			}

			deliverThenDelete := func() {
				deferredWriter.deliverResult = delivery.Result{Outcome: delivery.Delivered, VersionID: "v1"}
				reconcileTimes(2)
				Expect(read(workPlacementName).Status.DeliveryReceipt).To(BeEmpty())
				deleteNamed(workPlacementName)
			}

			It("keeps the finalizer and records a pending removal", func() {
				deliverThenDelete()
				deferredWriter.deliverResult = pendingRemoval

				result := reconcileTimes(1)

				Expect(removalsDelivered()).To(HaveLen(1))
				Expect(deferredWriter.DeleteFilesCallCount()).To(BeZero())
				latest := read(workPlacementName)
				Expect(latest.GetFinalizers()).To(ContainElement(repoCleanup))
				Expect(latest.Status.DeliveryReceipt).To(Equal("removal-1"))
				Expect(latest.Status.DeliveryRemoval).To(BeTrue())
				Expect(latest.Status.DeliveryURL).To(Equal("https://h/pr/9"))
				Expect(latest.Status.DeliveryMode).To(Equal(ptr.To("")))
				written := apiMeta.FindStatusCondition(latest.Status.Conditions, v1alpha1.WriteSucceededConditionType)
				Expect(written.Status).To(Equal(metav1.ConditionUnknown))
				Expect(written.Message).To(Equal("Waiting for approval to remove: https://h/pr/9"))
				Expect(result.RequeueAfter).To(Equal(7 * time.Second))
			})

			It("drops an update's receipt, and saves that, before delivering the removal once", func() {
				reconcileTimes(2)
				Expect(read(workPlacementName).Status.DeliveryReceipt).To(Equal("receipt-1"))
				deleteNamed(workPlacementName)
				deferredWriter.deliverResult = pendingRemoval
				var duringRemoval v1alpha1.WorkPlacementStatus
				deferredWriter.onDeliver = func() { duringRemoval = read(workPlacementName).Status }

				reconcileTimes(2)

				Expect(deferredWriter.deliverCalls).To(Equal(2), "one update, one removal")
				Expect(removalsDelivered()).To(HaveLen(1))
				Expect(deferredWriter.checkReceipts).NotTo(ContainElement("receipt-1"))
				Expect(duringRemoval.DeliveryReceipt).To(BeEmpty())
				written := apiMeta.FindStatusCondition(duringRemoval.Conditions, v1alpha1.WriteSucceededConditionType)
				Expect(written).NotTo(BeNil())
				Expect(written.Message).To(Equal("removing the workloads"))
				Expect(read(workPlacementName).Status.DeliveryReceipt).To(Equal("removal-1"))
			})

			It("names the update's receipt it dropped as superseded by the removal", func() {
				deferredWriter.deliverResult.Receipt = "42"
				reconcileTimes(2)
				Expect(read(workPlacementName).Status.DeliveryReceipt).To(Equal("42"))
				deleteNamed(workPlacementName)
				deferredWriter.deliverResult = pendingRemoval

				reconcileTimes(1)

				Expect(removalsDelivered()).To(HaveLen(1))
				Expect(removalsDelivered()[0].target.Supersedes).To(Equal("42"))
			})

			It("supersedes nothing when no delivery was outstanding", func() {
				deliverThenDelete()
				deferredWriter.deliverResult = pendingRemoval

				reconcileTimes(1)

				Expect(removalsDelivered()).To(HaveLen(1))
				Expect(removalsDelivered()[0].target.Supersedes).To(BeEmpty())
			})

			It("saves an update that comes back after deletion started as an update, then removes", func() {
				deleted := false
				deferredWriter.onDeliver = func() {
					if !deleted {
						deleted = true
						deleteNamed(workPlacementName)
					}
				}

				reconcileTimes(2)

				latest := read(workPlacementName)
				Expect(latest.DeletionTimestamp.IsZero()).To(BeFalse())
				Expect(latest.Status.DeliveryReceipt).To(Equal("receipt-1"))
				Expect(latest.Status.DeliveryRemoval).To(BeFalse())

				deferredWriter.deliverResult = pendingRemoval
				reconcileTimes(1)

				Expect(removalsDelivered()).To(HaveLen(1))
				Expect(read(workPlacementName).Status.DeliveryReceipt).To(Equal("removal-1"))
				Expect(read(workPlacementName).Status.DeliveryRemoval).To(BeTrue())
			})

			It("leaves a removal receipt saved since its copy was read", func() {
				reconcileTimes(2)
				deleteNamed(workPlacementName)
				stale := read(workPlacementName)
				Expect(stale.Status.DeliveryReceipt).To(Equal("receipt-1"))

				saved := read(workPlacementName)
				saved.Status.DeliveryReceipt = "removal-1"
				saved.Status.DeliveryRemoval = true
				Expect(fakeK8sClient.Status().Update(ctx, saved)).To(Succeed())

				servedStale := false
				reconciler.Client = interceptor.NewClient(fakeK8sClient.(client.WithWatch), interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if wp, ok := obj.(*v1alpha1.WorkPlacement); ok && !servedStale {
							servedStale = true
							stale.DeepCopyInto(wp)
							return nil
						}
						return c.Get(ctx, key, obj, opts...)
					},
				})

				reconcileTimes(1)

				Expect(servedStale).To(BeTrue())
				Expect(deferredWriter.deliverCalls).To(Equal(1), "only the update")
				Expect(read(workPlacementName).Status.DeliveryReceipt).To(Equal("removal-1"))
				Expect(read(workPlacementName).Status.DeliveryRemoval).To(BeTrue())

				By("checking the saved removal on the next reconcile")
				reconcileTimes(1)
				Expect(deferredWriter.checkReceipts).To(Equal([]string{"removal-1"}))
				Expect(deferredWriter.deliverCalls).To(Equal(1))
			})

			When("the removal is rejected", func() {
				BeforeEach(func() {
					deliverThenDelete()
					deferredWriter.deliverResult = delivery.Result{
						Outcome: delivery.Rejected, Reason: "closed by a reviewer", URL: "https://h/pr/9",
					}
					reconcileTimes(1)
				})

				It("keeps the finalizer and says the workloads are still on the Destination", func() {
					latest := read(workPlacementName)
					Expect(latest.GetFinalizers()).To(ContainElement(repoCleanup))
					Expect(latest.Status.DeliveryRemoval).To(BeTrue())
					Expect(latest.Status.DeliveryReceipt).To(BeEmpty())
					written := apiMeta.FindStatusCondition(latest.Status.Conditions, v1alpha1.WriteSucceededConditionType)
					Expect(written.Status).To(Equal(metav1.ConditionFalse))
					Expect(written.Reason).To(Equal("WorkloadsRejected"))
					Expect(written.Message).To(Equal(
						"Removal rejected; the workloads are still on test-destination. closed by a reviewer"))
				})

				It("neither delivers nor checks again", func() {
					delivered := deferredWriter.deliverCalls

					result := reconcileTimes(2)

					Expect(deferredWriter.deliverCalls).To(Equal(delivered))
					Expect(deferredWriter.checkReceipts).To(BeEmpty())
					Expect(result).To(Equal(ctrl.Result{}))
					Expect(read(workPlacementName).GetFinalizers()).To(ContainElement(repoCleanup))
				})
			})

			It("keeps Kratix's sentence when the rejection reason is long", func() {
				deliverThenDelete()
				deferredWriter.deliverResult = delivery.Result{Outcome: delivery.Rejected, Reason: strings.Repeat("x", 2000)}

				reconcileTimes(1)

				written := readCondition(v1alpha1.WriteSucceededConditionType)
				Expect(written.Message).To(HavePrefix("Removal rejected; the workloads are still on test-destination. xxx"))
				Expect(utf8.RuneCountInString(written.Message)).To(BeNumerically("<=", 1024))
			})

			When("a removal is pending", func() {
				BeforeEach(func() {
					deliverThenDelete()
					deferredWriter.deliverResult = pendingRemoval
					reconcileTimes(1)
					Expect(read(workPlacementName).Status.DeliveryRemoval).To(BeTrue())
					bumpGenerationAsTheAPIServerDoes()
				})

				It("releases the WorkPlacement once the check says delivered, without delivering again", func() {
					deferredWriter.checkResult = delivery.Result{Outcome: delivery.Delivered, VersionID: "merge-sha"}

					reconcileTimes(1)

					Expect(deferredWriter.checkReceipts).To(Equal([]string{"removal-1"}))
					Expect(removalsDelivered()).To(HaveLen(1))
					Expect(isGone(workPlacementName)).To(BeTrue())
				})

				It("drops a receipt the writer does not recognise and delivers the removal again", func() {
					deferredWriter.checkErr = fmt.Errorf("%w: %q", delivery.ErrUnknownReceipt, "removal-1")
					deferredWriter.deliverResult.Receipt = "removal-2"

					reconcileTimes(1)

					Expect(deferredWriter.checkReceipts).To(Equal([]string{"removal-1"}))
					Expect(removalsDelivered()).To(HaveLen(2))
					Expect(read(workPlacementName).Status.DeliveryReceipt).To(Equal("removal-2"))
					Expect(read(workPlacementName).Status.DeliveryRemoval).To(BeTrue())
				})

				It("records a rejection the check reports, with its URL and without the receipt", func() {
					deferredWriter.checkResult = delivery.Result{
						Outcome: delivery.Rejected, Reason: "closed by a reviewer", URL: "https://h/pr/10",
					}

					reconcileTimes(1)

					latest := read(workPlacementName)
					Expect(deferredWriter.checkReceipts).To(Equal([]string{"removal-1"}))
					Expect(latest.Status.DeliveryReceipt).To(BeEmpty())
					Expect(latest.Status.DeliveryURL).To(Equal("https://h/pr/10"))
					Expect(latest.Status.DeliveryRemoval).To(BeTrue())
				})

				It("holds no removal when the receipt is unknown and the removal cannot be delivered again", func() {
					deferredWriter.checkErr = fmt.Errorf("%w: %q", delivery.ErrUnknownReceipt, "removal-1")
					deferredWriter.deliverErr = fmt.Errorf("host down")

					reconcileTimes(1)

					latest := read(workPlacementName)
					Expect(removalsDelivered()).To(HaveLen(2))
					Expect(latest.Status.DeliveryReceipt).To(BeEmpty())
					Expect(latest.Status.DeliveryURL).To(BeEmpty())
					Expect(latest.Status.DeliveryRemoval).To(BeFalse())
				})

				It("leaves a newer removal receipt saved since its copy was read when the old receipt is unknown", func() {
					stale := read(workPlacementName)
					saved := read(workPlacementName)
					saved.Status.DeliveryReceipt = "removal-2"
					Expect(fakeK8sClient.Status().Update(ctx, saved)).To(Succeed())
					deferredWriter.checkErr = fmt.Errorf("%w: %q", delivery.ErrUnknownReceipt, "removal-1")

					servedStale := false
					reconciler.Client = interceptor.NewClient(fakeK8sClient.(client.WithWatch), interceptor.Funcs{
						Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
							if wp, ok := obj.(*v1alpha1.WorkPlacement); ok && !servedStale {
								servedStale = true
								stale.DeepCopyInto(wp)
								return nil
							}
							return c.Get(ctx, key, obj, opts...)
						},
					})

					reconcileTimes(1)

					Expect(servedStale).To(BeTrue())
					Expect(deferredWriter.checkReceipts).To(Equal([]string{"removal-1"}))
					Expect(removalsDelivered()).To(HaveLen(1))
					Expect(read(workPlacementName).Status.DeliveryReceipt).To(Equal("removal-2"))
				})
			})

			It("does not deliver the removal again when the API server already holds a rejected removal", func() {
				deliverThenDelete()
				stale := read(workPlacementName)
				deferredWriter.deliverResult = delivery.Result{Outcome: delivery.Rejected, Reason: "closed by a reviewer", URL: "https://h/pr/9"}
				reconcileTimes(1)
				Expect(read(workPlacementName).Status.DeliveryRemoval).To(BeTrue())
				deferredWriter.deliverResult = pendingRemoval

				servedStale := false
				reconciler.Client = interceptor.NewClient(fakeK8sClient.(client.WithWatch), interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if wp, ok := obj.(*v1alpha1.WorkPlacement); ok && !servedStale {
							servedStale = true
							stale.DeepCopyInto(wp)
							return nil
						}
						return c.Get(ctx, key, obj, opts...)
					},
				})

				reconcileTimes(1)

				Expect(servedStale).To(BeTrue())
				Expect(removalsDelivered()).To(HaveLen(1))
				latest := read(workPlacementName)
				Expect(latest.Status.DeliveryReceipt).To(BeEmpty())
				Expect(apiMeta.FindStatusCondition(latest.Status.Conditions, v1alpha1.WriteSucceededConditionType).Reason).
					To(Equal("WorkloadsRejected"))
			})

			It("keeps a rejected removal saved while its own removal was being delivered", func() {
				deliverThenDelete()
				deferredWriter.deliverResult = pendingRemoval
				deferredWriter.onDeliver = func() {
					latest := read(workPlacementName)
					latest.Status.DeliveryRemoval = true
					latest.SetWriteRejectedCondition("Removal rejected; the workloads are still on test-destination. closed")
					Expect(fakeK8sClient.Status().Update(ctx, latest)).To(Succeed())
				}

				reconcileTimes(1)

				latest := read(workPlacementName)
				Expect(removalsDelivered()).To(HaveLen(1))
				Expect(latest.Status.DeliveryReceipt).To(BeEmpty())
				Expect(latest.Status.DeliveryRemoval).To(BeTrue())
				Expect(apiMeta.FindStatusCondition(latest.Status.Conditions, v1alpha1.WriteSucceededConditionType).Reason).
					To(Equal("WorkloadsRejected"))
			})

			When("the removal cannot be delivered", func() {
				BeforeEach(func() {
					reconcileTimes(2)
					Expect(read(workPlacementName).Status.DeliveryURL).To(Equal("https://h/pr/7"))
					deleteNamed(workPlacementName)
					deferredWriter.deliverErr = fmt.Errorf("host down")

					reconcileTimes(1)
				})

				It("fails the write with the writer's error and keeps the finalizer", func() {
					Expect(removalsDelivered()).To(HaveLen(1))
					written := readCondition(v1alpha1.WriteSucceededConditionType)
					Expect(written.Status).To(Equal(metav1.ConditionFalse))
					Expect(written.Reason).To(Equal("WorkloadsFailedWrite"))
					Expect(written.Message).To(ContainSubstring("host down"))
					Expect(read(workPlacementName).GetFinalizers()).To(ContainElement(repoCleanup))
				})

				It("does not keep the update's pull request link", func() {
					Expect(read(workPlacementName).Status.DeliveryURL).To(BeEmpty())
				})
			})

			It("records the delivery mode with a rejected removal of a WorkPlacement that never recorded one", func() {
				repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{Writer: deferredWriter, DeliveryMode: "pullRequest"}, nil)
				reconcileTimes(1)
				Expect(read(workPlacementName).GetFinalizers()).To(ContainElement(repoCleanup))
				Expect(read(workPlacementName).Status.DeliveryMode).To(BeNil())
				deleteNamed(workPlacementName)
				deferredWriter.deliverResult = delivery.Result{Outcome: delivery.Rejected, Reason: "closed", URL: "https://h/pr/11"}

				reconcileTimes(1)

				latest := read(workPlacementName)
				Expect(removalsDelivered()).To(HaveLen(1))
				Expect(latest.Status.DeliveryMode).To(Equal(ptr.To("pullRequest")))
				Expect(latest.Status.DeliveryURL).To(Equal("https://h/pr/11"))
				Expect(latest.Status.DeliveryReceipt).To(BeEmpty())
			})

			It("removes every WorkPlacement of a Work, dropping an update receipt first", func() {
				deferredWriter.deliverResult = delivery.Result{Outcome: delivery.Delivered, VersionID: "v1"}
				reconcileNamed(workPlacementName, 2)

				second := createWorkPlacement(workPlacementName+"-2", workloads)
				Expect(fakeK8sClient.Create(ctx, &second)).To(Succeed())
				deferredWriter.deliverResult = delivery.Result{Outcome: delivery.Pending, Receipt: "receipt-2"}
				reconcileNamed(second.GetName(), 2)
				Expect(read(second.GetName()).Status.DeliveryReceipt).To(Equal("receipt-2"))

				deleteNamed(workPlacementName)
				deleteNamed(second.GetName())
				deferredWriter.deliverResult = pendingRemoval
				var secondDuringRemoval *v1alpha1.WorkPlacementStatus
				deferredWriter.onDeliver = func() {
					latest := read(second.GetName())
					if len(deferredWriter.delivered) > 0 && deferredWriter.delivered[len(deferredWriter.delivered)-1].target.WorkPlacement == second.GetName() {
						secondDuringRemoval = &latest.Status
					}
				}

				reconcileNamed(workPlacementName, 1)
				reconcileNamed(second.GetName(), 1)

				var removed []string
				for _, call := range removalsDelivered() {
					removed = append(removed, call.target.WorkPlacement)
				}
				Expect(removed).To(ConsistOf(workPlacementName, second.GetName()))
				Expect(secondDuringRemoval).NotTo(BeNil())
				Expect(secondDuringRemoval.DeliveryReceipt).To(BeEmpty())
				Expect(deferredWriter.checkReceipts).NotTo(ContainElement("receipt-2"))
				Expect(read(workPlacementName).Status.DeliveryRemoval).To(BeTrue())
				Expect(read(second.GetName()).Status.DeliveryRemoval).To(BeTrue())
			})
		})

		When("the state store's delivery mode changes", func() {
			var newWriter *fakeDeferredWriter

			useRepository := func(writer delivery.Writer, mode string) {
				repositoryCache.GetRepositoryByTypeAndNameReturns(&controller.Repository{Writer: writer, DeliveryMode: mode}, nil)
			}

			modeChangedEvents := func() []string {
				var found []string
				for {
					select {
					case event := <-workplacementRecorder.Events:
						if strings.Contains(event, "DeliveryModeChanged") {
							found = append(found, event)
						}
					default:
						return found
					}
				}
			}

			readStatus := func() v1alpha1.WorkPlacementStatus {
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				return workPlacement.Status
			}

			forgetDeliveryMode := func() {
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&workPlacement), &workPlacement)).To(Succeed())
				workPlacement.Status.DeliveryMode = nil
				Expect(fakeK8sClient.Status().Update(ctx, &workPlacement)).To(Succeed())
			}

			BeforeEach(func() {
				newWriter = &fakeDeferredWriter{
					FakeStateStoreWriter: &writersfakes.FakeStateStoreWriter{},
					deliverResult:        delivery.Result{Outcome: delivery.Delivered, VersionID: "v-new"},
				}
				useRepository(deferredWriter, "x")
			})

			When("a delivery is outstanding", func() {
				BeforeEach(func() {
					reconcileTimes(2)
					Expect(readStatus().DeliveryReceipt).To(Equal("receipt-1"))
					Expect(workPlacement.Status.DeliveryMode).To(Equal(ptr.To("x")))
				})

				It("drops the outstanding delivery and delivers again with the new mode", func() {
					useRepository(newWriter, "")

					reconcileTimes(1)

					Expect(modeChangedEvents()).To(ConsistOf(
						`Warning DeliveryModeChanged delivery mode changed from "x" to "built-in"; ` +
							`delivering the workloads again with the new mode. ` +
							`The earlier delivery, https://h/pr/7, is left as it is`))
					status := readStatus()
					Expect(status.DeliveryReceipt).To(BeEmpty())
					Expect(status.DeliveryGeneration).To(BeZero())
					Expect(status.DeliveryURL).To(BeEmpty())
					Expect(status.DeliveryMode).To(Equal(ptr.To("")))
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsAwaitingDelivery"))
					Expect(newWriter.checkReceipts).To(BeEmpty())
					Expect(newWriter.deliverCalls).To(Equal(0))

					By("delivering with the new writer on the next reconcile")
					reconcileTimes(1)

					Expect(newWriter.deliverCalls).To(Equal(1))
					Expect(newWriter.checkReceipts).To(BeEmpty())
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionTrue))
					Expect(workPlacement.Status.DeliveryMode).To(Equal(ptr.To("")))
				})

				It("checks the delivery as before when only the configuration changed", func() {
					useRepository(newWriter, "x")
					newWriter.checkResult = delivery.Result{Outcome: delivery.Pending, Receipt: "receipt-1"}

					reconcileTimes(1)

					Expect(modeChangedEvents()).To(BeEmpty())
					Expect(newWriter.checkReceipts).To(ConsistOf("receipt-1"))
					Expect(newWriter.deliverCalls).To(Equal(0))
				})

				It("records the current mode without delivering again when no mode was recorded", func() {
					forgetDeliveryMode()
					useRepository(newWriter, "y")
					newWriter.checkResult = delivery.Result{Outcome: delivery.Pending, Receipt: "receipt-1"}

					reconcileTimes(1)

					Expect(modeChangedEvents()).To(BeEmpty())
					Expect(newWriter.checkReceipts).To(ConsistOf("receipt-1"))
					Expect(newWriter.deliverCalls).To(Equal(0))
					Expect(readStatus().DeliveryMode).To(Equal(ptr.To("y")))
				})
			})

			When("a delivery was rejected", func() {
				BeforeEach(func() {
					reconcileTimes(2)
					deferredWriter.checkResult = delivery.Result{
						Outcome: delivery.Rejected, Reason: "closed without merging", URL: "https://h/pr/8",
					}
					reconcileTimes(1)
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsRejected"))
					Expect(workPlacement.Status.DeliveryMode).To(Equal(ptr.To("x")))
				})

				It("drops the rejection and writes the workloads with the built-in writer", func() {
					fakeWriter.UpdateFilesReturns("sha-direct", nil)
					useRepository(delivery.Direct(fakeWriter), delivery.BuiltIn)

					reconcileTimes(1)

					Expect(modeChangedEvents()).To(ConsistOf(
						`Warning DeliveryModeChanged delivery mode changed from "x" to "built-in"; ` +
							`delivering the workloads again with the new mode. ` +
							`The earlier delivery, https://h/pr/8, is left as it is`))
					written := readCondition(v1alpha1.WriteSucceededConditionType)
					Expect(written.Status).To(Equal(metav1.ConditionUnknown))
					Expect(written.Reason).To(Equal("WorkloadsAwaitingDelivery"))
					Expect(workPlacement.Status.DeliveryWorkloadsHash).To(BeEmpty())
					Expect(workPlacement.Status.DeliveryURL).To(BeEmpty())
					Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(0))

					By("writing the workloads on the next reconcile")
					reconcileTimes(1)

					Expect(fakeWriter.UpdateFilesCallCount()).To(Equal(1))
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionTrue))
					Expect(workPlacement.Status.VersionID).To(Equal("sha-direct"))
					Expect(workPlacement.Status.DeliveryMode).To(Equal(ptr.To("")))
				})

				It("drops the rejection and delivers again when the new mode is not the built-in one", func() {
					useRepository(newWriter, "y")

					reconcileTimes(1)

					Expect(modeChangedEvents()).To(ConsistOf(
						`Warning DeliveryModeChanged delivery mode changed from "x" to "y"; ` +
							`delivering the workloads again with the new mode. ` +
							`The earlier delivery, https://h/pr/8, is left as it is`))
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsAwaitingDelivery"))
					Expect(newWriter.deliverCalls).To(Equal(0))

					By("delivering with the new writer on the next reconcile")
					reconcileTimes(1)

					Expect(newWriter.deliverCalls).To(Equal(1))
					Expect(workPlacement.Status.DeliveryMode).To(Equal(ptr.To("y")))
				})

				It("keeps the rejection when only the configuration changed", func() {
					useRepository(newWriter, "x")

					reconcileTimes(1)

					Expect(modeChangedEvents()).To(BeEmpty())
					Expect(newWriter.deliverCalls).To(Equal(0))
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsRejected"))
				})

				It("records the current mode and keeps the rejection when no mode was recorded", func() {
					forgetDeliveryMode()
					useRepository(newWriter, "y")

					reconcileTimes(1)

					Expect(modeChangedEvents()).To(BeEmpty())
					Expect(newWriter.deliverCalls).To(Equal(0))
					Expect(deferredWriter.deliverCalls).To(Equal(1))
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsRejected"))
					Expect(workPlacement.Status.DeliveryMode).To(Equal(ptr.To("y")))
				})

				It("waits for the repository when it is not in the cache yet", func() {
					repositoryCache.GetRepositoryByTypeAndNameReturns(nil, controller.ErrCacheMiss)

					result := reconcileTimes(1)

					Expect(result.RequeueAfter).To(BeNumerically(">", 0))
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Reason).To(Equal("WorkloadsRejected"))
				})
			})

			When("the workloads were delivered with the built-in mode", func() {
				BeforeEach(func() {
					deferredWriter.deliverResult = delivery.Result{Outcome: delivery.Delivered, VersionID: "v1"}
					useRepository(deferredWriter, delivery.BuiltIn)
					reconcileTimes(2)
					Expect(readCondition(v1alpha1.WriteSucceededConditionType).Status).To(Equal(metav1.ConditionTrue))
					Expect(workPlacement.Status.DeliveryMode).To(Equal(ptr.To("")))
				})

				It("delivers them again with the new mode without a warning", func() {
					useRepository(newWriter, "x")

					reconcileTimes(1)

					Expect(modeChangedEvents()).To(BeEmpty())
					Expect(newWriter.deliverCalls).To(Equal(1))
					Expect(readStatus().DeliveryMode).To(Equal(ptr.To("x")))
				})
			})
		})
	})
})

var _ = Describe("WorkPlacements for a state store", func() {
	var reconciler *controller.WorkPlacementReconciler

	destinationFor := func(name string, ref v1alpha1.StateStoreReference) {
		Expect(fakeK8sClient.Create(ctx, &v1alpha1.Destination{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       v1alpha1.DestinationSpec{StateStoreRef: &ref},
		})).To(Succeed())
		wp := createWorkPlacement("wp-on-"+name, nil)
		wp.Labels[controller.TargetDestinationNameLabel] = name
		wp.Spec.TargetDestinationName = name
		Expect(fakeK8sClient.Create(ctx, &wp)).To(Succeed())
	}

	BeforeEach(func() {
		reconciler = &controller.WorkPlacementReconciler{Client: fakeK8sClient, APIReader: fakeK8sClient, Log: ctrl.Log}
		destinationFor("dest-a", v1alpha1.StateStoreReference{Kind: "GitStateStore", Name: "store-1"})
		destinationFor("dest-b", v1alpha1.StateStoreReference{Kind: "GitStateStore", Name: "store-1"})
		destinationFor("dest-bucket", v1alpha1.StateStoreReference{Kind: "BucketStateStore", Name: "store-1"})
		destinationFor("dest-other", v1alpha1.StateStoreReference{Kind: "GitStateStore", Name: "store-2"})
		destinationFor("dest-custom", v1alpha1.StateStoreReference{
			APIVersion: "custom.example.com/v1", Kind: "GitStateStore", Name: "store-1",
		})
	})

	It("enqueues the WorkPlacements on the Destinations that use the state store", func() {
		store := &v1alpha1.GitStateStore{ObjectMeta: metav1.ObjectMeta{Name: "store-1"}}

		requests := controller.WorkPlacementsForStateStore(reconciler, v1alpha1.GitStateStoreKind)(ctx, store)

		Expect(requests).To(ConsistOf(
			ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "wp-on-dest-a"}},
			ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "wp-on-dest-b"}},
		))
	})

	It("tells a bucket store apart from a git store with the same name", func() {
		store := &v1alpha1.BucketStateStore{ObjectMeta: metav1.ObjectMeta{Name: "store-1"}}

		requests := controller.WorkPlacementsForStateStore(reconciler, v1alpha1.BucketStateStoreKind)(ctx, store)

		Expect(requests).To(ConsistOf(
			ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "wp-on-dest-bucket"}},
		))
	})

	Describe("the predicate", func() {
		storeAt := func(observedGeneration int64, status string) *v1alpha1.GitStateStore {
			store := &v1alpha1.GitStateStore{ObjectMeta: metav1.ObjectMeta{Name: "store-1"}}
			store.Status.ObservedGeneration = observedGeneration
			store.Status.Status = status
			return store
		}

		It("passes an update that changes the observed generation", func() {
			Expect(controller.StateStoreObservedGenerationChangedPredicate().Update(event.UpdateEvent{
				ObjectOld: storeAt(1, "Ready"), ObjectNew: storeAt(2, "Ready"),
			})).To(BeTrue())
		})

		It("drops an update that leaves the observed generation alone", func() {
			Expect(controller.StateStoreObservedGenerationChangedPredicate().Update(event.UpdateEvent{
				ObjectOld: storeAt(2, "Ready"), ObjectNew: storeAt(2, "NotReady"),
			})).To(BeFalse())
		})

		It("drops a create, since the WorkPlacements wait for a new repository on their own", func() {
			Expect(controller.StateStoreObservedGenerationChangedPredicate().Create(event.CreateEvent{
				Object: storeAt(1, "Ready"),
			})).To(BeFalse())
		})
	})
})

type deliverCall struct {
	target delivery.Target
	create []v1alpha1.Workload
	remove []string
}

// recordingWriter passes every call to Writer and keeps the arguments of each Deliver.
type recordingWriter struct {
	delivery.Writer
	delivered []deliverCall
}

func (r *recordingWriter) Deliver(ctx context.Context, target delivery.Target, create []v1alpha1.Workload, remove []string) (delivery.Result, error) {
	r.delivered = append(r.delivered, deliverCall{target: target, create: create, remove: remove})
	return r.Writer.Deliver(ctx, target, create, remove)
}

// fakeDeferredWriter is a delivery writer that returns Pending and writes no canary.
type fakeDeferredWriter struct {
	*writersfakes.FakeStateStoreWriter

	deliverResult  delivery.Result
	deliverErr     error
	deliverCalls   int
	deliverTargets []delivery.Target
	delivered      []deliverCall
	// onDeliver runs inside the call, which is the window the Work reads while a delivery runs.
	onDeliver     func()
	checkResult   delivery.Result
	checkErr      error
	checkReceipts []string
	onCheck       func()
}

func (f *fakeDeferredWriter) Deliver(_ context.Context, target delivery.Target, create []v1alpha1.Workload, remove []string) (delivery.Result, error) {
	f.deliverCalls++
	f.deliverTargets = append(f.deliverTargets, target)
	f.delivered = append(f.delivered, deliverCall{target: target, create: create, remove: remove})
	if f.onDeliver != nil {
		f.onDeliver()
	}
	if f.deliverErr != nil {
		return delivery.Result{}, f.deliverErr
	}
	return f.deliverResult, nil
}

func (f *fakeDeferredWriter) Check(_ context.Context, receipt string) (delivery.Result, error) {
	f.checkReceipts = append(f.checkReceipts, receipt)
	if f.onCheck != nil {
		f.onCheck()
	}
	if f.checkErr != nil {
		return delivery.Result{}, f.checkErr
	}
	if f.checkResult.Outcome == "" {
		return f.deliverResult, nil
	}
	return f.checkResult, nil
}

func (f *fakeDeferredWriter) WriteCanary(context.Context, string, []v1alpha1.Workload) (bool, error) {
	return false, nil
}

func (f *fakeDeferredWriter) RemoveCanary(context.Context, string, []string) error {
	return nil
}

func setupGitDestination(gitStateStore *v1alpha1.GitStateStore, destination *v1alpha1.Destination) {
	Expect(fakeK8sClient.Create(ctx, &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "metav1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"username": []byte("test-username"),
			"password": []byte("test-password"),
		},
	})).To(Succeed())
	*gitStateStore = v1alpha1.GitStateStore{
		TypeMeta: metav1.TypeMeta{
			Kind:       "GitStateStore",
			APIVersion: "platform.kratix.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
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

	Expect(fakeK8sClient.Create(ctx, destination)).To(Succeed())
}

func createWorkPlacement(name string, workload []v1alpha1.Workload) v1alpha1.WorkPlacement {
	return v1alpha1.WorkPlacement{
		TypeMeta: metav1.TypeMeta{
			Kind:       "WorkPlacement",
			APIVersion: "platform.kratix.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				controller.TargetDestinationNameLabel: "test-destination",
			},
		},
		Spec: v1alpha1.WorkPlacementSpec{
			TargetDestinationName: "test-destination",
			ID:                    hash.ComputeHash("."),
			Workloads:             workload,
			PromiseName:           "test-promise",
			ResourceName:          "test-resource",
		},
	}
}

func setupTestMeterProvider() (*sdkmetric.ManualReader, func()) {
	reader := sdkmetric.NewManualReader()
	original := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	return reader, func() {
		otel.SetMeterProvider(original)
	}
}

func collectWorkPlacementWriteMetrics(ctx context.Context, reader *sdkmetric.ManualReader) map[string]int64 {
	var rm metricdata.ResourceMetrics
	Expect(reader.Collect(ctx, &rm)).To(Succeed())

	counts := make(map[string]int64)
	for _, scopeMetrics := range rm.ScopeMetrics {
		for _, metricData := range scopeMetrics.Metrics {
			if metricData.Name != telemetry.WorkPlacementWritesMetric {
				continue
			}

			sum, ok := metricData.Data.(metricdata.Sum[int64])
			Expect(ok).To(BeTrue(), "expected WorkPlacement write metric to be an int64 Sum")

			for _, dataPoint := range sum.DataPoints {
				resultAttr, ok := dataPoint.Attributes.Value(attribute.Key("result"))
				Expect(ok).To(BeTrue(), "expected WorkPlacement write metric to include result attribute")
				counts[resultAttr.AsString()] = dataPoint.Value
			}
		}
	}

	return counts
}
