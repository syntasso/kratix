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
	)

	const (
		promisePath = "assets/promise.yaml"
	)

	Describe("Promise", func() {
		var (
			serviceAccount     v1.ServiceAccount
			role               rbacv1.Role
			roleBinding        rbacv1.RoleBinding
			job                batchv1.Job
			expectedObjectMeta = metav1.ObjectMeta{
				Name:      "redis-promise-pipeline",
				Namespace: "kratix-platform-system",
				Labels: map[string]string{
					"kratix-promise-id": "redis",
				},
			}
		)

		BeforeEach(func() {
			promise := promiseFromFile(promisePath)
			unstructuredPromise, err := promise.ToUnstructured()
			Expect(err).ToNot(HaveOccurred())

			pipelines, err := promise.GeneratePipelines(logger)
			Expect(err).ToNot(HaveOccurred())

			//TODO Expect that 4 resources are created: Job, ServiceAccount, Role, RoleBinding

			pipelineResources = pipeline.NewDeletePromise(unstructuredPromise, pipelines.DeletePromise)
		})

		It("creates a Job, ServiceAccount, Role, and RoleBinding", func() {
			Expect(pipelineResources).To(HaveLen(4))

			Expect(pipelineResources[0]).To(BeAssignableToTypeOf(&v1.ServiceAccount{}))
			Expect(pipelineResources[1]).To(BeAssignableToTypeOf(&rbacv1.Role{}))
			Expect(pipelineResources[2]).To(BeAssignableToTypeOf(&rbacv1.RoleBinding{}))
			Expect(pipelineResources[3]).To(BeAssignableToTypeOf(&batchv1.Job{}))

			//TODO: move testing of service account, role, and role binding to shared_test.go
			serviceAccount = *pipelineResources[0].(*v1.ServiceAccount)
			role = *pipelineResources[1].(*rbacv1.Role)
			roleBinding = *pipelineResources[2].(*rbacv1.RoleBinding)
			job = *pipelineResources[3].(*batchv1.Job)
		})

		It("creates the ServiceAccount with the right metadata", func() {
			expectedServiceAccount := v1.ServiceAccount{
				ObjectMeta: expectedObjectMeta,
			}
			Expect(serviceAccount).To(Equal(expectedServiceAccount))
		})

		It("creates the Role with the right metadata and rules", func() {
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
							"Containers": ConsistOf(
								MatchFields(IgnoreExtras, Fields{
									"Name":  Equal("demo-redis-delete-pipeline"),
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
							),
							"InitContainers": ConsistOf(
								MatchFields(IgnoreExtras, Fields{
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
							)},
						),
					}),
				}),
			}))
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
