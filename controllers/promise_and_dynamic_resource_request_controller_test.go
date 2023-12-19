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
	"os"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

var _ = Context("Promise Reconciler", func() {
	var (
		promiseCR *v1alpha1.Promise
		ctx       = context.Background()

		requestedResource  *unstructured.Unstructured
		resourceCommonName types.NamespacedName
		yamlContent        = map[string]*v1alpha1.Promise{}
	)

	const (
		RedisPromisePath = "../config/samples/redis/redis-promise.yaml"
	)

	applyPromise := func(promisePath string) {
		var promiseFromYAML *v1alpha1.Promise
		var found bool
		if promiseFromYAML, found = yamlContent[promisePath]; !found {
			promiseFromYAML = parseYAML(promisePath)
			yamlContent[promisePath] = promiseFromYAML
		}

		k8sClient.Create(ctx, promiseFromYAML)
		promiseCR = getPromise(promiseFromYAML.GetName())
	}

	Describe("Promise reconciliation lifecycle", func() {
		var (
			promiseGroup = "redis.redis.opstreelabs.in"
		)

		BeforeEach(func() {
			applyPromise(RedisPromisePath)
			promiseCommonLabels = map[string]string{
				"kratix-promise-id": promiseCR.GetName(),
			}
		})

		When("a resource is requested", func() {
			var (
				resourceLabels map[string]string
			)

			requestOnce := func(resourcePath string) {
				if requestedResource != nil {
					return
				}
				yamlFile, err := os.ReadFile(resourcePath)
				Expect(err).ToNot(HaveOccurred())

				requestedResource = &unstructured.Unstructured{}
				Expect(yaml.Unmarshal(yamlFile, requestedResource)).To(Succeed())
				requestedResource.SetNamespace("default")
				Expect(k8sClient.Create(ctx, requestedResource)).To(Succeed())
			}

			BeforeEach(func() {
				requestOnce("../config/samples/redis/redis-resource-request.yaml")
				resourceCommonName = types.NamespacedName{
					Name:      promiseCR.GetName() + "-resource-pipeline",
					Namespace: "default",
				}

				resourceLabels = map[string]string{
					"kratix-promise-id": promiseCR.GetName(),
				}
			})

			It("creates a service account for pipeline", func() {
				sa := &v1.ServiceAccount{}
				Eventually(func() error {
					return k8sClient.Get(ctx, resourceCommonName, sa)
				}, timeout, interval).Should(Succeed(), "Expected SA for pipeline to exist")

				Expect(sa.GetLabels()).To(Equal(resourceLabels))
			})

			It("creates a role for the pipeline service account", func() {
				role := &rbacv1.Role{}
				Eventually(func() error {
					return k8sClient.Get(ctx, resourceCommonName, role)
				}, timeout, interval).Should(Succeed(), "Expected Role for pipeline to exist")

				Expect(role.GetLabels()).To(Equal(resourceLabels))
				Expect(role.Rules).To(ConsistOf(
					rbacv1.PolicyRule{
						Verbs:     []string{"get", "list", "update", "create", "patch"},
						APIGroups: []string{promiseGroup},
						Resources: []string{"redises", "redises/status"},
					},
					rbacv1.PolicyRule{
						Verbs:     []string{"get", "update", "create", "patch"},
						APIGroups: []string{"platform.kratix.io"},
						Resources: []string{"works"},
					},
				))
				Expect(role.GetLabels()).To(Equal(resourceLabels))
			})

			It("associates the new role with the new service account", func() {
				binding := &rbacv1.RoleBinding{}
				Eventually(func() error {
					return k8sClient.Get(ctx, resourceCommonName, binding)
				}, timeout, interval).Should(Succeed(), "Expected RoleBinding for pipeline to exist")
				Expect(binding.RoleRef.Name).To(Equal(resourceCommonName.Name))
				Expect(binding.Subjects).To(HaveLen(1))
				Expect(binding.Subjects[0]).To(Equal(rbacv1.Subject{
					Kind:      "ServiceAccount",
					Namespace: requestedResource.GetNamespace(),
					Name:      resourceCommonName.Name,
				}))
				Expect(binding.GetLabels()).To(Equal(resourceLabels))
			})

			It("creates a config map with the promise scheduling in it", func() {
				configMap := &v1.ConfigMap{}
				configMapName := types.NamespacedName{
					Name:      "destination-selectors-" + promiseCR.GetName(),
					Namespace: "default",
				}
				Eventually(func() error {
					return k8sClient.Get(ctx, configMapName, configMap)
				}, timeout, interval).Should(Succeed(), "Expected ConfigMap for pipeline to exist")
				Expect(configMap.GetLabels()).To(Equal(resourceLabels))
				Expect(configMap.Data).To(HaveKey("destinationSelectors"))
				space := regexp.MustCompile(`\s+`)
				destinationSelectors := space.ReplaceAllString(configMap.Data["destinationSelectors"], " ")
				Expect(strings.TrimSpace(destinationSelectors)).To(Equal(`- matchlabels: environment: dev source: promise`))
			})

			It("adds finalizers to the Promise", func() {
				promise := &v1alpha1.Promise{}
				expectedPromise := types.NamespacedName{
					Name: promiseCR.Name,
				}

				Expect(k8sClient.Get(ctx, expectedPromise, promise)).To(Succeed())
				Expect(promise.GetFinalizers()).Should(
					ConsistOf(
						"kratix.io/resource-request-cleanup",
						"kratix.io/dynamic-controller-dependant-resources-cleanup",
						"kratix.io/api-crd-cleanup",
						"kratix.io/dependencies-cleanup",
					),
					"Promise should have finalizers set",
				)
			})

			It("triggers the resource configure workflow", func() {
				Eventually(func() int {
					jobs := getResourceConfigurePipelineJobs(promiseCR, k8sClient)
					return len(jobs)
				}, timeout, interval).Should(Equal(1), "Configure Pipeline never trigerred")
			})
		})

		When("a resource is updated", func() {
			It("retriggers the resource configure workflow", func() {
				toUpdate := getRedisResource(requestedResource, k8sClient)
				toUpdate.Object["spec"].(map[string]interface{})["mode"] = "cluster"
				Expect(k8sClient.Update(ctx, toUpdate)).To(Succeed())

				completeAllJobs(k8sClient)

				Eventually(func() int {
					jobs := getResourceConfigurePipelineJobs(promiseCR, k8sClient)
					return len(jobs)
				}, "60s", interval).Should(Equal(2), "Expected 2 pipeline jobs")
			})
		})

		When("a resource is deleted", func() {
			BeforeEach(func() {
				Expect(k8sClient.Delete(ctx, requestedResource)).To(Succeed())
			})

			It("executes the deletion process", func() {
				deletePipeline := batchv1.Job{}

				By("triggering the delete pipeline for the promise", func() {
					Eventually(func() bool {
						jobs := &batchv1.JobList{}
						err := k8sClient.List(ctx, jobs)
						if err != nil {
							return false
						}

						if len(jobs.Items) == 0 {
							return false
						}

						for _, job := range jobs.Items {
							if strings.HasPrefix(job.Name, "delete-pipeline-redis-promise") {
								deletePipeline = job
								return true
							}
						}
						return false
					}, timeout, interval).Should(BeTrue(), "Expected the delete pipeline to be created")
				})

				By("deleting the resources when the pipeline completes", func() {
					deletePipeline.Status.Succeeded = 1
					Expect(k8sClient.Status().Update(ctx, &deletePipeline)).To(Succeed())

					Eventually(func() int {
						rrList := &unstructured.UnstructuredList{}
						rrList.SetGroupVersionKind(requestedResource.GroupVersionKind())
						err := k8sClient.List(ctx, rrList)
						if err != nil {
							return -1
						}
						return len(rrList.Items)
					}, timeout, interval).Should(BeZero(), "Expected all RRs to be deleted")
				})

				By("removing the work for the resource", func() {
					workNamespacedName := types.NamespacedName{
						Name:      promiseCR.GetName() + "-" + requestedResource.GetName(),
						Namespace: requestedResource.GetNamespace(),
					}
					Eventually(func() bool {
						return errors.IsNotFound(k8sClient.Get(ctx, workNamespacedName, &v1alpha1.Work{}))
					}, timeout, interval).Should(BeTrue())
				})

				By("leaving the pipeline resources (SA/Role/RoleBinding/ConfigMap) intact", func() {
					resources := []client.Object{
						&rbacv1.Role{},
						&rbacv1.RoleBinding{},
						&v1.ServiceAccount{},
					}
					for _, resource := range resources {
						Expect(k8sClient.Get(ctx, resourceCommonName, resource)).To(Succeed())
					}

					configMap := &v1.ConfigMap{}
					configMapName := types.NamespacedName{
						Name:      "destination-selectors-" + promiseCR.GetName(),
						Namespace: "default",
					}
					Eventually(func() error {
						return k8sClient.Get(ctx, configMapName, configMap)
					}, timeout, interval).Should(Succeed(), "Expected ConfigMap for pipeline to exist")
				})
			})
		})
	})
})

