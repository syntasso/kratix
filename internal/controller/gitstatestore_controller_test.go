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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
)

var _ = Describe("GitStateStore Controller", func() {
	var (
		gitStateStore *v1alpha1.GitStateStore
		reconciler    *controller.GitStateStoreReconciler
	)

	BeforeEach(func() {
		reconciler = &controller.GitStateStoreReconciler{
			Client: fakeK8sClient,
			Scheme: scheme.Scheme,
			Log:    ctrl.Log.WithName("controllers").WithName("GitStateStore"),
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
				URL:        "https://github.com/org/default-store",
				AuthMethod: "BasicAuth",
			},
		}
	})

	When("the gitStateStore does not exists", func() {
		It("reconciles without error and does not requeue", func() {
			result, err := t.reconcileUntilCompletion(reconciler, gitStateStore)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})
	})
})
