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
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/lib/workflow"

	fakeclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	fakeK8sClient           client.Client
	fakeApiExtensionsClient apiextensionsv1.CustomResourceDefinitionsGetter
	t                       *testReconciler
	interceptorsFuncs       interceptor.Funcs
	errSubResourceUpdate    error
)

var _ = BeforeSuite(func(_ SpecContext) {
	err := v1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	//+kubebuilder:scaffold:scheme

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

}, NodeTimeout(time.Minute))

var _ = AfterSuite(func() {
})

var reconcileConfigureOptsArg workflow.Opts
var reconcileDeleteOptsArg workflow.Opts

var _ = BeforeEach(func() {
	yamlFile, err := os.ReadFile(resourceRequestPath)
	Expect(err).ToNot(HaveOccurred())

	resReq := &unstructured.Unstructured{}
	Expect(yaml.Unmarshal(yamlFile, resReq)).To(Succeed())

	interceptorsFuncs = interceptor.Funcs{}
	interceptorsFuncs.SubResourceUpdate = func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
		if errSubResourceUpdate != nil {
			return errSubResourceUpdate
		}
		return client.Status().Update(ctx, obj, opts...)
	}

	fakeK8sClient = fake.NewClientBuilder().WithInterceptorFuncs(interceptorsFuncs).WithScheme(scheme.Scheme).WithStatusSubresource(
		&v1alpha1.PromiseRelease{},
		&v1alpha1.Promise{},
		&v1alpha1.Work{},
		&v1alpha1.WorkPlacement{},
		&v1alpha1.Destination{},
		&v1alpha1.GitStateStore{},
		&v1alpha1.BucketStateStore{},
		//Add redis.marketplace.kratix.io/v1alpha1 so we can update its status
		resReq,
	).Build()

	fakeApiExtensionsClient = fakeclientset.NewSimpleClientset().ApiextensionsV1()
	t = &testReconciler{}

	controller.SetReconcileConfigureWorkflow(func(w workflow.Opts) (bool, error) {
		reconcileConfigureOptsArg = w
		return true, nil
	})

	controller.SetReconcileDeleteWorkflow(func(w workflow.Opts) (bool, error) {
		reconcileDeleteOptsArg = w
		return true, nil
	})
})

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

func setReconcileConfigureWorkflowToReturnFinished() {
	controller.SetReconcileConfigureWorkflow(func(w workflow.Opts) (bool, error) {
		reconcileConfigureOptsArg = w
		return false, nil
	})
}

func setReconcileDeleteWorkflowToReturnFinished(obj client.Object) {
	controller.SetReconcileDeleteWorkflow(func(w workflow.Opts) (bool, error) {
		us := &unstructured.Unstructured{}
		us.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())
		Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}, us)).To(Succeed())

		reconcileDeleteOptsArg = w
		return false, nil
	})
}

func setReconcileDeleteWorkflowToReturnError(obj client.Object) {
	controller.SetReconcileDeleteWorkflow(func(w workflow.Opts) (bool, error) {
		us := &unstructured.Unstructured{}
		us.SetGroupVersionKind(obj.GetObjectKind().GroupVersionKind())
		Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
			Name:      obj.GetName(),
			Namespace: obj.GetNamespace(),
		}, us)).To(Succeed())

		reconcileDeleteOptsArg = w
		return false, workflow.ErrDeletePipelineFailed
	})
}