func completeAllJobs(k8sClient client.Client) {
	jobs := &batchv1.JobList{}
	Expect(k8sClient.List(context.Background(), jobs)).To(Succeed())

	for _, job := range jobs.Items {
		job.Status = batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: v1.ConditionTrue,
				},
			},
			Succeeded: 1,
		}
		Expect(k8sClient.Status().Update(context.Background(), &job)).To(Succeed())
	}
}

func getRedisResource(resource *unstructured.Unstructured, k8sClient client.Client) *unstructured.Unstructured {
	res := &unstructured.Unstructured{}
	res.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "redis.redis.opstreelabs.in",
		Version: "v1beta1",
		Kind:    "Redis",
	})

	Expect(k8sClient.Get(context.Background(), types.NamespacedName{
		Namespace: resource.GetNamespace(),
		Name:      resource.GetName(),
	}, res)).To(Succeed())

	return res
}

func getPromiseConfigurePipelineJobs(promiseCR *v1alpha1.Promise, k8sClient client.Client) []batchv1.Job {
	jobs := &batchv1.JobList{}
	lo := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			"kratix-promise-id":      promiseCR.GetName(),
			"kratix-workflow-type":   "promise",
			"kratix-workflow-action": "configure",
		}),
	}
	Expect(k8sClient.List(context.Background(), jobs, lo)).To(Succeed())
	return jobs.Items
}

