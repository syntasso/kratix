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
	"fmt"
	"io/ioutil"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

var _ = Context("Promise Reconciler", func() {
	var promiseCR *platformv1alpha1.Promise

	//Should this be in the describe apply a redis promise?
	// Why the heck is the test failing if we get a cm?????
	BeforeEach(func() {
		promiseCR = &platformv1alpha1.Promise{}
		yamlFile, err := ioutil.ReadFile("../config/samples/redis/redis-promise.yaml")
		Expect(err).ToNot(HaveOccurred())
		err = yaml.Unmarshal(yamlFile, promiseCR)
		promiseCR.Namespace = "default"
		Expect(err).ToNot(HaveOccurred())

		//Works once, then fails as the promiseCR already exists. Consider building check here.
		k8sClient.Create(context.Background(), promiseCR)
	})

	It("Controls the lifecycle of a Redis Promise", func() {
		promiseIdentifier := "redis-promise-default"
		expectedPromise := types.NamespacedName{
			Namespace: "default",
			Name:      "redis-promise",
		}

		By("creating an API for redis.redis.redis")
		var expectedAPI = "redis.redis.redis.opstreelabs.in"
		Eventually(func() string {
			crd, _ := apiextensionClient.
				ApiextensionsV1().
				CustomResourceDefinitions().
				Get(context.Background(), expectedAPI, metav1.GetOptions{})

			// The returned CRD is missing the expected metadata,
			// therefore we need to reach inside the spec to get the
			// underlying Redis crd definition to allow us to assert correctly.
			return crd.Spec.Names.Singular + "." + crd.Spec.Group
		}, timeout, interval).Should(Equal(expectedAPI))

		controllerResourceNamespacedName := types.NamespacedName{Name: promiseIdentifier + "-promise-controller"}
		piplineResourceNamespacedName := types.NamespacedName{Name: promiseIdentifier + "-promise-pipeline"}

		By("creating clusterRoleBindings for the controller and pipeline")
		Eventually(func() error {
			binding := &rbacv1.ClusterRoleBinding{}
			err := k8sClient.Get(context.Background(), controllerResourceNamespacedName, binding)
			return err
		}, timeout, interval).Should(BeNil(), "Expected controller ClusterRoleBinding to exist")

		Eventually(func() error {
			binding := &rbacv1.ClusterRoleBinding{}
			err := k8sClient.Get(context.Background(), piplineResourceNamespacedName, binding)
			return err
		}, timeout, interval).Should(BeNil(), "Expected pipeline ClusterRoleBinding to exist")

		By("creating clusterRoles for the controller and pipeline")
		Eventually(func() error {
			clusterRole := &rbacv1.ClusterRole{}
			err := k8sClient.Get(context.Background(), controllerResourceNamespacedName, clusterRole)
			return err
		}, timeout, interval).Should(BeNil(), "Expected controller ClusterRole to exist")

		Eventually(func() error {
			clusterRole := &rbacv1.ClusterRole{}
			err := k8sClient.Get(context.Background(), piplineResourceNamespacedName, clusterRole)
			return err
		}, timeout, interval).Should(BeNil(), "Expected pipeline ClusterRole to exist")

		By("creating a serviceAccount for the pipeline")
		pipelineServiceAccountNamespacedName := types.NamespacedName{Name: piplineResourceNamespacedName.Name, Namespace: "default"}
		Eventually(func() error {
			serviceAccount := &v1.ServiceAccount{}
			err := k8sClient.Get(context.Background(), pipelineServiceAccountNamespacedName, serviceAccount)
			return err
		}, timeout, interval).Should(BeNil(), "Expected pipeline ServiceAccount to exist")

		By("being able to create RRs")
		yamlFile, err := ioutil.ReadFile("../config/samples/redis/redis-resource-request.yaml")
		Expect(err).ToNot(HaveOccurred())

		redisRequest := &unstructured.Unstructured{}
		err = yaml.Unmarshal(yamlFile, redisRequest)
		Expect(err).ToNot(HaveOccurred())

		redisRequest.SetNamespace("default")
		err = k8sClient.Create(context.Background(), redisRequest)
		Expect(err).ToNot(HaveOccurred())

		By("creating a configMap to store Promise selectors")
		Eventually(func() string {
			cm := &v1.ConfigMap{}
			expectedCM := types.NamespacedName{
				Namespace: "default",
				Name:      "cluster-selectors-" + promiseIdentifier,
			}

			err := k8sClient.Get(context.Background(), expectedCM, cm)
			if err != nil {
				fmt.Println(err.Error())
			}

			return cm.Data["selectors"]
		}, timeout, interval).Should(Equal(labels.FormatLabels(promiseCR.Spec.ClusterSelector)), "Expected redis cluster selectors to be in configMap")

		promise := &v1alpha1.Promise{}

		err = k8sClient.Get(context.Background(), expectedPromise, promise)
		Expect(err).NotTo(HaveOccurred())
		Expect(promise.GetFinalizers()).Should(
			ConsistOf(
				"kratix.io/cluster-selectors-config-map-cleanup",
				"kratix.io/resource-request-cleanup",
				"kratix.io/dynamic-controller-dependant-resources-cleanup",
			),
			"Promise should have finalizers set")

		By("creating Redis Worker Cluster Resources")
		workNamespacedName := types.NamespacedName{
			Name:      promiseIdentifier,
			Namespace: "default",
		}
		Eventually(func() error {
			err := k8sClient.Get(context.Background(), workNamespacedName, &v1alpha1.Work{})
			return err
		}, timeout, interval).Should(BeNil())

		By("deleting the Promise")
		err = k8sClient.Delete(context.Background(), promiseCR)
		Expect(err).NotTo(HaveOccurred())

		By("also deleting the resource requests")
		Eventually(func() int {
			rrList := &unstructured.UnstructuredList{}
			rrList.SetGroupVersionKind(redisRequest.GroupVersionKind())
			err := k8sClient.List(context.Background(), rrList)
			if err != nil {
				return -1
			}
			return len(rrList.Items)
		}, timeout, interval).Should(BeZero(), "Expected all RRs to be deleted")

		By("also deleting the config map")
		Eventually(func() bool {
			cm := &v1.ConfigMap{}
			expectedCM := types.NamespacedName{
				Namespace: "default",
				Name:      "cluster-selectors-redis-promise-default",
			}

			err := k8sClient.Get(context.Background(), expectedCM, cm)
			return errors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "ConfigMap should have been deleted")

		By("also deleting the ClusterRoleBinding for the controller and pipeline")
		Eventually(func() bool {
			binding := &rbacv1.ClusterRoleBinding{}
			err := k8sClient.Get(context.Background(), piplineResourceNamespacedName, binding)
			return errors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "Expected pipeline ClusterRoleBinding not to be found")

		Eventually(func() bool {
			binding := &rbacv1.ClusterRoleBinding{}
			err := k8sClient.Get(context.Background(), controllerResourceNamespacedName, binding)
			return errors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "Expected controller ClusterRoleBinding not to be found")

		By("also deleting the ClusterRole for the controller and pipeline")
		Eventually(func() bool {
			binding := &rbacv1.ClusterRole{}
			err := k8sClient.Get(context.Background(), piplineResourceNamespacedName, binding)
			return errors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "Expected pipeline ClusterRole not to be found")

		Eventually(func() bool {
			binding := &rbacv1.ClusterRole{}
			err := k8sClient.Get(context.Background(), controllerResourceNamespacedName, binding)
			return errors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "Expected controller ClusterRole not to be found")

		By("also deleting the serviceAccount for the pipeline")
		Eventually(func() bool {
			serviceAccount := &v1.ServiceAccount{}
			err := k8sClient.Get(context.Background(), pipelineServiceAccountNamespacedName, serviceAccount)
			return errors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "Expected pipleine ServiceAccount not to be found")

		By("finally deleting the Promise itself")
		Eventually(func() bool {
			err := k8sClient.Get(context.Background(), expectedPromise, &v1alpha1.Promise{})
			return errors.IsNotFound(err)
		}, timeout, interval).Should(BeTrue(), "Expected Promise not to be found")
	})

	Describe("Lifecycle of a Redis Custom Resource", func() {
		createdPod := v1.Pod{}

		redisRequest := &unstructured.Unstructured{}

		expectedName := types.NamespacedName{
			//The name of the pod is generated dynamically by the Promise Controller. For testing purposes, we set a TEST_PROMISE_CONTROLLER_POD_IDENTIFIER_UUID via an environment variable in the Makefile to make the name deterministic
			Name:      "request-pipeline-redis-promise-default-12345",
			Namespace: "default",
		}

		BeforeEach(func() {
			yamlFile, err := ioutil.ReadFile("../config/samples/redis/redis-resource-request.yaml")
			Expect(err).ToNot(HaveOccurred())

			err = yaml.Unmarshal(yamlFile, redisRequest)
			Expect(err).ToNot(HaveOccurred())

			redisRequest.SetNamespace("default")
		})

		It("Creates", func() {
			err := k8sClient.Create(context.Background(), redisRequest)
			Expect(err).ToNot(HaveOccurred())

			By("defining a valid pod spec for the transformation pipeline")
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

			By("setting the finalizer on the resource")
			Eventually(func() []string {
				createdRedisRequest := &unstructured.Unstructured{}
				createdRedisRequest.SetGroupVersionKind(redisRequest.GroupVersionKind())
				err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(redisRequest), createdRedisRequest)
				if err != nil {
					fmt.Println(err.Error())
					return nil
				}
				return createdRedisRequest.GetFinalizers()
			}, timeout, interval).Should(ConsistOf("kratix.io/work-cleanup", "kratix.io/pipeline-cleanup"))
		})

		It("Takes no action on update", func() {
			existingResourceRequest := &unstructured.Unstructured{}
			gvk := schema.GroupVersionKind{
				Group:   "redis.redis.opstreelabs.in",
				Version: "v1beta1",
				Kind:    "Redis",
			}
			existingResourceRequest.SetGroupVersionKind(gvk)
			ns := client.ObjectKeyFromObject(redisRequest)
			err := k8sClient.Get(context.Background(), ns, existingResourceRequest)
			Expect(err).ToNot(HaveOccurred())

			existingResourceRequest.SetAnnotations(map[string]string{
				"new-annotation": "auto-added",
			})
			err = k8sClient.Update(context.Background(), existingResourceRequest)
			Expect(err).ToNot(HaveOccurred())

			Consistently(func() int {
				isPromise, _ := labels.NewRequirement("kratix-promise-id", selection.Equals, []string{"redis-promise-default"})
				selector := labels.NewSelector().
					Add(*isPromise)

				listOps := &client.ListOptions{
					Namespace:     "default",
					LabelSelector: selector,
				}

				ol := &v1.PodList{}
				err := k8sClient.List(context.Background(), ol, listOps)
				if err != nil {
					fmt.Println(err.Error())
					return -1
				}
				return len(ol.Items)
			}, timeout, interval).Should(Equal(1))
		})

		It("Deletes the associated resources", func() {
			//create what the pipeline would have created: Work
			work = &platformv1alpha1.Work{}
			work.Name = "redis-promise-default-default-opstree-redis"
			work.Namespace = "default"
			err := k8sClient.Create(context.Background(), work)
			Expect(err).ToNot(HaveOccurred())

			//test delete
			err = k8sClient.Delete(context.Background(), redisRequest)
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() bool {
				work = &platformv1alpha1.Work{}
				work.Name = "redis-promise-default-default-opstree-redis"
				work.Namespace = "default"
				err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(work), work)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Expected the Work to be deleted")

			Eventually(func() bool {
				pipeline := &v1.Pod{}
				err := k8sClient.Get(context.Background(), expectedName, pipeline)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Expected the request pipeline to be deleted")

			Eventually(func() bool {
				createdRedisRequest := &unstructured.Unstructured{}
				createdRedisRequest.SetGroupVersionKind(redisRequest.GroupVersionKind())
				err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(redisRequest), createdRedisRequest)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Expected the Redis resource to be deleted")
		})
	})
})
