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

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/lib/delivery"
	"github.com/syntasso/kratix/lib/writers"
	"github.com/syntasso/kratix/lib/writers/writersfakes"
)

var _ = Describe("GitStateStore Controller", func() {
	var (
		gitStateStore         *v1alpha1.GitStateStore
		updatedGitStateStore  *v1alpha1.GitStateStore
		reconciler            *controller.GitStateStoreReconciler
		fakeWriter            *writersfakes.FakeStateStoreWriter
		eventRecorder         *events.FakeRecorder
		secret                *corev1.Secret
		ctx                   context.Context
		testGitStateStoreName types.NamespacedName
		secretName            string
	)

	BeforeEach(func() {
		ctx = context.Background()

		testGitStateStoreName = types.NamespacedName{
			Name: "default-store",
		}

		secretName = "store-secret"

		eventRecorder = events.NewFakeRecorder(1024)

		fakeWriter = &writersfakes.FakeStateStoreWriter{}
		controller.SetNewGitWriter(
			func(l logr.Logger, s v1alpha1.GitStateStoreSpec, d string, c map[string][]byte) (writers.StateStoreWriter, error) {
				return fakeWriter, nil
			},
		)
		fakeWriter.UpdateFilesReturns("", nil)
		fakeWriter.ValidatePermissionsReturns(nil)

		reconciler = &controller.GitStateStoreReconciler{
			Client:          fakeK8sClient,
			APIReader:       fakeK8sClient,
			Scheme:          scheme.Scheme,
			Log:             ctrl.Log.WithName("controllers").WithName("GitStateStore"),
			EventRecorder:   eventRecorder,
			RepositoryCache: controller.NewRepositoryCache(),
		}

		gitStateStore = &v1alpha1.GitStateStore{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default-store",
			},
			TypeMeta: metav1.TypeMeta{
				Kind:       "GitStateStore",
				APIVersion: "platform.kratix.io/v1alpha1",
			},
			Spec: v1alpha1.GitStateStoreSpec{
				URL: "localhost:3000",
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

		updatedGitStateStore = &v1alpha1.GitStateStore{}
	})

	When("the GitStateStore does not exist", func() {
		It("reconciles without error and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, gitStateStore)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})

	When("the GitStateStore exists", func() {
		var result ctrl.Result
		var err error

		BeforeEach(func() {
			Expect(fakeK8sClient.Create(ctx, gitStateStore)).To(Succeed())
			Expect(fakeK8sClient.Create(ctx, secret)).To(Succeed())
		})

		It("reconciles without error and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, gitStateStore)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			By("validating permissions on the state store", func() {
				Expect(fakeWriter.ValidatePermissionsCallCount()).To(Equal(2))
			})

			By("updating the status to say the state store is ready", func() {
				Expect(fakeK8sClient.Get(ctx, testGitStateStoreName, updatedGitStateStore)).To(Succeed())
				Expect(updatedGitStateStore.Status.Status).To(Equal(controller.StatusReady))
				Expect(updatedGitStateStore.Status.Conditions).To(ContainElement(SatisfyAll(
					HaveField("Type", "Ready"),
					HaveField("Message", "State store is ready"),
					HaveField("Reason", "StateStoreReady"),
					HaveField("Status", metav1.ConditionTrue),
				)))
			})

			By("firing an event to indicate the state store is ready", func() {
				Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
					"Normal Ready GitStateStore \"default-store\" is ready")))
			})
		})

		When("the writer fails to initialise", func() {
			BeforeEach(func() {
				gitStateStore.Status.Status = controller.StatusReady
				Expect(fakeK8sClient.Status().Update(ctx, gitStateStore)).To(Succeed())

				controller.SetNewGitWriter(
					func(l logr.Logger, s v1alpha1.GitStateStoreSpec, d string, c map[string][]byte) (writers.StateStoreWriter, error) {
						return fakeWriter, errors.New("writer-create-error")
					},
				)

				result, err = t.reconcileUntilCompletion(reconciler, gitStateStore, &opts{requeueExpected: true})
			})

			It("updates the status ", func() {
				Expect(result).To(Equal(ctrl.Result{RequeueAfter: time.Second * 15}))
				Expect(err).ToNot(HaveOccurred())

				Expect(fakeK8sClient.Get(ctx, testGitStateStoreName, updatedGitStateStore)).To(Succeed())
				Expect(updatedGitStateStore.Status.Status).To(Equal(controller.StatusNotReady))
				Expect(updatedGitStateStore.Status.Conditions).To(ContainElement(SatisfyAll(
					HaveField("Type", "Ready"),
					HaveField("Message", "unable to create git writer: writer-create-error"),
					HaveField("Reason", "ErrorInitialisingWriter"),
					HaveField("Status", metav1.ConditionFalse),
				)))
			})

			It("fires an event to indicate writer initialization failed", func() {
				Expect(eventRecorder.Events).To(Receive(ContainSubstring(
					"Warning NotReady unable to create git writer: writer-create-error")))
			})
		})

		When("the writer fails to validate permissions", func() {
			BeforeEach(func() {
				fakeWriter.ValidatePermissionsReturns(errors.New("ARGH!"))

				gitStateStore.Status.Status = controller.StatusReady
				Expect(fakeK8sClient.Status().Update(ctx, gitStateStore)).To(Succeed())

				result, err = t.reconcileUntilCompletion(reconciler, gitStateStore)
			})

			It("updates the status to say permissions validation failed", func() {
				Expect(result).To(Equal(ctrl.Result{RequeueAfter: time.Second * 15}))
				Expect(err).To(MatchError(ContainSubstring("reconcile loop detected")))

				Expect(fakeK8sClient.Get(ctx, testGitStateStoreName, updatedGitStateStore)).To(Succeed())
				Expect(updatedGitStateStore.Status.Status).To(Equal(controller.StatusNotReady))
				Expect(updatedGitStateStore.Status.Conditions).To(ContainElement(SatisfyAll(
					HaveField("Type", "Ready"),
					HaveField("Message", "Error validating state store permissions: ARGH!"),
					HaveField("Reason", "ErrorValidatingPermissions"),
					HaveField("Status", metav1.ConditionFalse),
				)))
			})

			It("fires an event to indicate permissions validation failed", func() {
				Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
					"Warning NotReady Error validating state store permissions: ARGH!")))
			})
		})

		When("the delivery mode the writer was last built with is not the one in spec.delivery", func() {
			var (
				oldMode string
				retirer *fakeRetiringWriter
			)

			BeforeEach(func() {
				oldMode = uniqueDeliveryMode("retire-old")
				retirer = &fakeRetiringWriter{
					fakeDeferredWriter: &fakeDeferredWriter{FakeStateStoreWriter: fakeWriter},
					initCalls:          fakeWriter.InitCallCount,
				}
				delivery.Register(oldMode, func(_ writers.StateStoreWriter, config map[string]string, _ map[string][]byte) (delivery.Writer, error) {
					retirer.builtWithConfig = config
					return retirer, nil
				})

				gitStateStore.Status.Delivery = &v1alpha1.Delivery{Mode: oldMode, Config: map[string]string{"repo": "old"}}
				Expect(fakeK8sClient.Status().Update(ctx, gitStateStore)).To(Succeed())

				createDestinationOn("this-store-dest", v1alpha1.GitStateStoreKind, "default-store")
				createDestinationOn("other-store-dest", v1alpha1.GitStateStoreKind, "other-store")
				createDestinationOn("bucket-same-name-dest", v1alpha1.BucketStateStoreKind, "default-store")
				createWorkPlacementOn("wp-a", "this-store-dest", "receipt-a")
				createWorkPlacementOn("wp-b", "this-store-dest", "receipt-b")
				createWorkPlacementOn("wp-other", "other-store-dest", "receipt-other")
				createWorkPlacementOn("wp-bucket", "bucket-same-name-dest", "receipt-bucket")
				createWorkPlacementOn("wp-no-receipt", "this-store-dest", "")
			})

			It("retires the old mode's outstanding deliveries on this store before building the new writer", func() {
				result, err := t.reconcileUntilCompletion(reconciler, gitStateStore)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(retirer.retireCalls).To(HaveLen(1))
				Expect(retirer.retireCalls[0]).To(ConsistOf(
					delivery.Outstanding{Receipt: "receipt-a", WorkPlacement: "wp-a"},
					delivery.Outstanding{Receipt: "receipt-b", WorkPlacement: "wp-b"},
				))
				Expect(retirer.initCallsAtRetire).To(Equal([]int{0}))
				Expect(retirer.builtWithConfig).To(Equal(map[string]string{"repo": "old"}))

				Expect(fakeK8sClient.Get(ctx, testGitStateStoreName, updatedGitStateStore)).To(Succeed())
				Expect(updatedGitStateStore.Status.Delivery).To(BeNil())
				Expect(updatedGitStateStore.Status.Status).To(Equal(controller.StatusReady))
			})

			When("retiring fails", func() {
				BeforeEach(func() {
					retirer.retireErr = errors.New("host unreachable")
				})

				It("reports NotReady, keeps the old delivery block, does not build the new writer and retries", func() {
					result, err := t.reconcileUntilCompletion(reconciler, gitStateStore, &opts{singleReconcile: true})
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{RequeueAfter: 15 * time.Second}))

					Expect(fakeK8sClient.Get(ctx, testGitStateStoreName, updatedGitStateStore)).To(Succeed())
					Expect(updatedGitStateStore.Status.Status).To(Equal(controller.StatusNotReady))
					Expect(updatedGitStateStore.Status.Conditions).To(ContainElement(SatisfyAll(
						HaveField("Type", "Ready"),
						HaveField("Status", metav1.ConditionFalse),
						HaveField("Reason", "RetireFailed"),
						HaveField("Message", fmt.Sprintf("could not retire delivery mode %q: host unreachable", oldMode)),
					)))
					Expect(updatedGitStateStore.Status.Delivery).To(Equal(&v1alpha1.Delivery{Mode: oldMode, Config: map[string]string{"repo": "old"}}))
					Expect(fakeWriter.InitCallCount()).To(Equal(0))

					_, err = t.reconcileUntilCompletion(reconciler, gitStateStore, &opts{singleReconcile: true})
					Expect(err).NotTo(HaveOccurred())
					Expect(retirer.retireCalls).To(HaveLen(2))
					Expect(fakeWriter.InitCallCount()).To(Equal(0))
				})
			})
		})

		When("the store is already Ready but has never recorded its delivery block", func() {
			var newMode string

			BeforeEach(func() {
				newMode = uniqueDeliveryMode("record")
				delivery.Register(newMode, func(base writers.StateStoreWriter, _ map[string]string, _ map[string][]byte) (delivery.Writer, error) {
					return &fakeDeferredWriter{FakeStateStoreWriter: fakeWriter}, nil
				})

				gitStateStore.Spec.Delivery = &v1alpha1.Delivery{Mode: newMode, Config: map[string]string{"repo": "new"}}
				Expect(fakeK8sClient.Update(ctx, gitStateStore)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, testGitStateStoreName, gitStateStore)).To(Succeed())
				gitStateStore.Status = v1alpha1.StateStoreStatus{
					Status:             controller.StatusReady,
					ObservedGeneration: gitStateStore.GetGeneration(),
					Conditions: []metav1.Condition{{
						Type:               "Ready",
						Status:             metav1.ConditionTrue,
						Reason:             "StateStoreReady",
						Message:            "State store is ready",
						LastTransitionTime: metav1.Now(),
					}},
				}
				Expect(fakeK8sClient.Status().Update(ctx, gitStateStore)).To(Succeed())
			})

			It("records spec.delivery in status", func() {
				_, err := t.reconcileUntilCompletion(reconciler, gitStateStore)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeK8sClient.Get(ctx, testGitStateStoreName, updatedGitStateStore)).To(Succeed())
				Expect(updatedGitStateStore.Status.Delivery).To(Equal(&v1alpha1.Delivery{Mode: newMode, Config: map[string]string{"repo": "new"}}))
			})
		})

		Describe("delivery changes that retire nothing", func() {
			var retirer *fakeRetiringWriter

			BeforeEach(func() {
				retirer = &fakeRetiringWriter{fakeDeferredWriter: &fakeDeferredWriter{FakeStateStoreWriter: fakeWriter}}
				createDestinationOn("this-store-dest", v1alpha1.GitStateStoreKind, "default-store")
				createWorkPlacementOn("wp-a", "this-store-dest", "receipt-a")
			})

			saveAndSpec := func(saved, spec *v1alpha1.Delivery) {
				gitStateStore.Spec.Delivery = spec
				Expect(fakeK8sClient.Update(ctx, gitStateStore)).To(Succeed())
				gitStateStore.Status.Delivery = saved
				Expect(fakeK8sClient.Status().Update(ctx, gitStateStore)).To(Succeed())
			}

			reconcileAndGetStore := func() {
				_, err := t.reconcileUntilCompletion(reconciler, gitStateStore)
				Expect(err).NotTo(HaveOccurred())
				Expect(fakeK8sClient.Get(ctx, testGitStateStoreName, updatedGitStateStore)).To(Succeed())
			}

			It("does not retire when only the config of the same mode changes", func() {
				mode := uniqueDeliveryMode("same")
				delivery.Register(mode, func(writers.StateStoreWriter, map[string]string, map[string][]byte) (delivery.Writer, error) {
					return retirer, nil
				})
				saveAndSpec(
					&v1alpha1.Delivery{Mode: mode, Config: map[string]string{"repo": "old"}},
					&v1alpha1.Delivery{Mode: mode, Config: map[string]string{"repo": "new"}},
				)

				reconcileAndGetStore()

				Expect(retirer.retireCalls).To(BeEmpty())
				Expect(updatedGitStateStore.Status.Delivery).To(Equal(&v1alpha1.Delivery{Mode: mode, Config: map[string]string{"repo": "new"}}))
			})

			It("does not retire when no delivery block was recorded", func() {
				mode := uniqueDeliveryMode("saved-nil")
				delivery.Register(mode, func(writers.StateStoreWriter, map[string]string, map[string][]byte) (delivery.Writer, error) {
					return retirer, nil
				})
				saveAndSpec(nil, &v1alpha1.Delivery{Mode: mode})

				reconcileAndGetStore()

				Expect(retirer.retireCalls).To(BeEmpty())
				Expect(updatedGitStateStore.Status.Delivery).To(Equal(&v1alpha1.Delivery{Mode: mode}))
			})

			It("records the new delivery block when the old writer cannot retire", func() {
				mode := uniqueDeliveryMode("no-retirer")
				delivery.Register(mode, func(writers.StateStoreWriter, map[string]string, map[string][]byte) (delivery.Writer, error) {
					return &fakeDeferredWriter{FakeStateStoreWriter: fakeWriter}, nil
				})
				saveAndSpec(&v1alpha1.Delivery{Mode: mode}, nil)

				reconcileAndGetStore()

				Expect(updatedGitStateStore.Status.Delivery).To(BeNil())
				Expect(updatedGitStateStore.Status.Status).To(Equal(controller.StatusReady))
			})

			It("warns and records the new delivery block when the old mode is not registered", func() {
				mode := uniqueDeliveryMode("never-registered")
				saveAndSpec(&v1alpha1.Delivery{Mode: mode}, nil)

				reconcileAndGetStore()

				Expect(updatedGitStateStore.Status.Delivery).To(BeNil())
				Expect(updatedGitStateStore.Status.Status).To(Equal(controller.StatusReady))
				Eventually(eventRecorder.Events).Should(Receive(SatisfyAll(
					ContainSubstring("Warning"),
					ContainSubstring(fmt.Sprintf("%q", mode)),
				)))
			})
		})

		It("returns from a reconcile when the writer fails to validate permissions", func() {
			fakeWriter.ValidatePermissionsReturns(errors.New("ARGH!"))

			done := make(chan ctrl.Result, 1)
			go func() {
				result, _ := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: testGitStateStoreName})
				done <- result
			}()

			Eventually(done, 5*time.Second).Should(Receive(Equal(ctrl.Result{RequeueAfter: 15 * time.Second})))
		})
	})
})

