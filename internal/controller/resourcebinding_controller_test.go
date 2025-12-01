/*
Copyright 2025 Syntasso.

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
	"os"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/lib/resourceutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("ResourceBinding Controller", func() {
	Describe("Reconciling a ResourceBinding", func() {
		var (
			reconciler *controller.ResourceBindingReconciler
			// eventRecorder            *record.FakeRecorder
			ctx                      context.Context
			resourceBinding          v1alpha1.ResourceBinding
			resourceBindingNamespace string
			resourceBindingName      string
			promisePath              string
			rr                       *unstructured.Unstructured
		)

		BeforeEach(func() {
			ctx = context.Background()
			reconciler = &controller.ResourceBindingReconciler{
				Client: fakeK8sClient,
				Scheme: scheme.Scheme,
				Log:    ctrl.Log.WithName("controllers").WithName("ResourceBindings"),
			}

			resourceBindingNamespace = "default"
			resourceBindingName = "example-redis-1s324"

			resourceBinding = v1alpha1.ResourceBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "example-redis-1s324",
					Namespace: resourceBindingNamespace,
				},
				Spec: v1alpha1.ResourceBindingSpec{
					Version: "v0.0.2",
					PromiseRef: v1alpha1.PromiseRef{
						Name: "redis",
					},
					ResourceRef: v1alpha1.ResourceRef{
						Name:      "example",
						Namespace: "default",
					},
				},
			}

			promisePath = "assets/redis-simple-promise.yaml"
			promise = createPromise(promisePath)

			err := fakeK8sClient.Create(ctx, &resourceBinding)
			Expect(err).ToNot(HaveOccurred())
		})

		When("the resource request does not exist", func() {
			It("returns an error", func() {
				request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

				_, err := reconciler.Reconcile(ctx, request)
				Expect(err).To(MatchError("failed to get resource request example"))
			})
		})

		When("the resource request has a version that does not match the spec.Version", func() {
			BeforeEach(func() {
				yamlFile, err := os.ReadFile(resourceRequestPath)
				Expect(err).ToNot(HaveOccurred())

				rr = &unstructured.Unstructured{}
				Expect(yaml.Unmarshal(yamlFile, rr)).To(Succeed())
				Expect(fakeK8sClient.Create(ctx, rr)).To(Succeed())
				resNameNamespacedName := types.NamespacedName{
					Name:      rr.GetName(),
					Namespace: rr.GetNamespace(),
				}

				resourceutil.SetStatus(rr, logr.Discard(), "promiseVersion", "v0.0.1")

				Expect(fakeK8sClient.Status().Update(ctx, rr)).To(Succeed())

				Expect(fakeK8sClient.Get(ctx, resNameNamespacedName, rr)).To(Succeed())
				Expect(resourceutil.GetStatus(rr, "promiseVersion")).To(Equal("v0.0.1"))
			})

			It("applies the manual reconciliation label to the resource request", func() {
				request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

				result, err := reconciler.Reconcile(ctx, request)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				fakeK8sClient.Get(ctx,
					types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()},
					rr,
				)
				Expect(rr.GetLabels()[resourceutil.ManualReconciliationLabel]).To(Equal("true"))
			})

			It("does not apply the manual reconciliation label when the versions match", func() {
				fakeK8sClient.Get(ctx,
					types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()},
					rr,
				)

				resourceutil.SetStatus(rr, logr.Discard(), "promiseVersion", "v0.0.2")
				Expect(fakeK8sClient.Status().Update(ctx, rr)).To(Succeed())

				request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

				result, err := reconciler.Reconcile(ctx, request)
				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				fakeK8sClient.Get(ctx,
					types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()},
					rr,
				)
				Expect(rr.GetLabels()[resourceutil.ManualReconciliationLabel]).To(Equal(""))
			})
		})
	})
})
