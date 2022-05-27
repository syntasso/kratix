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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

var _ = Context("Promise Reconciler", func() {
	Describe("Apply a Redis Promise", func() {
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

	Describe("Creating a Redis Custom Resource", func() {
		It("Creates a valid pod spec for the transformation pipeline", func() {
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
				//The name of the pod is generated dynamically by the Promise Controller. For testing purposes, we set a TEST_PROMISE_CONTROLLER_POD_IDENTIFIER_UUID via an environment variable in the Makefile to make the name deterministic
				Name:      "request-pipeline-redis-promise-default-12345",
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
	})
})
