package pipeline_test

import (
	"os"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/gomega/gstruct"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/pipeline"
)

var _ = Describe("Delete Pipeline", func() {
	var (
		logger            = logr.Discard()
		pipelineResources []client.Object
		serviceAccount    v1.ServiceAccount
		role              rbacv1.Role
		roleBinding       rbacv1.RoleBinding
		job               batchv1.Job
	)

	const (
		promisePath         = "assets/promise.yaml"
		resourceRequestPath = "assets/resource-request.yaml"
	)

	Describe("Promise", func() {
		var (
			expectedObjectMeta = metav1.ObjectMeta{
				Name:      "redis-promise-pipeline",
				Namespace: "kratix-platform-system",
				Labels: map[string]string{
					"kratix-promise-id": "redis",
				},
			}
		)

		When("delete pipeline resources are generated", func() {
			BeforeEach(func() {
				promise := promiseFromFile(promisePath)
				unstructuredPromise, err := promise.ToUnstructured()
				Expect(err).ToNot(HaveOccurred())

				pipelines, err := promise.GeneratePipelines(logger)
				Expect(err).ToNot(HaveOccurred())

				pipelineResources = pipeline.NewDeletePromise(
					unstructuredPromise,
					pipelines.DeletePromise,
				)
			})

			It("creates a Job, ServiceAccount, Role, and RoleBinding", func() {
				Expect(pipelineResources).To(HaveLen(4))

				Expect(pipelineResources[0]).To(BeAssignableToTypeOf(&v1.ServiceAccount{}))
				Expect(pipelineResources[1]).To(BeAssignableToTypeOf(&rbacv1.Role{}))
				Expect(pipelineResources[2]).To(BeAssignableToTypeOf(&rbacv1.RoleBinding{}))
				Expect(pipelineResources[3]).To(BeAssignableToTypeOf(&batchv1.Job{}))

				//TODO: move testing of service account, role, and role binding to shared_test.go
			})

			It("creates the ServiceAccount with the right metadata", func() {
				serviceAccount = *pipelineResources[0].(*v1.ServiceAccount)
				expectedServiceAccount := v1.ServiceAccount{
					ObjectMeta: expectedObjectMeta,
				}
				Expect(serviceAccount).To(Equal(expectedServiceAccount))
			})

			It("creates the Role with the right metadata and rules", func() {
				role = *pipelineResources[1].(*rbacv1.Role)
				expectedRole := rbacv1.Role{
					ObjectMeta: expectedObjectMeta,
					Rules: []rbacv1.PolicyRule{
						{
							APIGroups: []string{"platform.kratix.io"},
							Resources: []string{"Promise", "Promise/status"},
							Verbs:     []string{"get", "list", "update", "create", "patch"},
						},
						{
							APIGroups: []string{"platform.kratix.io"},
							Resources: []string{"works"},
							Verbs:     []string{"get", "update", "create", "patch"},
						},
					},
				}
				Expect(role).To(Equal(expectedRole))
			})

			It("creates the RoleBinding with the right metadata, roleRef, and subjects", func() {
				roleBinding = *pipelineResources[2].(*rbacv1.RoleBinding)
				expectedRoleBinding := rbacv1.RoleBinding{
					ObjectMeta: expectedObjectMeta,
					RoleRef: rbacv1.RoleRef{
						Kind:     "Role",
						APIGroup: "rbac.authorization.k8s.io",
						Name:     "redis-promise-pipeline",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Namespace: "kratix-platform-system",
							Name:      "redis-promise-pipeline",
						},
					},
				}
				Expect(roleBinding).To(Equal(expectedRoleBinding))
			})

			It("creates the Job with the right metadata and spec", func() {
				job = *pipelineResources[3].(*batchv1.Job)

				labelsMatcher := MatchAllKeys(Keys{
					"kratix-workflow-kind":            Equal("pipeline.platform.kratix.io"),
					"kratix-workflow-promise-version": Equal("v1alpha1"),
					"kratix-workflow-type":            Equal("promise"),
					"kratix-workflow-action":          Equal("delete"),
					"kratix-promise-id":               Equal("redis"),
				})

				Expect(job).To(MatchFields(IgnoreExtras, Fields{
					"ObjectMeta": MatchFields(IgnoreExtras, Fields{
						"Name":      HavePrefix("delete-pipeline-redis-"),
						"Namespace": Equal("kratix-platform-system"),
						"Labels":    labelsMatcher,
					}),
					"Spec": MatchFields(IgnoreExtras, Fields{
						"Template": MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Labels": labelsMatcher,
							}),
							"Spec": MatchFields(IgnoreExtras, Fields{
								"RestartPolicy":      Equal(v1.RestartPolicyOnFailure),
								"ServiceAccountName": Equal("redis-promise-pipeline"),
								"Containers": MatchAllElementsWithIndex(IndexIdentity, Elements{
									"0": MatchFields(IgnoreExtras, Fields{
										"Name":  Equal("demo-redis-promise-delete-pipeline"),
										"Image": Equal("syntasso/demo-redis-delete-pipeline:v1.1.0"),
										"VolumeMounts": ConsistOf(
											MatchFields(IgnoreExtras, Fields{
												"MountPath": Equal("/kratix/input"),
												"Name":      Equal("shared-input"),
												"ReadOnly":  Equal(true),
											}),
											MatchFields(IgnoreExtras, Fields{
												"MountPath": Equal("/kratix/output"),
												"Name":      Equal("shared-output"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"MountPath": Equal("/kratix/metadata"),
												"Name":      Equal("shared-metadata"),
											}),
										),
										"Env": ConsistOf(
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("KRATIX_WORKFLOW_ACTION"),
												"Value": Equal("delete"),
											}),
										),
									}),
								}),
								"InitContainers": MatchAllElementsWithIndex(IndexIdentity, Elements{
									"0": MatchFields(IgnoreExtras, Fields{
										"Name": Equal("reader"),
										"Env": ConsistOf(
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("OBJECT_KIND"),
												"Value": Equal("promise"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("OBJECT_GROUP"),
												"Value": Equal("platform.kratix.io"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("OBJECT_NAME"),
												"Value": Equal("redis"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("OBJECT_NAMESPACE"),
												"Value": Equal("kratix-platform-system"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("KRATIX_WORKFLOW_TYPE"),
												"Value": Equal("promise"),
											}),
										),
										"VolumeMounts": ConsistOf(
											MatchFields(IgnoreExtras, Fields{
												"MountPath": Equal("/kratix/input"),
												"Name":      Equal("shared-input"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"MountPath": Equal("/kratix/output"),
												"Name":      Equal("shared-output"),
											}),
										),
										"Command": ConsistOf(
											Equal("sh"),
											Equal("-c"),
											Equal("reader"),
										),
									}),
								})},
							),
						}),
					}),
				}))
			})
		})
	})

	Describe("Resource", func() {
		var (
			expectedObjectMeta = metav1.ObjectMeta{
				Name:      "redis-resource-pipeline",
				Namespace: "default",
				Labels: map[string]string{
					"kratix-promise-id": "redis",
				},
			}
		)

		When("delete pipeline resources are generated", func() {
			BeforeEach(func() {
				promise := promiseFromFile(promisePath)
				resourceRequest := resourceRequestFromFile(resourceRequestPath)

				pipelines, err := promise.GeneratePipelines(logger)
				Expect(err).ToNot(HaveOccurred())

				pipelineResources = pipeline.NewDeleteResource(
					resourceRequest,
					pipelines.DeleteResource,
					"example-redis",
					"redis",
				)
			})

			It("creates a Job, ServiceAccount, Role, and RoleBinding", func() {
				Expect(pipelineResources).To(HaveLen(4))

				Expect(pipelineResources[0]).To(BeAssignableToTypeOf(&v1.ServiceAccount{}))
				Expect(pipelineResources[1]).To(BeAssignableToTypeOf(&rbacv1.Role{}))
				Expect(pipelineResources[2]).To(BeAssignableToTypeOf(&rbacv1.RoleBinding{}))
				Expect(pipelineResources[3]).To(BeAssignableToTypeOf(&batchv1.Job{}))

				//TODO: move testing of service account, role, and role binding to shared_test.go
			})

			It("creates the ServiceAccount with the right metadata", func() {
				serviceAccount = *pipelineResources[0].(*v1.ServiceAccount)
				expectedServiceAccount := v1.ServiceAccount{
					ObjectMeta: expectedObjectMeta,
				}
				Expect(serviceAccount).To(Equal(expectedServiceAccount))
			})

			It("creates the Role with the right metadata and rules", func() {
				role = *pipelineResources[1].(*rbacv1.Role)
				expectedRole := rbacv1.Role{
					ObjectMeta: expectedObjectMeta,
					Rules: []rbacv1.PolicyRule{
						{
							APIGroups: []string{"marketplace.kratix.io"},
							Resources: []string{"redis", "redis/status"},
							Verbs:     []string{"get", "list", "update", "create", "patch"},
						},
						{
							APIGroups: []string{"platform.kratix.io"},
							Resources: []string{"works"},
							Verbs:     []string{"get", "update", "create", "patch"},
						},
					},
				}
				Expect(role).To(Equal(expectedRole))
			})

			It("creates the RoleBinding with the right metadata, roleRef, and subjects", func() {
				roleBinding = *pipelineResources[2].(*rbacv1.RoleBinding)
				expectedRoleBinding := rbacv1.RoleBinding{
					ObjectMeta: expectedObjectMeta,
					RoleRef: rbacv1.RoleRef{
						Kind:     "Role",
						APIGroup: "rbac.authorization.k8s.io",
						Name:     "redis-resource-pipeline",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Namespace: "default",
							Name:      "redis-resource-pipeline",
						},
					},
				}
				Expect(roleBinding).To(Equal(expectedRoleBinding))
			})

			It("creates the Job with the right metadata and spec", func() {
				job = *pipelineResources[3].(*batchv1.Job)

				labelsMatcher := MatchAllKeys(Keys{
					"kratix-workflow-kind":               Equal("pipeline.platform.kratix.io"),
					"kratix-workflow-promise-version":    Equal("v1alpha1"),
					"kratix-workflow-type":               Equal("resource"),
					"kratix-workflow-action":             Equal("delete"),
					"kratix-promise-id":                  Equal("redis"),
					"kratix-promise-resource-request-id": Equal("example-redis"),
				})

				Expect(job).To(MatchFields(IgnoreExtras, Fields{
					"ObjectMeta": MatchFields(IgnoreExtras, Fields{
						"Name":      HavePrefix("delete-pipeline-redis-"),
						"Namespace": Equal("default"),
						"Labels":    labelsMatcher,
					}),
					"Spec": MatchFields(IgnoreExtras, Fields{
						"Template": MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Labels": labelsMatcher,
							}),
							"Spec": MatchFields(IgnoreExtras, Fields{
								"RestartPolicy":      Equal(v1.RestartPolicyOnFailure),
								"ServiceAccountName": Equal("redis-resource-pipeline"),
								"Containers": MatchAllElementsWithIndex(IndexIdentity, Elements{
									"0": MatchFields(IgnoreExtras, Fields{
										"Name":  Equal("demo-redis-resource-delete-pipeline"),
										"Image": Equal("syntasso/demo-redis-delete-pipeline:v1.1.0"),
										"VolumeMounts": ConsistOf(
											MatchFields(IgnoreExtras, Fields{
												"MountPath": Equal("/kratix/input"),
												"Name":      Equal("shared-input"),
												"ReadOnly":  Equal(true),
											}),
											MatchFields(IgnoreExtras, Fields{
												"MountPath": Equal("/kratix/output"),
												"Name":      Equal("shared-output"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"MountPath": Equal("/kratix/metadata"),
												"Name":      Equal("shared-metadata"),
											}),
										),
										"Env": ConsistOf(
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("KRATIX_WORKFLOW_ACTION"),
												"Value": Equal("delete"),
											}),
										),
									}),
								}),
								"InitContainers": MatchAllElementsWithIndex(IndexIdentity, Elements{
									"0": MatchFields(IgnoreExtras, Fields{
										"Name": Equal("reader"),
										"Env": ConsistOf(
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("OBJECT_KIND"),
												"Value": Equal("redis"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("OBJECT_GROUP"),
												"Value": Equal("marketplace.kratix.io"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("OBJECT_NAME"),
												"Value": Equal("example"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("OBJECT_NAMESPACE"),
												"Value": Equal("default"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("KRATIX_WORKFLOW_TYPE"),
												"Value": Equal("resource"),
											}),
										),
										"VolumeMounts": ConsistOf(
											MatchFields(IgnoreExtras, Fields{
												"MountPath": Equal("/kratix/input"),
												"Name":      Equal("shared-input"),
											}),
											MatchFields(IgnoreExtras, Fields{
												"MountPath": Equal("/kratix/output"),
												"Name":      Equal("shared-output"),
											}),
										),
										"Command": ConsistOf(
											Equal("sh"),
											Equal("-c"),
											Equal("reader"),
										),
									}),
								})},
							),
						}),
					}),
				}))
			})
		})
	})
})

func promiseFromFile(path string) *v1alpha1.Promise {
	promiseBody, err := os.Open(path)
	Expect(err).ToNot(HaveOccurred())

	decoder := yaml.NewYAMLOrJSONDecoder(promiseBody, 2048)
	promise := &v1alpha1.Promise{}
	err = decoder.Decode(promise)
	Expect(err).ToNot(HaveOccurred())
	promiseBody.Close()

	return promise
}

func resourceRequestFromFile(path string) *unstructured.Unstructured {
	body, err := os.Open(path)
	Expect(err).ToNot(HaveOccurred())

	decoder := yaml.NewYAMLOrJSONDecoder(body, 2048)
	resourceRequest := &unstructured.Unstructured{}
	err = decoder.Decode(resourceRequest)
	Expect(err).ToNot(HaveOccurred())
	body.Close()

	return resourceRequest
}
