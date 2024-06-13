package pipeline_test

import (
	"os"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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
				Name:      "custom-namespace-promise-pipeline",
				Namespace: "kratix-platform-system",
				Labels: map[string]string{
					"kratix.io/promise-name": "custom-namespace",
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
					pipelines.DeletePromise[0],
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
							Resources: []string{"promises", "promises/status"},
							Verbs:     []string{"get", "list", "update", "create", "patch"},
						},
						{
							APIGroups: []string{"platform.kratix.io"},
							Resources: []string{"works"},
							Verbs:     []string{"*"},
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
						Name:     "custom-namespace-promise-pipeline",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Namespace: "kratix-platform-system",
							Name:      "custom-namespace-promise-pipeline",
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
					"kratix.io/promise-name":          Equal("custom-namespace"),
					"kratix.io/pipeline-name":         Equal("promise-delete"),
					"kratix.io/work-type":             Equal("promise"),
					"kratix-workflow-pipeline-name":   Equal("promise-delete"),
				})

				Expect(job).To(MatchFields(IgnoreExtras, Fields{
					"ObjectMeta": MatchFields(IgnoreExtras, Fields{
						"Name":      HavePrefix("kratix-custom-namespace-promise-delete-"),
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
								"ServiceAccountName": Equal("custom-namespace-promise-pipeline"),
								"Containers": MatchAllElementsWithIndex(IndexIdentity, Elements{
									"0": MatchFields(IgnoreExtras, Fields{
										"Name":  Equal("demo-custom-namespace-promise-delete-pipeline"),
										"Image": Equal("syntasso/demo-custom-namespace-delete-pipeline:v1.1.0"),
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
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("KRATIX_WORKFLOW_TYPE"),
												"Value": Equal("promise"),
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
												"Value": Equal("custom-namespace"),
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

			Context("the pipeline name would exceed the 63 character limit", func() {
				BeforeEach(func() {
					promise := promiseFromFile(promisePath)
					promise.SetName("long-long-long-long-long-long-promise")
					unstructuredPromise, err := promise.ToUnstructured()
					Expect(err).ToNot(HaveOccurred())

					pipelines, err := promise.GeneratePipelines(logger)
					Expect(err).ToNot(HaveOccurred())

					pipelineResources = pipeline.NewDeletePromise(
						unstructuredPromise,
						pipelines.DeletePromise[0],
					)
				})

				It("concatenates the pipeline name to ensure it fits the 63 character limit", func() {
					job = *pipelineResources[3].(*batchv1.Job)
					Expect(job.ObjectMeta.Name).To(HaveLen(62))
					Expect(job.ObjectMeta.Name).To(HavePrefix("kratix-long-long-long-long-long-long-promise-promise-del-"))
				})
			})
		})
	})

	Describe("Resource", func() {
		var (
			expectedObjectMeta = metav1.ObjectMeta{
				Name:      "custom-namespace-resource-pipeline",
				Namespace: "default",
				Labels: map[string]string{
					"kratix.io/promise-name": "custom-namespace",
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
					pipelines.DeleteResource[0],
					"example-ns",
					"custom-namespace",
					"custom-namespaces",
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
							Resources: []string{"custom-namespaces", "custom-namespaces/status"},
							Verbs:     []string{"get", "list", "update", "create", "patch"},
						},
						{
							APIGroups: []string{"platform.kratix.io"},
							Resources: []string{"works"},
							Verbs:     []string{"*"},
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
						Name:     "custom-namespace-resource-pipeline",
					},
					Subjects: []rbacv1.Subject{
						{
							Kind:      "ServiceAccount",
							Namespace: "default",
							Name:      "custom-namespace-resource-pipeline",
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
					"kratix.io/promise-name":             Equal("custom-namespace"),
					"kratix-promise-resource-request-id": Equal("example-ns"),
					"kratix-workflow-pipeline-name":      Equal("instance-delete"),
					"kratix.io/pipeline-name":            Equal("instance-delete"),
					"kratix.io/work-type":                Equal("resource"),
					"kratix.io/resource-name":            Equal("example"),
				})

				Expect(job).To(MatchFields(IgnoreExtras, Fields{
					"ObjectMeta": MatchFields(IgnoreExtras, Fields{
						"Name":      HavePrefix("kratix-custom-namespace-example-instance-delete-"),
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
								"ServiceAccountName": Equal("custom-namespace-resource-pipeline"),
								"Containers": MatchAllElementsWithIndex(IndexIdentity, Elements{
									"0": MatchFields(IgnoreExtras, Fields{
										"Name":  Equal("demo-custom-namespace-resource-delete-pipeline"),
										"Image": Equal("syntasso/demo-custom-namespace-delete-pipeline:v1.1.0"),
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
											MatchFields(IgnoreExtras, Fields{
												"Name":  Equal("KRATIX_WORKFLOW_TYPE"),
												"Value": Equal("resource"),
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
												"Value": Equal("custom-namespace"),
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

			Context("the pipeline name would exceed the 63 character limit", func() {
				BeforeEach(func() {
					promise := promiseFromFile(promisePath)
					resourceRequest := resourceRequestFromFile(resourceRequestPath)
					resourceRequest.SetName("long-long-request")

					pipelines, err := promise.GeneratePipelines(logger)
					Expect(err).ToNot(HaveOccurred())

					pipelineResources = pipeline.NewDeleteResource(
						resourceRequest,
						pipelines.DeleteResource[0],
						"long-long-request",
						"long-long-promise",
						"long-long-promises",
					)
				})

				It("concatenates the pipeline name to ensure it fits the 63 character limit", func() {
					job = *pipelineResources[3].(*batchv1.Job)
					Expect(job.ObjectMeta.Name).To(HaveLen(62))
					Expect(job.ObjectMeta.Name).To(HavePrefix("kratix-long-long-promise-long-long-request-instance-dele-"))
				})
			})
		})
	})

	Describe("optional workflow configs", func() {
		var (
			rr *unstructured.Unstructured
			p  v1alpha1.Pipeline
		)

		BeforeEach(func() {
			rr = &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "test-pod",
						"namespace": "test-namespace",
					},
					"spec": map[string]interface{}{
						"foo": "bar",
					},
				},
			}

			p = v1alpha1.Pipeline{
				Spec: v1alpha1.PipelineSpec{
					Containers: []v1alpha1.Container{
						{Name: "test-container", Image: "test-image"},
					},
				},
			}
		})

		It("can include args and commands", func() {
			p.Spec.Containers = append(p.Spec.Containers, v1alpha1.Container{
				Name:    "another-container",
				Image:   "another-image",
				Args:    []string{"arg1", "arg2"},
				Command: []string{"command1", "command2"},
			})
			resources := pipeline.NewDelete(rr, p, "", "test-promise", "promises")
			job := resources[3].(*batchv1.Job)

			Expect(job.Spec.Template.Spec.InitContainers[1].Args).To(BeEmpty())
			Expect(job.Spec.Template.Spec.InitContainers[1].Command).To(BeEmpty())
			Expect(job.Spec.Template.Spec.Containers[0].Args).To(Equal([]string{"arg1", "arg2"}))
			Expect(job.Spec.Template.Spec.Containers[0].Command).To(Equal([]string{"command1", "command2"}))
		})

		It("can include env and envFrom", func() {
			p.Spec.Containers = append(p.Spec.Containers, v1alpha1.Container{
				Name:  "another-container",
				Image: "another-image",
				Env: []corev1.EnvVar{
					{Name: "env1", Value: "value1"},
				},
				EnvFrom: []corev1.EnvFromSource{
					{
						ConfigMapRef: &corev1.ConfigMapEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "test-configmap"},
						},
					},
				},
			})
			resources := pipeline.NewDelete(rr, p, "", "test-promise", "promises")
			job := resources[3].(*batchv1.Job)

			Expect(job.Spec.Template.Spec.InitContainers[1].Env).To(ContainElements(
				corev1.EnvVar{Name: "KRATIX_WORKFLOW_ACTION", Value: "delete"},
			))
			Expect(job.Spec.Template.Spec.Containers[0].Env).To(ContainElements(
				corev1.EnvVar{Name: "KRATIX_WORKFLOW_ACTION", Value: "delete"},
				corev1.EnvVar{Name: "env1", Value: "value1"},
			))

			Expect(job.Spec.Template.Spec.InitContainers[1].EnvFrom).To(BeNil())
			Expect(job.Spec.Template.Spec.Containers[0].EnvFrom).To(ContainElements(
				corev1.EnvFromSource{
					ConfigMapRef: &corev1.ConfigMapEnvSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "test-configmap"},
					},
				},
			))
		})

		It("can include volume and volume mounts", func() {
			p.Spec.Volumes = []corev1.Volume{
				{Name: "test-volume", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			}
			p.Spec.Containers = append(p.Spec.Containers, v1alpha1.Container{
				Name:  "another-container",
				Image: "another-image",
				VolumeMounts: []corev1.VolumeMount{
					{Name: "test-volume-mount", MountPath: "/test-mount-path"},
				},
			})
			resources := pipeline.NewDelete(rr, p, "", "test-promise", "promises")
			job := resources[3].(*batchv1.Job)

			Expect(job.Spec.Template.Spec.InitContainers[1].VolumeMounts).To(HaveLen(3), "default volume mounts should've been included")
			Expect(job.Spec.Template.Spec.InitContainers[1].Command).To(BeEmpty())
			Expect(job.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(
				corev1.VolumeMount{Name: "test-volume-mount", MountPath: "/test-mount-path"},
			))
			Expect(job.Spec.Template.Spec.Volumes).To(ContainElement(
				corev1.Volume{Name: "test-volume", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			))
		})

		It("can include imagePullPolicy and imagePullSecrets", func() {
			p.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "test-secret"}, {Name: "another-secret"}}
			p.Spec.Containers = append(p.Spec.Containers, v1alpha1.Container{
				Name:            "another-container",
				Image:           "another-image",
				ImagePullPolicy: corev1.PullAlways,
			})
			resources := pipeline.NewDelete(rr, p, "", "test-promise", "promises")
			job := resources[3].(*batchv1.Job)

			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(HaveLen(2), "imagePullSecrets should've been included")
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(ContainElements(
				corev1.LocalObjectReference{Name: "test-secret"},
				corev1.LocalObjectReference{Name: "another-secret"},
			), "imagePullSecrets should've been included")
			Expect(job.Spec.Template.Spec.InitContainers[1].ImagePullPolicy).To(BeEmpty())
			Expect(job.Spec.Template.Spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullAlways))
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
