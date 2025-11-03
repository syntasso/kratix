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

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = FDescribe("PromiseRevisionController", func() {
	var reconciler *controller.PromiseRevisionReconciler
	var l logr.Logger
	var eventRecorder *record.FakeRecorder

	BeforeEach(func() {
		ctx = context.Background()
		l = ctrl.Log.WithName("controllers").WithName("PromiseRevision")
		eventRecorder = record.NewFakeRecorder(1024)
		reconciler = &controller.PromiseRevisionReconciler{
			Client:        fakeK8sClient,
			Log:           l,
			EventRecorder: eventRecorder,
		}
	})

	Describe("Reconcile", func() {
		var revision *v1alpha1.PromiseRevision
		var previousRevision *v1alpha1.PromiseRevision

		BeforeEach(func() {
			revision = &v1alpha1.PromiseRevision{
				TypeMeta: v1.TypeMeta{
					Kind:       "PromiseRevision",
					APIVersion: "platform.kratix.io/v1alpha1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-revision",
					Labels: map[string]string{
						"kratix.io/latest-revision": "true",
					},
				},
				Spec: v1alpha1.PromiseRevisionSpec{
					Version: "v1.0.0",
					PromiseRef: v1alpha1.PromiseRef{
						Name: "test-promise",
					},
				},
			}
			Expect(fakeK8sClient.Create(ctx, revision)).To(Succeed())

			previousRevision = &v1alpha1.PromiseRevision{
				TypeMeta: v1.TypeMeta{
					Kind:       "PromiseRevision",
					APIVersion: "platform.kratix.io/v1alpha1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "previous-latest-revision",
					Labels: map[string]string{
						"kratix.io/latest-revision":       "true",
						platformv1alpha1.PromiseNameLabel: "test-promise",
					},
				},
				Spec: v1alpha1.PromiseRevisionSpec{
					Version: "v0.9.0",
					PromiseRef: v1alpha1.PromiseRef{
						Name: "test-promise",
					},
				},
			}
			Expect(fakeK8sClient.Create(ctx, previousRevision)).To(Succeed())
			Expect(fakeK8sClient.Status().Update(ctx, previousRevision)).To(Succeed())
		})

		When("the revision has the latest label", func() {
			It("updates the revision status to latest", func() {
				result, err := t.reconcileUntilCompletion(reconciler, revision)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: revision.Name}, revision)).To(Succeed())
				Expect(revision.GetLabels()["kratix.io/latest-revision"]).To(Equal("true"))
				Expect(revision.Status.Latest).To(BeTrue())
			})

			It("removes the latest label from the previous latest revision", func() {
				result, err := t.reconcileUntilCompletion(reconciler, revision)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: previousRevision.Name}, previousRevision)).To(Succeed())
				Expect(previousRevision.GetLabels()).ToNot(HaveKey("kratix.io/latest-revision"))
			})
		})

		When("the revision does not have the latest label", func() {
			It("ensures the revision status is not latest", func() {
				nonLatestRevision := &v1alpha1.PromiseRevision{
					TypeMeta: v1.TypeMeta{
						Kind:       "PromiseRevision",
						APIVersion: "platform.kratix.io/v1alpha1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name: "non-latest-revision",
					},
					Spec: v1alpha1.PromiseRevisionSpec{
						Version: "v0.8.0",
						PromiseRef: v1alpha1.PromiseRef{
							Name: "test-promise",
						},
					},
					Status: v1alpha1.PromiseRevisionStatus{
						Latest: true,
					},
				}
				Expect(fakeK8sClient.Create(ctx, nonLatestRevision)).To(Succeed())
				Expect(fakeK8sClient.Status().Update(ctx, nonLatestRevision)).To(Succeed())

				result, err := t.reconcileUntilCompletion(reconciler, nonLatestRevision)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: nonLatestRevision.Name}, nonLatestRevision)).To(Succeed())
				Expect(nonLatestRevision.GetLabels()).ToNot(HaveKey("kratix.io/latest-revision"))
				Expect(nonLatestRevision.Status.Latest).To(BeFalse())
			})
		})
	})
})
