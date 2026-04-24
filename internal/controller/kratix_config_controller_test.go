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
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
)

var _ = Describe("KratixConfigReconciler", func() {
	var (
		reconciler     *controller.KratixConfigReconciler
		receivedData   map[string]string
		callbackCalled bool
		configMap      *corev1.ConfigMap
	)

	BeforeEach(func() {
		callbackCalled = false
		receivedData = nil

		reconciler = &controller.KratixConfigReconciler{
			Client: fakeK8sClient,
			Log:    ctrl.Log.WithName("test"),
			OnConfigChange: func(data map[string]string) error {
				callbackCalled = true
				receivedData = data
				return nil
			},
		}

		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kratix",
				Namespace: v1alpha1.SystemNamespace,
			},
			Data: map[string]string{
				"config": `
workflows:
  defaultImagePullPolicy: Always
`,
			},
		}
	})

	When("the ConfigMap exists", func() {
		BeforeEach(func() {
			Expect(fakeK8sClient.Create(ctx, configMap)).To(Succeed())
		})

		AfterEach(func() {
			Expect(fakeK8sClient.Delete(ctx, configMap)).To(Succeed())
		})

		It("calls OnConfigChange with the ConfigMap data", func() {
			_, err := reconciler.Reconcile(ctx, requestForKratixConfig())
			Expect(err).NotTo(HaveOccurred())

			Expect(callbackCalled).To(BeTrue())
			Expect(receivedData).To(HaveKey("config"))
			Expect(receivedData["config"]).To(ContainSubstring("Always"))
		})

		When("OnConfigChange returns an error", func() {
			BeforeEach(func() {
				reconciler.OnConfigChange = func(_ map[string]string) error {
					return errors.New("config apply failed")
				}
			})

			It("returns the error", func() {
				_, err := reconciler.Reconcile(ctx, requestForKratixConfig())
				Expect(err).To(MatchError("config apply failed"))
			})
		})
	})

	When("the ConfigMap does not exist", func() {
		It("succeeds without calling OnConfigChange", func() {
			_, err := reconciler.Reconcile(ctx, requestForKratixConfig())
			Expect(err).NotTo(HaveOccurred())
			Expect(callbackCalled).To(BeFalse())
		})
	})

	When("the workflow defaults are applied via the callback", func() {
		BeforeEach(func() {
			reconciler.OnConfigChange = func(data map[string]string) error {
				v1alpha1.SetWorkflowDefaults(nil, "Never", nil, nil)
				return nil
			}
			Expect(fakeK8sClient.Create(ctx, configMap)).To(Succeed())
		})

		AfterEach(func() {
			Expect(fakeK8sClient.Delete(ctx, configMap)).To(Succeed())
			v1alpha1.SetWorkflowDefaults(nil, "", nil, nil)
		})

		It("updates workflow defaults so new pipeline jobs use the new values", func() {
			_, err := reconciler.Reconcile(ctx, requestForKratixConfig())
			Expect(err).NotTo(HaveOccurred())

			Expect(v1alpha1.DefaultImagePullPolicy).To(Equal(corev1.PullPolicy("Never")))
		})
	})
})

func requestForKratixConfig() ctrl.Request {
	return ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "kratix",
			Namespace: v1alpha1.SystemNamespace,
		},
	}
}
