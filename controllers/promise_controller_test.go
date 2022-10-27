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
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"io/ioutil"
	v1 "k8s.io/api/core/v1"
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
	BeforeEach(func() {
		yamlFile, err := ioutil.ReadFile("../config/samples/redis/redis-promise.yaml")
		Expect(err).ToNot(HaveOccurred())

		promiseCR := &platformv1alpha1.Promise{}
		err = yaml.Unmarshal(yamlFile, promiseCR)
		promiseCR.Namespace = "default"
		Expect(err).ToNot(HaveOccurred())

		//Works once, then fails as the promiseCR already exists. Consider building check here.
		k8sClient.Create(context.Background(), promiseCR)
	})

	Describe("Apply a Redis Promise", func() {
		It("Creates an API for redis.redis.redis", func() {
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

		It("Creates Redis Worker Cluster Resources", func() {
			var timeout = "30s"
			var interval = "3s"
			Eventually(func() bool {
				//Get the works object
				expectedName := types.NamespacedName{
					Name:      "redis-promise-default",
					Namespace: "default",
				}
				err := k8sClient.Get(context.Background(), expectedName, &v1alpha1.Work{})
				return err == nil
			}, timeout, interval).Should(BeTrue())
		})
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
				err := k8sClient.Get(context.Background(), types.NamespacedName{
					Namespace: redisRequest.GetNamespace(),
					Name:      redisRequest.GetName(),
				}, createdRedisRequest)
				if err != nil {
					fmt.Println(err.Error())
					return nil
				}
				return createdRedisRequest.GetFinalizers()
			}, timeout, interval).Should(ConsistOf("finalizers.redis.resource-request.kratix.io/work-cleanup"))
		})

		It("Takes no action on update", func() {
			existingResourceRequest := &unstructured.Unstructured{}
			gvk := schema.GroupVersionKind{
				Group:   "redis.redis.opstreelabs.in",
				Version: "v1beta1",
				Kind:    "Redis",
			}
			existingResourceRequest.SetGroupVersionKind(gvk)
			ns := types.NamespacedName{
				Name:      redisRequest.GetName(),
				Namespace: redisRequest.GetNamespace(),
			}
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

		It("Deletes the associated Work when it is deleted", func() {
			//create what the pipeline would of creatd: Work
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
				createdRedisRequest := &unstructured.Unstructured{}
				createdRedisRequest.SetGroupVersionKind(redisRequest.GroupVersionKind())
				err := k8sClient.Get(context.Background(), types.NamespacedName{
					Namespace: redisRequest.GetNamespace(),
					Name:      redisRequest.GetName(),
				}, createdRedisRequest)
				return errors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "Expected the Redis resource to be deleted")
		})
	})
})
