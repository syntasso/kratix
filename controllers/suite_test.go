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
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	. "github.com/syntasso/kratix/controllers"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var k8sClient client.Client
var apiextensionClient *clientset.Clientset
var testEnv *envtest.Environment
var k8sManager ctrl.Manager

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	cfg, err := testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = platformv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	//+kubebuilder:scaffold:scheme

	apiextensionClient = clientset.NewForConfigOrDie(cfg)
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	k8sManager, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	err = (&PromiseReconciler{
		Manager:             k8sManager,
		ApiextensionsClient: apiextensionClient,
		Client:              k8sManager.GetClient(),
		Scheme:              k8sManager.GetScheme(),
		Log:                 ctrl.Log.WithName("controllers").WithName("PromiseReconciler"),
	}).SetupWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctrl.SetupSignalHandler())
		Expect(err).ToNot(HaveOccurred())
	}()

}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	testEnv.Stop()
})

var _ = AfterEach(func() {
	var workPlacements platformv1alpha1.WorkPlacementList
	k8sClient.List(context.Background(), &workPlacements)

	for _, wp := range workPlacements.Items {
		wp.Finalizers = nil
		k8sClient.Update(context.Background(), &wp)
	}

	Expect(k8sClient.DeleteAllOf(context.Background(), &platformv1alpha1.Cluster{}, client.InNamespace("default"))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(context.Background(), &platformv1alpha1.Promise{}, client.InNamespace("default"))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(context.Background(), &platformv1alpha1.Work{}, client.InNamespace("default"))).To(Succeed())
	Expect(k8sClient.DeleteAllOf(context.Background(), &platformv1alpha1.WorkPlacement{}, client.InNamespace("default"))).To(Succeed())
})

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}
