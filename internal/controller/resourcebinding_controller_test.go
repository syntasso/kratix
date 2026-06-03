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
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("ResourceBinding Controller", func() {
	Describe("Reconciling a ResourceBinding", func() {
		var (
			reconciler               *controller.ResourceBindingReconciler
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
				rr = createResourceRequestWithVersion(ctx, "v0.0.1")
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

			It("sets the UpgradeSucceeded condition to Unknown on the resource binding", func() {
				request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

				_, err := reconciler.Reconcile(ctx, request)
				Expect(err).ToNot(HaveOccurred())

				var rb v1alpha1.ResourceBinding
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &rb)).To(Succeed())
				cond := apiMeta.FindStatusCondition(rb.Status.Conditions, v1alpha1.UpgradeSucceededCondition)
				Expect(cond).NotTo(BeNil())
				Expect(string(cond.Status)).To(Equal(string(metav1.ConditionUnknown)))
				Expect(cond.Reason).To(Equal(v1alpha1.UpgradeInProgressReason))
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
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				fakeK8sClient.Get(ctx,
					types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()},
					rr,
				)
				Expect(rr.GetLabels()[resourceutil.ManualReconciliationLabel]).To(Equal(""))
			})

			It("does not apply the manual reconciliation label when the promise has no version", func() {
				fakeK8sClient.Get(ctx,
					types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()},
					rr,
				)

				resourceutil.SetStatus(rr, logr.Discard(), "promiseVersion", controller.UnversionedPromiseVersion)
				Expect(fakeK8sClient.Status().Update(ctx, rr)).To(Succeed())

				request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

				result, err := reconciler.Reconcile(ctx, request)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				fakeK8sClient.Get(ctx,
					types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()},
					rr,
				)
				Expect(rr.GetLabels()[resourceutil.ManualReconciliationLabel]).To(Equal(""))
			})

			It("does not apply the manual reconciliation label when the binding tracks latest and the RR is already on the latest revision", func() {
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &resourceBinding)).To(Succeed())
				resourceBinding.Spec.Version = "latest"
				Expect(fakeK8sClient.Update(ctx, &resourceBinding)).To(Succeed())

				createPromiseRevision(fakeK8sClient, promise, "v0.0.1")

				request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

				result, err := reconciler.Reconcile(ctx, request)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				fakeK8sClient.Get(ctx,
					types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()},
					rr,
				)
				Expect(rr.GetLabels()[resourceutil.ManualReconciliationLabel]).To(Equal(""))
			})

			It("applies the manual reconciliation label when the binding tracks latest but the RR is behind the latest revision", func() {
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &resourceBinding)).To(Succeed())
				resourceBinding.Spec.Version = "latest"
				Expect(fakeK8sClient.Update(ctx, &resourceBinding)).To(Succeed())

				createPromiseRevision(fakeK8sClient, promise, "v0.0.2")

				request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

				result, err := reconciler.Reconcile(ctx, request)
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				fakeK8sClient.Get(ctx,
					types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()},
					rr,
				)
				Expect(rr.GetLabels()[resourceutil.ManualReconciliationLabel]).To(Equal("true"))
			})

			It("returns an error when the binding tracks latest but no latest PromiseRevision exists", func() {
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &resourceBinding)).To(Succeed())
				resourceBinding.Spec.Version = "latest"
				Expect(fakeK8sClient.Update(ctx, &resourceBinding)).To(Succeed())

				request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

				_, err := reconciler.Reconcile(ctx, request)
				Expect(err).To(MatchError(ContainSubstring("failed to get latest provision revision for promise redis")))
			})

			When("UpgradeSucceeded is False", func() {
				BeforeEach(func() {
					// Pre-set the binding with UpgradeSucceeded=False (pipeline previously failed).
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &resourceBinding)).To(Succeed())
					apiMeta.SetStatusCondition(&resourceBinding.Status.Conditions, metav1.Condition{
						Type:   v1alpha1.UpgradeSucceededCondition,
						Status: metav1.ConditionFalse,
						Reason: v1alpha1.UpgradeFailedReason,
					})
					Expect(fakeK8sClient.Status().Update(ctx, &resourceBinding)).To(Succeed())
				})

				When("FailedVersion matches the desired version (same version that previously failed)", func() {
					BeforeEach(func() {
						// FailedVersion == spec.Version ("v0.0.2"), so this attempt already failed.
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &resourceBinding)).To(Succeed())
						resourceBinding.Status.FailedVersion = resourceBinding.Spec.Version
						Expect(fakeK8sClient.Status().Update(ctx, &resourceBinding)).To(Succeed())
					})

					It("returns early without re-triggering the upgrade", func() {
						request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

						result, err := reconciler.Reconcile(ctx, request)
						Expect(err).ToNot(HaveOccurred())
						Expect(result).To(Equal(ctrl.Result{}))

						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()}, rr)).To(Succeed())
						Expect(rr.GetLabels()[resourceutil.ManualReconciliationLabel]).NotTo(Equal("true"))

						var rb v1alpha1.ResourceBinding
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &rb)).To(Succeed())
						cond := apiMeta.FindStatusCondition(rb.Status.Conditions, v1alpha1.UpgradeSucceededCondition)
						Expect(string(cond.Status)).To(Equal(string(metav1.ConditionFalse)))
						Expect(rb.Status.FailedVersion).To(Equal(resourceBinding.Spec.Version))
					})
				})

				When("FailedVersion does not match the desired version (spec.version was changed to a new target)", func() {
					BeforeEach(func() {
						// FailedVersion points to an older version, not the current spec.Version.
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &resourceBinding)).To(Succeed())
						resourceBinding.Status.FailedVersion = "v0.0.1"
						Expect(fakeK8sClient.Status().Update(ctx, &resourceBinding)).To(Succeed())
					})

					It("triggers a new upgrade attempt, clears FailedVersion, and sets UpgradeSucceeded=Unknown", func() {
						request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

						result, err := reconciler.Reconcile(ctx, request)
						Expect(err).ToNot(HaveOccurred())
						Expect(result).To(Equal(ctrl.Result{}))

						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()}, rr)).To(Succeed())
						Expect(rr.GetLabels()[resourceutil.ManualReconciliationLabel]).To(Equal("true"))

						var rb v1alpha1.ResourceBinding
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &rb)).To(Succeed())
						cond := apiMeta.FindStatusCondition(rb.Status.Conditions, v1alpha1.UpgradeSucceededCondition)
						Expect(string(cond.Status)).To(Equal(string(metav1.ConditionUnknown)))
						Expect(cond.Reason).To(Equal(v1alpha1.UpgradeInProgressReason))
						Expect(rb.Status.FailedVersion).To(BeEmpty())
					})
				})

				When("FailedVersion is unset (legacy binding without FailedVersion)", func() {
					It("triggers a new upgrade attempt and sets UpgradeSucceeded=Unknown", func() {
						request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

						result, err := reconciler.Reconcile(ctx, request)
						Expect(err).ToNot(HaveOccurred())
						Expect(result).To(Equal(ctrl.Result{}))

						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()}, rr)).To(Succeed())
						Expect(rr.GetLabels()[resourceutil.ManualReconciliationLabel]).To(Equal("true"))

						var rb v1alpha1.ResourceBinding
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &rb)).To(Succeed())
						cond := apiMeta.FindStatusCondition(rb.Status.Conditions, v1alpha1.UpgradeSucceededCondition)
						Expect(string(cond.Status)).To(Equal(string(metav1.ConditionUnknown)))
						Expect(rb.Status.FailedVersion).To(BeEmpty())
					})
				})
			})

			When("UpgradeSucceeded is Unknown and FailedVersion is stale", func() {
				It("clears FailedVersion as part of the in-progress write", func() {
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &resourceBinding)).To(Succeed())
					apiMeta.SetStatusCondition(&resourceBinding.Status.Conditions, metav1.Condition{
						Type:   v1alpha1.UpgradeSucceededCondition,
						Status: metav1.ConditionUnknown,
						Reason: v1alpha1.UpgradeInProgressReason,
					})
					resourceBinding.Status.FailedVersion = "stale-version"
					Expect(fakeK8sClient.Status().Update(ctx, &resourceBinding)).To(Succeed())

					request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

					_, err := reconciler.Reconcile(ctx, request)
					Expect(err).ToNot(HaveOccurred())

					var rb v1alpha1.ResourceBinding
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &rb)).To(Succeed())
					Expect(rb.Status.FailedVersion).To(BeEmpty())
				})
			})

			When("an upgrade is already in flight targeting the desired version", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &resourceBinding)).To(Succeed())
					apiMeta.SetStatusCondition(&resourceBinding.Status.Conditions, metav1.Condition{
						Type:    v1alpha1.UpgradeSucceededCondition,
						Status:  metav1.ConditionUnknown,
						Reason:  v1alpha1.UpgradeInProgressReason,
						Message: "Upgrade to version v0.0.2 is in progress",
					})
					Expect(fakeK8sClient.Status().Update(ctx, &resourceBinding)).To(Succeed())
				})

				It("does not re-apply the manual reconciliation label to the resource request", func() {
					request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

					result, err := reconciler.Reconcile(ctx, request)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()}, rr)).To(Succeed())
					Expect(rr.GetLabels()).NotTo(HaveKey(resourceutil.ManualReconciliationLabel))
				})

				When("the manual reconciliation label is set on the binding", func() {
					BeforeEach(func() {
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &resourceBinding)).To(Succeed())
						labels := resourceBinding.GetLabels()
						if labels == nil {
							labels = make(map[string]string)
						}
						labels[resourceutil.ManualReconciliationLabel] = "true"
						resourceBinding.SetLabels(labels)
						Expect(fakeK8sClient.Update(ctx, &resourceBinding)).To(Succeed())
					})

					It("doesn't apply the manual reconciliation label to RR, but it does clear it from the binding", func() {
						request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

						result, err := reconciler.Reconcile(ctx, request)
						Expect(err).NotTo(HaveOccurred())
						Expect(result).To(Equal(ctrl.Result{}))

						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()}, rr)).To(Succeed())
						Expect(rr.GetLabels()).NotTo(HaveKey(resourceutil.ManualReconciliationLabel))

						var rb v1alpha1.ResourceBinding
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &rb)).To(Succeed())
						Expect(rb.GetLabels()).NotTo(HaveKey(resourceutil.ManualReconciliationLabel))
					})
				})
			})

			When("an upgrade is in flight targeting a different version than desired", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}, &resourceBinding)).To(Succeed())
					apiMeta.SetStatusCondition(&resourceBinding.Status.Conditions, metav1.Condition{
						Type:    v1alpha1.UpgradeSucceededCondition,
						Status:  metav1.ConditionUnknown,
						Reason:  v1alpha1.UpgradeInProgressReason,
						Message: "Upgrade to version v0.0.1 is in progress",
					})
					Expect(fakeK8sClient.Status().Update(ctx, &resourceBinding)).To(Succeed())
				})

				It("applies the manual reconciliation label to the resource request", func() {
					request := ctrl.Request{NamespacedName: types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}}

					result, err := reconciler.Reconcile(ctx, request)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: rr.GetName(), Namespace: rr.GetNamespace()}, rr)).To(Succeed())
					Expect(rr.GetLabels()).To(HaveKeyWithValue(resourceutil.ManualReconciliationLabel, "true"))
				})
			})
		})

		When("the manual reconciliation label is applied to the binding", func() {
			var bindingName types.NamespacedName

			setUpgradeCondition := func(status metav1.ConditionStatus, reason, failedVersion string) {
				GinkgoHelper()
				Expect(fakeK8sClient.Get(ctx, bindingName, &resourceBinding)).To(Succeed())
				apiMeta.SetStatusCondition(&resourceBinding.Status.Conditions, metav1.Condition{
					Type:   v1alpha1.UpgradeSucceededCondition,
					Status: status,
					Reason: reason,
				})
				resourceBinding.Status.FailedVersion = failedVersion
				Expect(fakeK8sClient.Status().Update(ctx, &resourceBinding)).To(Succeed())
			}

			expectManualReconciliationRetry := func() {
				GinkgoHelper()
				result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: bindingName})
				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(rr), rr)).To(Succeed())
				Expect(rr.GetLabels()).To(
					HaveKeyWithValue(resourceutil.ManualReconciliationLabel, "true"),
				)

				var rb v1alpha1.ResourceBinding
				Expect(fakeK8sClient.Get(ctx, bindingName, &rb)).To(Succeed())
				Expect(rb.GetLabels()).NotTo(HaveKey(resourceutil.ManualReconciliationLabel))
				cond := apiMeta.FindStatusCondition(rb.Status.Conditions, v1alpha1.UpgradeSucceededCondition)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
				Expect(cond.Reason).To(Equal(v1alpha1.UpgradeInProgressReason))
				Expect(cond.Message).To(Equal("Reconciliation requested for version v0.0.2"))
				Expect(rb.Status.FailedVersion).To(BeEmpty())
			}

			BeforeEach(func() {
				bindingName = types.NamespacedName{Name: resourceBindingName, Namespace: resourceBindingNamespace}
				Expect(fakeK8sClient.Get(ctx, bindingName, &resourceBinding)).To(Succeed())
				labels := resourceBinding.GetLabels()
				if labels == nil {
					labels = make(map[string]string)
				}
				labels[resourceutil.ManualReconciliationLabel] = "true"
				resourceBinding.SetLabels(labels)
				Expect(fakeK8sClient.Update(ctx, &resourceBinding)).To(Succeed())
			})

			DescribeTable("retries when the binding is labelled for manual reconciliation",
				func(rrVersion string, condStatus metav1.ConditionStatus, condReason, failedVersion string) {
					rr = createResourceRequestWithVersion(ctx, rrVersion)
					setUpgradeCondition(condStatus, condReason, failedVersion)
					expectManualReconciliationRetry()
				},
				Entry("when upgrade failed and resource version matches binding", "v0.0.2", metav1.ConditionFalse, v1alpha1.UpgradeFailedReason, "v0.0.2"),
				Entry("when upgrade succeeded and resource version matches binding", "v0.0.2", metav1.ConditionTrue, v1alpha1.UpgradeCompleteReason, ""),
				Entry("when upgrade failed and FailedVersion guard would block", "v0.0.1", metav1.ConditionFalse, v1alpha1.UpgradeFailedReason, "v0.0.2"),
			)

			It("does not retry when the resource promise version is unversioned", func() {
				rr = createResourceRequestWithVersion(ctx, controller.UnversionedPromiseVersion)
				setUpgradeCondition(metav1.ConditionFalse, v1alpha1.UpgradeFailedReason, "v0.0.2")

				result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: bindingName})
				Expect(err).ToNot(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(rr), rr)).To(Succeed())
				Expect(rr.GetLabels()).NotTo(HaveKey(resourceutil.ManualReconciliationLabel))

				var rb v1alpha1.ResourceBinding
				Expect(fakeK8sClient.Get(ctx, bindingName, &rb)).To(Succeed())
				Expect(rb.GetLabels()).To(HaveKeyWithValue(resourceutil.ManualReconciliationLabel, "true"))
			})
		})
	})
})

func createResourceRequestWithVersion(ctx context.Context, version string) *unstructured.Unstructured {
	GinkgoHelper()
	yamlFile, err := os.ReadFile(resourceRequestPath)
	Expect(err).ToNot(HaveOccurred())

	rr := &unstructured.Unstructured{}
	Expect(yaml.Unmarshal(yamlFile, rr)).To(Succeed())
	Expect(fakeK8sClient.Create(ctx, rr)).To(Succeed())
	resNameNamespacedName := types.NamespacedName{
		Name:      rr.GetName(),
		Namespace: rr.GetNamespace(),
	}

	resourceutil.SetStatus(rr, logr.Discard(), "promiseVersion", version)

	Expect(fakeK8sClient.Status().Update(ctx, rr)).To(Succeed())

	Expect(fakeK8sClient.Get(ctx, resNameNamespacedName, rr)).To(Succeed())
	return rr
}
