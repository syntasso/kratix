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
	"os"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

var _ = Context("Promise Reconciler", func() {
	var (
		promiseCR           *v1alpha1.Promise
		ctx                 = context.Background()
		promiseCommonLabels map[string]string

		expectedCRDName = "redises.redis.redis.opstreelabs.in"

		requestedResource                     *unstructured.Unstructured
		promiseCommonName, resourceCommonName types.NamespacedName
		yamlContent                           = map[string]*v1alpha1.Promise{}
	)

	const (
		RedisPromisePath                          = "../config/samples/redis/redis-promise.yaml"
		promiseWithWorkflow                       = "assets/promise-with-workflow.yaml"
		promiseWithRequirements                   = "assets/promise-with-requirements.yaml"
		resourceRequestForPromiseWithRequirements = "assets/namespace-resource-request.yaml"
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

	Describe("Promise reconciliation lifecycle", func() {
		var (
			promiseGroup        = "redis.redis.opstreelabs.in"
			promiseResourceName = "redises"
		)

		BeforeEach(func() {
			applyPromise(RedisPromisePath)
			promiseCommonLabels = map[string]string{
				"kratix-promise-id": promiseCR.GetName(),
			}
		})

		When("installing a Promise", func() {
			It("creates a CRD for the promise", func() {
				Eventually(func() string {
					crd, _ := apiextensionClient.
						ApiextensionsV1().
						CustomResourceDefinitions().
						Get(ctx, expectedCRDName, metav1.GetOptions{})

					// The returned CRD is missing the expected metadata,
					// therefore we need to reach inside the spec to get the
					// underlying Redis crd definition to allow us to assert correctly.
					return crd.Spec.Names.Plural + "." + crd.Spec.Group
				}, timeout, interval).Should(Equal(expectedCRDName))
			})

			It("creates a ClusterRole to access the Promise CRD", func() {
				clusterRoleName := types.NamespacedName{
					Name: promiseCR.GetControllerResourceName(),
				}

				clusterrole := &rbacv1.ClusterRole{}
				Eventually(func() error {
					return k8sClient.Get(ctx, clusterRoleName, clusterrole)
				}, timeout, interval).Should(Succeed(), "Expected controller ClusterRole to exist")

				Expect(clusterrole.Rules).To(ConsistOf(
					rbacv1.PolicyRule{
						Verbs:     []string{rbacv1.VerbAll},
						APIGroups: []string{promiseGroup},
						Resources: []string{promiseResourceName},
					},
					rbacv1.PolicyRule{
						Verbs:     []string{"update"},
						APIGroups: []string{promiseGroup},
						Resources: []string{promiseResourceName + "/finalizers"},
					},
					rbacv1.PolicyRule{
						Verbs:     []string{"get", "update", "patch"},
						APIGroups: []string{promiseGroup},
						Resources: []string{promiseResourceName + "/status"},
					},
				))

				Expect(clusterrole.GetLabels()).To(Equal(promiseCommonLabels))
			})

			It("binds the Kratix SA the newly created ClusterRole", func() {
				bindingName := types.NamespacedName{
					Name: promiseCR.GetControllerResourceName(),
				}

				binding := &rbacv1.ClusterRoleBinding{}
				Eventually(func() error {
					return k8sClient.Get(ctx, bindingName, binding)
				}, timeout, interval).Should(Succeed(), "Expected controller binding to exist")

				Expect(binding.RoleRef.Name).To(Equal(promiseCR.GetControllerResourceName()))
				Expect(binding.Subjects).To(HaveLen(1))
				Expect(binding.Subjects[0]).To(Equal(rbacv1.Subject{
					Kind:      "ServiceAccount",
					Namespace: v1alpha1.KratixSystemNamespace,
					Name:      "kratix-platform-controller-manager",
				}))
				Expect(binding.GetLabels()).To(Equal(promiseCommonLabels))
			})

			It("creates works for the dependencies, on the "+v1alpha1.KratixSystemNamespace+" namespace", func() {
				workNamespacedName := types.NamespacedName{
					Name:      promiseCR.GetName(),
					Namespace: v1alpha1.KratixSystemNamespace,
				}
				Eventually(func() error {
					return k8sClient.Get(ctx, workNamespacedName, &v1alpha1.Work{})
				}, timeout, interval).Should(Succeed())
			})

			It("sets the status to available", func() {
				Eventually(func(g Gomega) {
					promise := getPromise(promiseCR.GetName())
					g.Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusAvailable))
					g.Expect(promise.Status.Conditions).To(HaveLen(1))
					g.Expect(promise.Status.Conditions[0].Type).To(Equal("RequirementsFulfilled"))
					g.Expect(promise.Status.Conditions[0].Status).To(Equal(metav1.ConditionTrue))
					g.Expect(promise.Status.Conditions[0].Message).To(Equal("Requirements fulfilled"))
					g.Expect(promise.Status.Conditions[0].Reason).To(Equal("RequirementsInstalled"))
					g.Expect(promise.Status.Conditions[0].LastTransitionTime).ToNot(BeNil())
					g.Expect(promise.Status.Requirements).To(BeEmpty())
				}, timeout, interval).Should(Succeed())
			})

			When("the promise has a configure workflow", func() {
				BeforeEach(func() {
					applyPromise(promiseWithWorkflow)
					promiseCommonLabels = map[string]string{
						"kratix-promise-id": promiseCR.GetName(),
					}

					promiseCommonName = types.NamespacedName{
						Name:      promiseCR.GetName() + "-promise-pipeline",
						Namespace: "kratix-platform-system",
					}
				})

				It("creates a service account for pipeline", func() {
					sa := &v1.ServiceAccount{}
					Eventually(func() error {
						return k8sClient.Get(ctx, promiseCommonName, sa)
					}, timeout, interval).Should(Succeed(), "Expected SA for pipeline to exist")

					Expect(sa.GetLabels()).To(Equal(promiseCommonLabels))
				})

				It("creates a role for the pipeline service account", func() {
					role := &rbacv1.ClusterRole{}
					Eventually(func() error {
						return k8sClient.Get(ctx, promiseCommonName, role)
					}, timeout, interval).Should(Succeed(), "Expected Role for pipeline to exist")

					Expect(role.GetLabels()).To(Equal(promiseCommonLabels))
					Expect(role.Rules).To(ConsistOf(
						rbacv1.PolicyRule{
							Verbs:     []string{"get", "list", "update", "create", "patch"},
							APIGroups: []string{"platform.kratix.io"},
							Resources: []string{"promises", "promises/status", "works"},
						},
					))
					Expect(role.GetLabels()).To(Equal(promiseCommonLabels))
				})

				It("associates the new role with the new service account", func() {
					binding := &rbacv1.ClusterRoleBinding{}
					Eventually(func() error {
						return k8sClient.Get(ctx, promiseCommonName, binding)
					}, timeout, interval).Should(Succeed(), "Expected ClusterRoleBinding for pipeline to exist")
					Expect(binding.RoleRef.Name).To(Equal(promiseCommonName.Name))
					Expect(binding.Subjects).To(HaveLen(1))
					Expect(binding.Subjects[0]).To(Equal(rbacv1.Subject{
						Kind:      "ServiceAccount",
						Namespace: "kratix-platform-system",
						Name:      promiseCommonName.Name,
					}))
					Expect(binding.GetLabels()).To(Equal(promiseCommonLabels))
				})

				It("creates a config map with the promise scheduling in it", func() {
					configMap := &v1.ConfigMap{}
					configMapName := types.NamespacedName{
						Name:      "destination-selectors-" + promiseCR.GetName(),
						Namespace: "kratix-platform-system",
					}
					Eventually(func() error {
						return k8sClient.Get(ctx, configMapName, configMap)
					}, timeout, interval).Should(Succeed(), "Expected ConfigMap for pipeline to exist")
					Expect(configMap.GetLabels()).To(Equal(promiseCommonLabels))
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

					Eventually(func() []string {
						Expect(k8sClient.Get(ctx, expectedPromise, promise)).To(Succeed())
						return promise.GetFinalizers()
					}, timeout, interval).Should(
						ConsistOf(
							"kratix.io/dependencies-cleanup",
							"kratix.io/workflows-cleanup",
						),
						"Promise should have finalizers set",
					)
				})

				It("triggers the promise configure workflow", func() {
					Eventually(func() int {
						jobs := getPromiseConfigurePipelineJobs(promiseCR, k8sClient)
						return len(jobs)
					}, timeout, interval).Should(Equal(1), "Configure Pipeline never trigerred")
				})

				When("deleting", func() {
					It("removes the workflows jobs", func() {
						Expect(k8sClient.Delete(ctx, promiseCR)).To(Succeed())
						Eventually(func() int {
							jobs := getPromiseConfigurePipelineJobs(promiseCR, k8sClient)
							return len(jobs)
						}, timeout, interval).Should(Equal(0), "Configure Pipeline never deleted")
					})
				})
			})
		})

		When("a resource is requested", func() {
			var (
				resourceLabels map[string]string
			)

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

		When("a promise is deleted", func() {
			BeforeEach(func() {
				Expect(k8sClient.Delete(ctx, promiseCR)).To(Succeed())
			})

			It("deletes all the resources", func() {
				expectedPromise := types.NamespacedName{
					Name: promiseCR.Name,
				}

				By("deleting all the pipeline resources", func() {
					configMap := &v1.ConfigMap{}
					configMapName := types.NamespacedName{
						Name:      "destination-selectors-" + promiseCR.GetName(),
						Namespace: "default",
					}
					Eventually(func() bool {
						return errors.IsNotFound(k8sClient.Get(ctx, configMapName, configMap))
					}, timeout, interval).Should(BeTrue(), "ConfigMap was never deleted")

					resources := []client.Object{
						&rbacv1.Role{},
						&rbacv1.RoleBinding{},
						&v1.ServiceAccount{},
					}
					for i, resource := range resources {
						Eventually(func() bool {
							return errors.IsNotFound(k8sClient.Get(ctx, resourceCommonName, resource))
						}, timeout, interval).Should(BeTrue(), fmt.Sprintf("Resource %d was never deleted", i))
					}
				})

				By("deleting all the promise resources", func() {
					resources := []client.Object{
						&rbacv1.ClusterRole{},
						&rbacv1.ClusterRoleBinding{},
					}
					name := types.NamespacedName{
						Name: promiseCR.GetControllerResourceName(),
					}

					for i, resource := range resources {
						Eventually(func() bool {
							return errors.IsNotFound(k8sClient.Get(ctx, name, resource))
						}, timeout, interval).Should(BeTrue(), fmt.Sprintf("Resource %d was never deleted", i))
					}
				})

				By("deleting the Promise CRD", func() {
					Eventually(func() bool {
						_, err := apiextensionClient.
							ApiextensionsV1().
							CustomResourceDefinitions().
							Get(ctx, expectedCRDName, metav1.GetOptions{})

						return errors.IsNotFound(err)
					}, timeout, interval).Should(BeTrue(), "Expected CRD to not be found")
				})

				By("deleting the work", func() {
					Eventually(func() bool {
						workNamespacedName := types.NamespacedName{
							Name:      promiseCR.GetName(),
							Namespace: v1alpha1.KratixSystemNamespace,
						}
						err := k8sClient.Get(ctx, workNamespacedName, &v1alpha1.Work{})
						return errors.IsNotFound(err)
					}, timeout, interval).Should(BeTrue(), "Expected Work to not be found")
				})

				By("deleting the Promise itself", func() {
					Eventually(func() bool {
						err := k8sClient.Get(ctx, expectedPromise, &v1alpha1.Promise{})
						return errors.IsNotFound(err)
					}, timeout, interval).Should(BeTrue(), "Expected Promise not to be found")
				})
			})
		})
	})

	Describe("Can support Promises that only contain dependencies", func() {
		var (
			promiseIdentifier string
			expectedPromise   types.NamespacedName
		)

		BeforeEach(func() {
			applyPromise("../config/samples/nil-api-promise.yaml")
			promiseIdentifier = "nil-api-promise"
			expectedPromise = types.NamespacedName{
				Name: "nil-api-promise",
			}
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, promiseCR)).To(Succeed())
		})

		It("only creates a work resource", func() {
			By("creating dependencies", func() {
				workNamespacedName := types.NamespacedName{
					Name:      promiseIdentifier,
					Namespace: v1alpha1.KratixSystemNamespace,
				}
				Eventually(func() error {
					err := k8sClient.Get(ctx, workNamespacedName, &v1alpha1.Work{})
					return err
				}, timeout, interval).Should(Succeed())
			})

			By("setting the correct finalizers", func() {
				Eventually(func() []string {
					promise := &v1alpha1.Promise{}
					err := k8sClient.Get(ctx, expectedPromise, promise)
					Expect(err).NotTo(HaveOccurred())
					return promise.GetFinalizers()
				}, timeout, interval).Should(
					ConsistOf(
						"kratix.io/dependencies-cleanup",
					), "Promise should have finalizers set")
			})
		})
	})

	Describe("handles Promise status field", func() {
		BeforeEach(func() {
			applyPromise("./assets/redis-simple-promise.yaml")
		})

		AfterEach(func() {
			Expect(k8sClient.Delete(ctx, promiseCR)).To(Succeed())
		})

		It("automatically creates status and additionalPrinter fields for Penny", func() {
			var crd *apiextensions.CustomResourceDefinition
			expectedCRDName = "redis.marketplace.kratix.io"

			Eventually(func() string {
				crd, _ = apiextensionClient.
					ApiextensionsV1().
					CustomResourceDefinitions().
					Get(ctx, expectedCRDName, metav1.GetOptions{})

				// The returned CRD is missing the expected metadata,
				// therefore we need to reach inside the spec to get the
				// underlying Redis crd definition to allow us to assert correctly.
				return crd.Spec.Names.Plural + "." + crd.Spec.Group
			}, timeout, interval).Should(Equal(expectedCRDName))

			status, ok := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"]
			Expect(ok).To(BeTrue(), "expected .status to exist")

			Expect(status.XPreserveUnknownFields).ToNot(BeNil())
			Expect(*status.XPreserveUnknownFields).To(BeTrue())

			message, ok := status.Properties["message"]
			Expect(ok).To(BeTrue(), ".status.message did not exist. Spec %v", status)
			Expect(message.Type).To(Equal("string"))

			conditions, ok := status.Properties["conditions"]
			Expect(ok).To(BeTrue())
			Expect(conditions.Type).To(Equal("array"))

			conditionsProperties := conditions.Items.Schema.Properties

			lastTransitionTime, ok := conditionsProperties["lastTransitionTime"]
			Expect(ok).To(BeTrue())
			Expect(lastTransitionTime.Type).To(Equal("string"))

			message, ok = conditionsProperties["message"]
			Expect(ok).To(BeTrue())
			Expect(message.Type).To(Equal("string"))

			reason, ok := conditionsProperties["reason"]
			Expect(ok).To(BeTrue())
			Expect(reason.Type).To(Equal("string"))

			status, ok = conditionsProperties["status"]
			Expect(ok).To(BeTrue())
			Expect(status.Type).To(Equal("string"))

			typeField, ok := conditionsProperties["type"]
			Expect(ok).To(BeTrue())
			Expect(typeField.Type).To(Equal("string"))

			printerFields := crd.Spec.Versions[0].AdditionalPrinterColumns
			Expect(printerFields).ToNot(BeNil())
		})
	})

	Describe("updating a promise", func() {
		When("the destinationSelectors get updated", func() {
			var (
				promise      *v1alpha1.Promise
				destinationA *v1alpha1.Destination
				destinationB *v1alpha1.Destination
			)

			BeforeEach(func() {
				destinationA = createDestination("destination-a")
				destinationB = createDestination("destination-b")

				promise = parseYAML(RedisPromisePath)
				promise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{
					{
						MatchLabels: map[string]string{
							"destination": "a",
						},
					},
				}
				Expect(k8sClient.Create(ctx, promise)).To(Succeed())

				waitForWork(promise.GetName())

				promise = getPromise(promise.GetName())
				promise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{
					{
						MatchLabels: map[string]string{
							"destination": "b",
						},
					},
				}
				Expect(k8sClient.Update(ctx, promise)).To(Succeed())
			})

			It("schedules any dependencies to the new destinations", func() {
				Eventually(func() v1alpha1.WorkloadGroupScheduling {
					work := waitForWork(promise.GetName())
					return work.Spec.WorkloadGroups[0].DestinationSelectors[0]
				}, timeout, interval).Should(Equal(v1alpha1.WorkloadGroupScheduling{
					MatchLabels: map[string]string{
						"destination": "b",
					},
					Source: "promise",
				},
				))
			})

			AfterEach(func() {
				deleteAndWait(promise)
				Expect(k8sClient.Delete(ctx, destinationA)).To(Succeed())
				Expect(k8sClient.Delete(ctx, destinationB)).To(Succeed())
			})
		})

		When("the dependencies get updated", func() {
			var (
				promise *v1alpha1.Promise
			)

			BeforeEach(func() {

				promise = parseYAML(RedisPromisePath)
				Expect(k8sClient.Create(ctx, promise)).To(Succeed())

				waitForWork(promise.GetName())
				Eventually(func() string {
					work := waitForWork(promise.GetName())
					return work.Spec.WorkloadGroups[0].Workloads[0].Content
				}, timeout, interval).Should(ContainSubstring("a-non-crd-resource"))

				promise = getPromise(promise.GetName())
				//first dependencies is Name: "a-non-crd-resource" Kind: Namespace
				promise.Spec.Dependencies[0].SetName("super-secret-new-namespace")
				Expect(k8sClient.Update(ctx, promise)).To(Succeed())
			})

			It("updates the Work resource to have only the new workloads", func() {
				Eventually(func() string {
					work := waitForWork(promise.GetName())
					return work.Spec.WorkloadGroups[0].Workloads[0].Content
				}, timeout, interval).Should(ContainSubstring("super-secret-new-namespace"))
				Eventually(func() string {
					work := waitForWork(promise.GetName())
					return work.Spec.WorkloadGroups[0].Workloads[0].Content
				}, timeout, interval).ShouldNot(ContainSubstring("a-non-crd-resource"))
			})

			AfterEach(func() {
				deleteAndWait(promise)
			})
		})

		When("the workflows get updated", func() {
			var (
				promise *v1alpha1.Promise
			)

			BeforeEach(func() {
				promise = parseYAML(RedisPromisePath)
				Expect(k8sClient.Create(ctx, promise)).To(Succeed())

				// send a resource request
				RedisResourceRequest := "../config/samples/redis/redis-resource-request.yaml"
				yamlFile, err := os.ReadFile(RedisResourceRequest)
				Expect(err).ToNot(HaveOccurred())

				requestedResource = &unstructured.Unstructured{}
				Expect(yaml.Unmarshal(yamlFile, requestedResource)).To(Succeed())
				requestedResource.SetNamespace("default")
				Eventually(func() error {
					return k8sClient.Create(ctx, requestedResource)
				}, timeout, interval).Should(Succeed())

				// wait for the pipeline
				Eventually(func() []batchv1.Job {
					return getResourceConfigurePipelineJobs(promise, k8sClient)
				}, timeout, interval).Should(HaveLen(1), "pipeline never started")

				completeAllJobs(k8sClient)

				// update the promise workflow
				promise = getPromise(promise.GetName())

				newContainers := map[string]interface{}{
					"name":  "redis",
					"image": "new-image",
				}

				err = unstructured.SetNestedSlice(promise.Spec.Workflows.Resource.Configure[0].Object, []interface{}{newContainers}, "spec", "containers")
				Expect(err).ToNot(HaveOccurred())
				Expect(k8sClient.Update(ctx, promise)).To(Succeed())
			})

			AfterEach(func() {
				deleteAndWait(promise)
			})

			It("retriggers all pipelines with latest changes", func() {
				// assert the pipeline job has the latest changes
				Eventually(func() int {
					return len(getResourceConfigurePipelineJobs(promise, k8sClient))
				}, timeout, interval).Should(Equal(2), "pipeline never started")

				jobs := getResourceConfigurePipelineJobs(promise, k8sClient)
				jobContainers := []string{jobs[0].Spec.Template.Spec.InitContainers[1].Image, jobs[1].Spec.Template.Spec.InitContainers[1].Image}
				Expect(jobContainers).To(ConsistOf("syntasso/kustomize-redis", "new-image"))
			})

		})
	})

	Describe("Promise with requirements", func() {
		var (
			namespacePromiseCR *v1alpha1.Promise
			namespaceRR        *unstructured.Unstructured
		)
		BeforeEach(func() {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "namespace"}, &v1alpha1.Promise{})
			if err != nil {
				promiseFromYAML := parseYAML(promiseWithRequirements)
				Expect(k8sClient.Create(ctx, promiseFromYAML)).To(Succeed())
				namespacePromiseCR = getPromise(promiseFromYAML.GetName())
			}
		})

		When("the promise requirements are not installed", func() {
			It("updates the status to indicate the dependencies are not installed", func() {
				Eventually(func(g Gomega) {
					promise := getPromise(namespacePromiseCR.GetName())
					g.Expect(promise.Status.Conditions).To(HaveLen(1))
					g.Expect(promise.Status.Conditions[0].Type).To(Equal("RequirementsFulfilled"))
					g.Expect(promise.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))
					g.Expect(promise.Status.Conditions[0].Message).To(Equal("Requirements not fulfilled"))
					g.Expect(promise.Status.Conditions[0].Reason).To(Equal("RequirementsNotInstalled"))
					g.Expect(promise.Status.Conditions[0].LastTransitionTime).ToNot(BeNil())

					g.Expect(promise.Status.Requirements).To(ConsistOf(
						v1alpha1.RequirementStatus{
							Name:    "kafka",
							Version: "v1.2.0",
							State:   "Requirement not installed",
						},
					))

					g.Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusUnavailable))
				}, timeout, interval).Should(Succeed())
			})

			It("ensures requested resources can be created and left in pending state", func() {
				// create the resource request
				namespaceRR = createResourceRequest(resourceRequestForPromiseWithRequirements, "namespace-with-requirements")

				Eventually(func(g Gomega) {
					resourceRequest := &unstructured.Unstructured{}
					// We set the GVK to match the requested resource as this is not set
					// by default on the unstructured object.
					// In the absence of this, the package will return marshalling errors
					// on the client.Get() as the 'kind' cannot be found.
					resourceRequest.SetGroupVersionKind(namespaceRR.GroupVersionKind())
					err := k8sClient.Get(ctx, types.NamespacedName{Name: "namespace-with-requirements", Namespace: "default"}, resourceRequest)
					g.Expect(err).ToNot(HaveOccurred())

					status := resourceRequest.Object["status"]
					g.Expect(status).ToNot(BeNil())
					statusMap := status.(map[string]interface{})
					g.Expect(statusMap["message"]).To(Equal("Pending"))

					conditions, err := conditionsFromStatus(status)
					Expect(err).ToNot(HaveOccurred())
					g.Expect(conditions).To(ContainElement(
						gstruct.MatchFields(
							gstruct.IgnoreExtras,
							gstruct.Fields{
								"Type":               Equal(clusterv1.ConditionType("PromiseAvailable")),
								"Status":             Equal(v1.ConditionFalse),
								"Reason":             Equal("PromiseRequirementsNotInstalled"),
								"Message":            Equal("Promise Requirements are not installed"),
								"LastTransitionTime": Not(BeNil()),
							},
						),
					))
				}, timeout, interval).Should(Succeed())

				// Check that the status of the resource never changes from Pending
				Consistently(func(g Gomega) {
					resourceRequest := &unstructured.Unstructured{}
					resourceRequest.SetGroupVersionKind(namespaceRR.GroupVersionKind())
					err := k8sClient.Get(ctx, types.NamespacedName{Name: "namespace-with-requirements", Namespace: "default"}, resourceRequest)
					g.Expect(err).ToNot(HaveOccurred())

					status := resourceRequest.Object["status"]
					g.Expect(status).ToNot(BeNil())
					statusMap := status.(map[string]interface{})

					g.Expect(statusMap["message"]).To(Equal("Pending"))
				}, "5s", interval).Should(Succeed())

				// Delete the resource request
				Expect(k8sClient.Delete(ctx, namespaceRR)).To(Succeed())

				// Check that the resource request is deleted
				Eventually(func(g Gomega) {
					resourceRequest := &unstructured.Unstructured{}
					resourceRequest.SetGroupVersionKind(namespaceRR.GroupVersionKind())
					err := k8sClient.Get(ctx, types.NamespacedName{Name: "namespace-with-requirements", Namespace: "default"}, resourceRequest)
					g.Expect(err).To(HaveOccurred())
					g.Expect(errors.IsNotFound(err)).To(BeTrue())
				}, timeout, interval).Should(Succeed())
			})

			When("there's a resource request with a status other than Pending", func() {
				BeforeEach(func() {
					requestedResource = createResourceRequest(resourceRequestForPromiseWithRequirements, "namespace-with-requirements")

					resourceRequest := &unstructured.Unstructured{}
					Eventually(func(g Gomega) {
						resourceRequest.SetGroupVersionKind(requestedResource.GroupVersionKind())
						err := k8sClient.Get(ctx, types.NamespacedName{Name: "namespace-with-requirements", Namespace: "default"}, resourceRequest)
						g.Expect(err).ToNot(HaveOccurred())

						status := resourceRequest.Object["status"]
						g.Expect(status).ToNot(BeNil())
						statusMap := status.(map[string]interface{})
						g.Expect(statusMap["message"]).To(Equal("Pending"))
					}, timeout, interval).Should(Succeed())

					resourceRequest.Object["status"] = map[string]interface{}{
						"message": "NotPending",
					}
					Expect(k8sClient.Status().Update(ctx, resourceRequest)).To(Succeed())
				})

				AfterEach(func() {
					Expect(k8sClient.Delete(ctx, requestedResource)).To(Succeed())

					// Check that the resource request is deleted
					Eventually(func(g Gomega) {
						resourceRequest := &unstructured.Unstructured{}
						resourceRequest.SetGroupVersionKind(requestedResource.GroupVersionKind())
						err := k8sClient.Get(ctx, types.NamespacedName{Name: "namespace-with-requirements", Namespace: "default"}, resourceRequest)
						g.Expect(err).To(HaveOccurred())
						g.Expect(errors.IsNotFound(err)).To(BeTrue())
					}, timeout, interval).Should(Succeed())
				})

				It("updates the rr status to indicate the now Pending state", func() {
					Eventually(func(g Gomega) string {
						resourceRequest := &unstructured.Unstructured{}
						resourceRequest.SetGroupVersionKind(requestedResource.GroupVersionKind())
						err := k8sClient.Get(ctx, types.NamespacedName{Name: "namespace-with-requirements", Namespace: "default"}, resourceRequest)
						g.Expect(err).ToNot(HaveOccurred())

						status := resourceRequest.Object["status"]
						g.Expect(status).ToNot(BeNil())
						statusMap := status.(map[string]interface{})
						return statusMap["message"].(string)
					}, timeout, interval).Should(Equal("Pending"))
				})
			})
		})

		When("the promise requirements are not installed at the specified version", func() {
			BeforeEach(func() {
				err := k8sClient.Create(ctx, &v1alpha1.Promise{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kafka",
						Labels: map[string]string{
							"kratix.io/promise-version": "v1.0.0",
						},
					},
				})
				Expect(err).ToNot(HaveOccurred())
			})

			AfterEach(func() {
				deleteTestEnvPromise("kafka")
			})

			It("updates the status to indicate the dependencies are not installed at the specified version", func() {
				Eventually(func(g Gomega) {
					promise := getPromise(namespacePromiseCR.GetName())
					g.Expect(promise.Status.Conditions).To(HaveLen(1))
					g.Expect(promise.Status.Conditions[0].Type).To(Equal("RequirementsFulfilled"))
					g.Expect(promise.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))
					g.Expect(promise.Status.Conditions[0].Message).To(Equal("Requirements not fulfilled"))
					g.Expect(promise.Status.Conditions[0].Reason).To(Equal("RequirementsNotInstalled"))
					g.Expect(promise.Status.Conditions[0].LastTransitionTime).ToNot(BeNil())

					g.Expect(promise.Status.Requirements).To(ConsistOf(
						v1alpha1.RequirementStatus{
							Name:    "kafka",
							Version: "v1.2.0",
							State:   "Requirement not installed at the specified version",
						},
					))

					g.Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusUnavailable))
				}, timeout, interval).Should(Succeed())
			})
		})

		When("the promise requirements are installed at the specified version", func() {
			BeforeEach(func() {
				// send a resource request
				namespaceRR = createResourceRequest(resourceRequestForPromiseWithRequirements, "namespace-with-requirements")

				// satisfy the promise requirements
				err := k8sClient.Create(ctx, &v1alpha1.Promise{
					ObjectMeta: metav1.ObjectMeta{
						Name: "kafka",
						Labels: map[string]string{
							"kratix.io/promise-version": "v1.2.0",
						},
					},
				})
				Expect(err).ToNot(HaveOccurred())

				// wait for the version to make its way to status
				Eventually(func(g Gomega) {
					kafkaPromise := &v1alpha1.Promise{}
					err := k8sClient.Get(ctx, types.NamespacedName{Name: "kafka"}, kafkaPromise)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(kafkaPromise.Status.Version).To(Equal("v1.2.0"))
				})
			})

			AfterEach(func() {
				deleteTestEnvPromise("kafka")
			})

			It("updates the status to indicate the dependencies are installed at the specified version", func() {
				Eventually(func(g Gomega) {
					kafkaPromise := &v1alpha1.Promise{}
					err := k8sClient.Get(ctx, types.NamespacedName{Name: "kafka"}, kafkaPromise)
					g.Expect(err).ToNot(HaveOccurred())

					promise := getPromise(namespacePromiseCR.GetName())
					g.Expect(promise.Status.Conditions).To(HaveLen(1))
					g.Expect(promise.Status.Conditions[0].Type).To(Equal("RequirementsFulfilled"))
					g.Expect(promise.Status.Conditions[0].Status).To(Equal(metav1.ConditionTrue))
					g.Expect(promise.Status.Conditions[0].Message).To(Equal("Requirements fulfilled"))
					g.Expect(promise.Status.Conditions[0].Reason).To(Equal("RequirementsInstalled"))
					g.Expect(promise.Status.Conditions[0].LastTransitionTime).ToNot(BeNil())

					g.Expect(promise.Status.Requirements).To(ConsistOf(
						v1alpha1.RequirementStatus{
							Name:    "kafka",
							Version: "v1.2.0",
							State:   "Requirement installed",
						},
					))

					g.Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusAvailable))
					g.Expect(kafkaPromise.Status.RequiredBy).To(ConsistOf(
						v1alpha1.RequiredBy{
							Promise: v1alpha1.PromiseSummary{
								Name: namespacePromiseCR.Name,
							},
							RequiredVersion: "v1.2.0",
						},
					))
				}, timeout, interval).Should(Succeed())
			})

			It("ensures requested resources can be created and the resources configured", func() {
				Eventually(func(g Gomega) {
					resourceRequest := &unstructured.Unstructured{}
					resourceRequest.SetGroupVersionKind(namespaceRR.GroupVersionKind())
					err := k8sClient.Get(ctx, types.NamespacedName{Name: "namespace-with-requirements", Namespace: "default"}, resourceRequest)
					g.Expect(err).ToNot(HaveOccurred())

					status := resourceRequest.Object["status"]
					g.Expect(status).ToNot(BeNil())

					conditions, err := conditionsFromStatus(status)
					Expect(err).ToNot(HaveOccurred())
					g.Expect(conditions).To(ContainElement(
						gstruct.MatchFields(
							gstruct.IgnoreExtras,
							gstruct.Fields{
								"Type":               Equal(clusterv1.ConditionType("PromiseAvailable")),
								"Status":             Equal(v1.ConditionTrue),
								"Reason":             Equal("PromiseAvailable"),
								"Message":            Equal("Promise Requirements are met"),
								"LastTransitionTime": Not(BeNil()),
							},
						),
					))
				}, timeout, interval).Should(Succeed())

				Eventually(func() int {
					jobs := getResourceConfigurePipelineJobs(namespacePromiseCR, k8sClient)
					return len(jobs)
				}, timeout, interval).Should(Equal(1), "Configure Pipeline never trigerred")

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
	}, timeout, interval).Should(Succeed())
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