var deliveryModeCount int

// uniqueDeliveryMode names a mode no other spec registered: registration is process-wide and refuses duplicates.
func uniqueDeliveryMode(prefix string) string {
	deliveryModeCount++
	return fmt.Sprintf("%s-%d", prefix, deliveryModeCount)
}

type fakeRetiringWriter struct {
	*fakeDeferredWriter

	retireErr         error
	retireCalls       [][]delivery.Outstanding
	initCalls         func() int
	initCallsAtRetire []int
	builtWithConfig   map[string]string
}

func (f *fakeRetiringWriter) Retire(_ context.Context, outstanding []delivery.Outstanding) error {
	f.retireCalls = append(f.retireCalls, outstanding)
	if f.initCalls != nil {
		f.initCallsAtRetire = append(f.initCallsAtRetire, f.initCalls())
	}
	return f.retireErr
}

func createDestinationOn(name, storeKind, storeName string) {
	Expect(fakeK8sClient.Create(context.Background(), &v1alpha1.Destination{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.DestinationSpec{
			StateStoreRef: &v1alpha1.StateStoreReference{Kind: storeKind, Name: storeName},
		},
	})).To(Succeed())
}

func createWorkPlacementOn(name, destination, receipt string) {
	wp := &v1alpha1.WorkPlacement{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{controller.TargetDestinationNameLabel: destination},
		},
		Spec: v1alpha1.WorkPlacementSpec{TargetDestinationName: destination},
	}
	Expect(fakeK8sClient.Create(context.Background(), wp)).To(Succeed())
	wp.Status.DeliveryReceipt = receipt
	Expect(fakeK8sClient.Status().Update(context.Background(), wp)).To(Succeed())
}
