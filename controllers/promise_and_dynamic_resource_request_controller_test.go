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

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensions "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
	//+kubebuilder:scaffold:imports
)

var _ = Context("Promise Reconciler", func() {
	var (
		promiseCR           *v1alpha1.Promise
		ctx                 = context.Background()
		promiseCommonLabels map[string]string

		expectedCRDName = "redises.redis.redis.opstreelabs.in"

		requestedResource  *unstructured.Unstructured
		resourceCommonName types.NamespacedName
		yamlContent        = map[string]*v1alpha1.Promise{}
	)

	applyPromise := func(promisePath string) {
		var promiseFromYAML *v1alpha1.Promise
		var found bool
		if promiseFromYAML, found = yamlContent[promisePath]; !found {
			promiseFromYAML = &v1alpha1.Promise{}
			yamlFile, err := os.ReadFile(promisePath)
			Expect(err).ToNot(HaveOccurred())
			Expect(yaml.Unmarshal(yamlFile, promiseFromYAML)).To(Succeed())

			yamlContent[promisePath] = promiseFromYAML
		}

		k8sClient.Create(ctx, promiseFromYAML)

		promiseCR = &v1alpha1.Promise{}
		name := promiseFromYAML.GetName()
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name: name,
			}, promiseCR)
		}, timeout, interval).Should(Succeed())
	}

	Describe("Promise reconciliation lifecycle", func() {
		var (
			promiseGroup        = "redis.redis.opstreelabs.in"
			promiseResourceName = "redises"
		)

		BeforeEach(func() {
			applyPromise("../config/samples/redis/redis-promise.yaml")
			promiseCommonLabels = map[string]string{
				"kratix-promise-id": promiseCR.GetIdentifier(),
			}
		})

		When("the promise is installed", func() {
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
				}, timeout, interval).Should(BeNil(), "Expected controller ClusterRole to exist")

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
				}, timeout, interval).Should(BeNil(), "Expected controller binding to exist")

				Expect(binding.RoleRef.Name).To(Equal(promiseCR.GetControllerResourceName()))
				Expect(binding.Subjects).To(HaveLen(1))
				Expect(binding.Subjects[0]).To(Equal(rbacv1.Subject{
					Kind:      "ServiceAccount",
					Namespace: controllers.KratixSystemNamespace,
					Name:      "kratix-platform-controller-manager",
				}))
				Expect(binding.GetLabels()).To(Equal(promiseCommonLabels))
			})

			It("creates works for the dependencies, on the "+controllers.KratixSystemNamespace+" namespace", func() {
				workNamespacedName := types.NamespacedName{
					Name:      promiseCR.GetIdentifier(),
					Namespace: controllers.KratixSystemNamespace,
				}
				Eventually(func() error {
					return k8sClient.Get(ctx, workNamespacedName, &v1alpha1.Work{})
				}, timeout, interval).Should(BeNil())
			})
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
					Name:      promiseCR.GetIdentifier() + "-promise-pipeline",
					Namespace: "default",
				}

				resourceLabels = map[string]string{
					"kratix-promise-id": promiseCR.GetIdentifier(),
				}
			})

			It("creates a service account for pipeline", func() {
				sa := &v1.ServiceAccount{}
				Eventually(func() error {
					return k8sClient.Get(ctx, resourceCommonName, sa)
				}, timeout, interval).Should(BeNil(), "Expected SA for pipeline to exist")

				Expect(sa.GetLabels()).To(Equal(resourceLabels))
			})

			It("creates a role for the pipeline service account", func() {
				role := &rbacv1.Role{}
				Eventually(func() error {
					return k8sClient.Get(ctx, resourceCommonName, role)
				}, timeout, interval).Should(BeNil(), "Expected Role for pipeline to exist")

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
				}, timeout, interval).Should(BeNil(), "Expected RoleBinding for pipeline to exist")
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
					Name:      "scheduling-" + promiseCR.GetIdentifier(),
					Namespace: "default",
				}
				Eventually(func() error {
					return k8sClient.Get(ctx, configMapName, configMap)
				}, timeout, interval).Should(BeNil(), "Expected ConfigMap for pipeline to exist")
				Expect(configMap.GetLabels()).To(Equal(resourceLabels))
				Expect(configMap.Data).To(HaveKey("scheduling"))
				space := regexp.MustCompile(`\s+`)
				targetScheduling := space.ReplaceAllString(configMap.Data["scheduling"], " ")
				Expect(strings.TrimSpace(targetScheduling)).To(Equal(`- target: matchlabels: environment: dev`))
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
					jobs := &batchv1.JobList{}
					lo := &client.ListOptions{
						LabelSelector: labels.SelectorFromSet(map[string]string{
							"kratix-promise-id":    promiseCR.GetIdentifier(),
							"kratix-pipeline-type": "configure",
						}),
					}

					Expect(k8sClient.List(ctx, jobs, lo)).To(Succeed())
					return len(jobs.Items)
				}, timeout, interval).Should(Equal(1), "Configure Pipeline never trigerred")
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
						Name:      promiseCR.GetIdentifier() + "-" + requestedResource.GetNamespace() + "-" + requestedResource.GetName(),
						Namespace: requestedResource.GetNamespace(),
					}
					Eventually(func() bool {
						return errors.IsNotFound(k8sClient.Get(ctx, workNamespacedName, &v1alpha1.Work{}))
					}, timeout, interval).Should(BeTrue())
				})

				By("leaving the pipeline resources (SA/Role/RoleBinding) intact", func() {
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
						Name:      "scheduling-" + promiseCR.GetIdentifier(),
						Namespace: "default",
					}
					Eventually(func() error {
						return k8sClient.Get(ctx, configMapName, configMap)
					}, timeout, interval).Should(BeNil(), "Expected ConfigMap for pipeline to exist")
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
						Name:      "scheduling-" + promiseCR.GetIdentifier(),
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
							Name:      promiseCR.GetIdentifier(),
							Namespace: controllers.KratixSystemNamespace,
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
					Namespace: controllers.KratixSystemNamespace,
				}
				Eventually(func() error {
					err := k8sClient.Get(ctx, workNamespacedName, &v1alpha1.Work{})
					return err
				}, timeout, interval).Should(BeNil())
			})

			By("setting the correct finalizers", func() {
				promise := &v1alpha1.Promise{}
				err := k8sClient.Get(ctx, expectedPromise, promise)
				Expect(err).NotTo(HaveOccurred())

				Expect(promise.GetFinalizers()).Should(
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
})