func createResourceRequest(path string, name string) *unstructured.Unstructured {
	rr := &unstructured.Unstructured{}
	yamlFile, err := os.ReadFile(path)
	Expect(err).ToNot(HaveOccurred())
	Expect(yaml.Unmarshal(yamlFile, rr)).To(Succeed())
	rr.SetNamespace("default")

	resourceRequest := &unstructured.Unstructured{}
	resourceRequest.SetGroupVersionKind(rr.GroupVersionKind())
	err = k8sClient.Get(context.Background(), types.NamespacedName{Name: rr.GetName(), Namespace: "default"}, resourceRequest)
	if err == nil {
		return resourceRequest
	}

	Eventually(func() error {
		return k8sClient.Create(context.Background(), rr)
	}, timeout, interval).Should(BeNil())

	Eventually(func(g Gomega) {
		resourceRequest := &unstructured.Unstructured{}
		resourceRequest.SetGroupVersionKind(rr.GroupVersionKind())
		err := k8sClient.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, resourceRequest)
		g.Expect(err).ToNot(HaveOccurred())

		status := resourceRequest.Object["status"]
		g.Expect(status).ToNot(BeNil())
	}, timeout, interval).Should(Succeed())
	return rr
}

func deleteTestEnvPromise(name string) {
	ctx := context.Background()
	promise := &v1alpha1.Promise{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, promise)
	Expect(err).ToNot(HaveOccurred())

	err = k8sClient.Delete(ctx, promise)
	Expect(err).ToNot(HaveOccurred())

	Eventually(func(g Gomega) error {
		err = k8sClient.Get(ctx, types.NamespacedName{Name: name}, promise)
		if errors.IsNotFound(err) {
			return nil
		}
		g.Expect(err).ToNot(HaveOccurred())
		promise.Finalizers = []string{}
		err = k8sClient.Update(ctx, promise)
		g.Expect(err).ToNot(HaveOccurred())
		return fmt.Errorf("%s promise still exists", name)
	}, timeout, interval).Should(BeNil())
}
