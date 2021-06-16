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

package controllers

import (
	"context"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	platformv1alpha1 "github.com/syntasso/synpl-platform/api/v1alpha1"
	//+kubebuilder:scaffold:imports

	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	ctrl "sigs.k8s.io/controller-runtime"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var apiextensionClient *clientset.Clientset
var testEnv *envtest.Environment

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]Reporter{printer.NewlineReporter{}})
}

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

	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
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

var _ = Context("Promise Reconciler", func() {
	Describe("Creating Redis Promise CR", func() {
		Describe("Dynamic Redis CRD is applied from Redis Promise CR", func() {
			It("Creates an API for redis.redis.redis", func() {

				yamlFile, err := ioutil.ReadFile("../config/samples/redis/redis-promise.yaml")
				Expect(err).ToNot(HaveOccurred())

				promiseCR := &platformv1alpha1.Promise{}
				err = yaml.Unmarshal(yamlFile, promiseCR)
				promiseCR.Namespace = "default"
				Expect(err).ToNot(HaveOccurred())

				err = k8sClient.Create(context.Background(), promiseCR)
				Expect(err).ToNot(HaveOccurred())

				var expectedAPI = "redis.redis.redis.opstreelabs.in"
				var timeout = "30s"
				var interval = "3s"
				Eventually(func() string {
					crd, _ := apiextensionClient.
						ApiextensionsV1().
						CustomResourceDefinitions().
						Get(context.Background(), expectedAPI, metav1.GetOptions{})

					// The returned CRD is missing the expected metadata,
					// therefore we need to reach inside of the spec to get the
					// underlying Redis crd defintion to allow us to assert correctly.
					return crd.Spec.Names.Singular + "." + crd.Spec.Group
				}, timeout, interval).Should(Equal(expectedAPI))
			})
		})
	})

	Describe("Creating a Redis CR", func() {
		Describe("Creates a valid pod spec for the transformation pipeline", func() {
			It("Creates a pod spec", func() {
				yamlFile, err := ioutil.ReadFile("../config/samples/redis/redis-resource-request.yaml")
				Expect(err).ToNot(HaveOccurred())

				redisRequest := &unstructured.Unstructured{}
				err = yaml.Unmarshal(yamlFile, redisRequest)
				Expect(err).ToNot(HaveOccurred())

				redisRequest.SetNamespace("default")
				err = k8sClient.Create(context.Background(), redisRequest)
				Expect(err).ToNot(HaveOccurred())

				createdPod := v1.Pod{}
				expectedName := types.NamespacedName{
					Name:      "pipeline",
					Namespace: "default",
				}

				var timeout = "30s"
				var interval = "3s"
				Eventually(func() string {
					err := k8sClient.Get(context.Background(), expectedName, &createdPod)
					if err != nil {
						fmt.Println(err.Error())
						return ""
					}
					return createdPod.Spec.Containers[0].Name
				}, timeout, interval).Should(Equal("writer"))
			})

			// Test switched off
			//
			// This test needs to run against a k8s deployment that can run containers in order to execute the
			// transfomation we're asserting.
			//
			// TODO: Enable this in https://www.pivotaltracker.com/story/show/178496362
			XIt("Transforms the redis-resource-request", func() {
				var timeout = "10s"
				var interval = "3s"
				Eventually(func() string {
					gvk := schema.GroupVersionKind{
						Group:   "redis.redis.opstreelabs.in",
						Version: "v1beta1",
						Kind:    "Redis",
					}

					redisRequest := &unstructured.Unstructured{}
					redisRequest.SetGroupVersionKind(gvk)

					expectedName := types.NamespacedName{
						Name:      "opstree-redis",
						Namespace: "default",
					}

					err := k8sClient.Get(context.Background(), expectedName, redisRequest)
					if err != nil {
						fmt.Println(err.Error())
						return ""
					}
					fmt.Println(redisRequest)
					annotations := redisRequest.Object["annotations"]
					return fmt.Sprintf("%v", annotations)
				}, timeout, interval).Should(Equal("syntasso: true"))
			})
		})
	})
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	testEnv.Stop()
})