func getResourceConfigurePipelineJobs(promiseCR *v1alpha1.Promise, k8sClient client.Client) []batchv1.Job {
	jobs := &batchv1.JobList{}
	lo := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			"kratix-promise-id":      promiseCR.GetName(),
			"kratix-workflow-type":   "resource",
			"kratix-workflow-action": "configure",
		}),
	}
	Expect(k8sClient.List(context.Background(), jobs, lo)).To(Succeed())
	return jobs.Items
}

func parseYAML(promisePath string) *v1alpha1.Promise {
	promiseFromYAML := &v1alpha1.Promise{}
	yamlFile, err := os.ReadFile(promisePath)
	Expect(err).ToNot(HaveOccurred())
	Expect(yaml.Unmarshal(yamlFile, promiseFromYAML)).To(Succeed())
	return promiseFromYAML
}

func deleteAndWait(promise *v1alpha1.Promise) {
	ctx := context.Background()
	ExpectWithOffset(1, k8sClient.Delete(ctx, promise)).To(Succeed())
	EventuallyWithOffset(1, func() error {
		completeAllJobs(k8sClient)
		return k8sClient.Get(ctx, types.NamespacedName{
			Name: promise.GetName(),
		}, promise)
	}, timeout, interval).Should(HaveOccurred())
}

func createDestination(name string, labels ...map[string]string) *v1alpha1.Destination {
	destination := &v1alpha1.Destination{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: v1alpha1.KratixSystemNamespace,
		},
	}

	if len(labels) > 0 {
		destination.SetLabels(labels[0])
	}

	Expect(k8sClient.Create(context.Background(), destination)).To(Succeed())
	return destination
}

func getPromise(promiseName string) *v1alpha1.Promise {
	promise := &v1alpha1.Promise{}
	Eventually(func() error {
		return k8sClient.Get(context.Background(), types.NamespacedName{Name: promiseName}, promise)
	}).Should(Succeed())
	return promise
}

func waitForWork(workName string) *v1alpha1.Work {
	work := &v1alpha1.Work{}
	Eventually(func() error {
		return k8sClient.Get(context.Background(), types.NamespacedName{
			Name:      "redis-promise",
			Namespace: v1alpha1.KratixSystemNamespace,
		}, work)
	}, timeout, interval).Should(Succeed())
	return work
}
