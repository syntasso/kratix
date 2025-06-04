package v1alpha1_test

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/hash"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
)

var _ = Describe("Pipeline", func() {
	var (
		pipeline                     *v1alpha1.Pipeline
		promise                      *v1alpha1.Promise
		promiseCrd                   *apiextensionsv1.CustomResourceDefinition
		resourceRequest              *unstructured.Unstructured
		globalDefaultSecurityContext *corev1.SecurityContext
		defaultKratixSecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			RunAsNonRoot:             ptr.To(true),
			Privileged:               ptr.To(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
			SeccompProfile: &corev1.SeccompProfile{
				Type: "RuntimeDefault",
			},
		}
	)

	BeforeEach(func() {
		secretRef := &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "secretName"}}

		pipeline = &v1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pipelineName",
				Labels: map[string]string{
					"user-provided-label": "label-value",
				},
				Annotations: map[string]string{
					"user-provided-annotation": "annotation-value",
				},
			},
			Spec: v1alpha1.PipelineSpec{
				Containers: []v1alpha1.Container{
					{
						Name:            "container-0",
						Image:           "container-0-image",
						Args:            []string{"arg1", "arg2"},
						Command:         []string{"command1", "command2"},
						Env:             []corev1.EnvVar{{Name: "env1", Value: "value1"}},
						EnvFrom:         []corev1.EnvFromSource{{Prefix: "prefix1", SecretRef: secretRef}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "customVolume", MountPath: "/mount/path"}},
						ImagePullPolicy: "Always",
						SecurityContext: &corev1.SecurityContext{
							Privileged: ptr.To(true),
						},
					},
					{Name: "container-1", Image: "container-1-image"},
				},
				Volumes:          []corev1.Volume{{Name: "customVolume", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				NodeSelector:     map[string]string{"customNodeSelector": "nodeValue"},
				Tolerations:      []corev1.Toleration{{Key: "customToleration", Operator: "Equal", Value: "nodeValue", Effect: "NoSchedule"}},
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: "imagePullSecret"}},
			},
		}

		globalDefaultSecurityContext = &corev1.SecurityContext{
			Privileged: ptr.To(false),
		}
		v1alpha1.DefaultUserProvidedContainersSecurityContext = globalDefaultSecurityContext
		v1alpha1.DefaultImagePullPolicy = ""
		v1alpha1.DefaultJobBackoffLimit = nil
		promiseCrd = &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "promise.crd.group",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Singular: "promiseCrd",
					Plural:   "promiseCrdPlural",
				},
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1"},
				},
			},
		}

		rawCrd, err := json.Marshal(promiseCrd)
		Expect(err).ToNot(HaveOccurred())
		promise = &v1alpha1.Promise{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "fake.promise.group/v1",
				Kind:       "Promise",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "promiseName",
			},
			Spec: v1alpha1.PromiseSpec{
				DestinationSelectors: []v1alpha1.PromiseScheduling{
					{MatchLabels: map[string]string{"label": "value"}},
					{MatchLabels: map[string]string{"another-label": "another-value"}},
				},
				API: &runtime.RawExtension{Raw: rawCrd},
			},
		}

		resourceRequest = &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "promise.crd.group/v1",
				"kind":       "promisecrd",
				"metadata": map[string]interface{}{
					"name": "resourceName",
				},
			},
		}
	})

	Describe("Pipeline Factory Constructors", func() {
		Describe("ForPromise", func() {
			It("sets the appropriate fields", func() {
				f := pipeline.ForPromise(promise, v1alpha1.WorkflowActionConfigure)
				Expect(f).ToNot(BeNil())
				Expect(f.ID).To(Equal(promise.GetName() + "-promise-configure-pipelineName"))
				Expect(f.Promise).To(Equal(promise))
				Expect(f.ResourceRequest).To(BeNil())
				Expect(f.Pipeline).To(Equal(pipeline))
				Expect(f.Namespace).To(Equal(v1alpha1.SystemNamespace))
				Expect(f.WorkflowAction).To(Equal(v1alpha1.WorkflowActionConfigure))
				Expect(f.WorkflowType).To(Equal(v1alpha1.WorkflowTypePromise))
				Expect(f.ClusterScoped).To(BeTrue())
				Expect(f.ResourceWorkflow).To(BeFalse())
				Expect(f.CRDPlural).To(Equal("promises"))
			})
		})

		Describe("ForResource", func() {
			It("sets the appropriate fields", func() {
				f := pipeline.ForResource(promise, v1alpha1.WorkflowActionConfigure, resourceRequest)
				Expect(f).ToNot(BeNil())
				Expect(f.ID).To(Equal(promise.GetName() + "-resource-configure-pipelineName"))
				Expect(f.Promise).To(Equal(promise))
				Expect(f.ResourceRequest).To(Equal(resourceRequest))
				Expect(f.Pipeline).To(Equal(pipeline))
				Expect(f.Namespace).To(Equal(resourceRequest.GetNamespace()))
				Expect(f.WorkflowAction).To(Equal(v1alpha1.WorkflowActionConfigure))
				Expect(f.WorkflowType).To(Equal(v1alpha1.WorkflowTypeResource))
				Expect(f.ClusterScoped).To(BeFalse())
				Expect(f.ResourceWorkflow).To(BeTrue())
				Expect(f.CRDPlural).To(Equal("promiseCrdPlural"))
			})

			It("sets ClusterScoped to true if the promise API is cluster scoped", func() {
				promiseCrd.Spec.Scope = apiextensionsv1.ClusterScoped
				rawCrd, err := json.Marshal(promiseCrd)
				Expect(err).ToNot(HaveOccurred())
				promise.Spec.API = &runtime.RawExtension{Raw: rawCrd}

				f := pipeline.ForResource(promise, v1alpha1.WorkflowActionConfigure, resourceRequest)
				Expect(f.ClusterScoped).To(BeTrue())
			})
		})
	})

	Describe("PipelineFactory", func() {
		var factory *v1alpha1.PipelineFactory

		BeforeEach(func() {
			factory = &v1alpha1.PipelineFactory{
				ID:              "factoryID",
				Namespace:       "factoryNamespace",
				Promise:         promise,
				WorkflowAction:  "fakeAction",
				WorkflowType:    "fakeType",
				ResourceRequest: resourceRequest,
				Pipeline:        pipeline,
			}
		})

		Describe("Resources()", func() {
			When("promise", func() {
				BeforeEach(func() {
					factory.CRDPlural = "promises"
					factory.WorkflowType = "promise"
				})

				When("building for configure action", func() {
					It("returns a list of resources", func() {
						factory.WorkflowAction = v1alpha1.WorkflowActionConfigure
						env := []corev1.EnvVar{{Name: "env1", Value: "value1"}}
						resources, err := factory.Resources(env)
						Expect(err).ToNot(HaveOccurred())
						Expect(resources.Name).To(Equal(pipeline.GetName()))

						roles := resources.Shared.Roles
						bindings := resources.Shared.RoleBindings
						clusterRoles := resources.Shared.ClusterRoles
						clusterRoleBindings := resources.Shared.ClusterRoleBindings
						serviceAccount := resources.Shared.ServiceAccount
						configMap := resources.Shared.ConfigMap
						job := resources.Job

						objs := resources.GetObjects()
						Expect(objs).To(HaveLen(4))
						Expect(objs).To(ContainElements(
							serviceAccount, &clusterRoles[0], &clusterRoleBindings[0], configMap,
						))
						Expect(roles).To(BeEmpty())
						Expect(bindings).To(BeEmpty())

						Expect(serviceAccount.GetName()).To(Equal("factoryID"))
						Expect(resources.Job.Name).To(HavePrefix("kratix-%s-%s", promise.GetName(), pipeline.GetName()))

						job.Name = resources.Job.Name
						Expect(resources.Job).To(Equal(job))

						matchClusterRolesAndBindings(clusterRoles, clusterRoleBindings, factory, serviceAccount)

						Expect(configMap).ToNot(BeNil())
						matchConfigureConfigmap(configMap, factory)

						Expect(resources.WorkflowType).To(Equal(factory.WorkflowType))
						Expect(resources.WorkflowAction).To(Equal(factory.WorkflowAction))
					})
				})

				When("building for delete action", func() {
					It("should return a list of resources", func() {
						factory.WorkflowAction = v1alpha1.WorkflowActionDelete
						env := []corev1.EnvVar{{Name: "env1", Value: "value1"}}
						resources, err := factory.Resources(env)
						Expect(err).ToNot(HaveOccurred())
						Expect(resources.Name).To(Equal(pipeline.GetName()))

						roles := resources.Shared.Roles
						bindings := resources.Shared.RoleBindings
						clusterRoles := resources.Shared.ClusterRoles
						clusterRoleBindings := resources.Shared.ClusterRoleBindings
						serviceAccount := resources.Shared.ServiceAccount
						configMap := resources.Shared.ConfigMap
						job := resources.Job

						objs := resources.GetObjects()
						Expect(objs).To(HaveLen(3))
						Expect(objs).To(ContainElements(
							serviceAccount, &clusterRoles[0], &clusterRoleBindings[0],
						))
						Expect(roles).To(BeEmpty())
						Expect(bindings).To(BeEmpty())

						Expect(resources.Job.Name).To(HavePrefix("kratix-%s-%s", promise.GetName(), pipeline.GetName()))
						job.Name = resources.Job.Name
						Expect(resources.Job).To(Equal(job))

						matchClusterRolesAndBindings(clusterRoles, clusterRoleBindings, factory, serviceAccount)
						Expect(configMap).To(BeNil())

						Expect(resources.WorkflowType).To(Equal(factory.WorkflowType))
						Expect(resources.WorkflowAction).To(Equal(factory.WorkflowAction))
					})
				})
			})

			When("ResourceWorkflow=true", func() {
				BeforeEach(func() {
					factory.ResourceWorkflow = true
					factory.WorkflowType = "resource"
					factory.CRDPlural = "promiseCrdPlural"
				})

				DescribeTable("generates the appropriate resources for action", func(action v1alpha1.Action, expectedNumObjects int, expectedConfigMap bool, expectedClusterRoles bool) {
					factory.WorkflowAction = action
					env := []corev1.EnvVar{{Name: "env1", Value: "value1"}}
					resources, err := factory.Resources(env)
					Expect(err).ToNot(HaveOccurred())
					Expect(resources.Name).To(Equal(pipeline.GetName()))

					roles := resources.Shared.Roles
					bindings := resources.Shared.RoleBindings
					clusterRoles := resources.Shared.ClusterRoles
					clusterRoleBindings := resources.Shared.ClusterRoleBindings
					serviceAccount := resources.Shared.ServiceAccount
					configMap := resources.Shared.ConfigMap
					job := resources.Job

					objs := resources.GetObjects()
					Expect(objs).To(HaveLen(expectedNumObjects))
					Expect(objs).To(ContainElements(
						serviceAccount, &roles[0], &bindings[0],
					))
					if expectedClusterRoles {
						Expect(objs).To(ContainElements(
							&clusterRoles[0], &clusterRoleBindings[0],
						))
						matchClusterRolesAndBindings(clusterRoles, clusterRoleBindings, factory, serviceAccount)
					} else {
						Expect(clusterRoles).To(BeEmpty())
						Expect(clusterRoleBindings).To(BeEmpty())
					}

					Expect(resources.Job.Name).To(HavePrefix("kratix-%s-%s-%s", promise.GetName(), resourceRequest.GetName(), pipeline.GetName()))
					job.Name = resources.Job.Name
					Expect(resources.Job).To(Equal(job))

					matchResourceRolesAndBindings(roles, bindings, factory, serviceAccount, promiseCrd)
					if expectedConfigMap {
						matchConfigureConfigmap(configMap, factory)
					} else {
						Expect(configMap).To(BeNil())
					}

					Expect(resources.WorkflowType).To(Equal(factory.WorkflowType))
					Expect(resources.WorkflowAction).To(Equal(factory.WorkflowAction))
				},
					Entry("Configure", v1alpha1.WorkflowActionConfigure, 4, true, false),
					Entry("Delete", v1alpha1.WorkflowActionDelete, 3, false, false),
				)
			})

		})

		Describe("Job", func() {
			var resources v1alpha1.PipelineJobResources
			BeforeEach(func() {
				var err error
				factory.WorkflowAction = "configure"
				factory.CRDPlural = "promises"
				resources, err = factory.Resources(nil)
				Expect(err).ToNot(HaveOccurred())
			})

			Describe("Job Spec", func() {
				var serviceAccount *corev1.ServiceAccount

				BeforeEach(func() {
					var err error
					serviceAccount = resources.Shared.ServiceAccount
					Expect(err).ToNot(HaveOccurred())
				})

				When("building a job for a promise pipeline", func() {
					When("building a job for the configure action", func() {
						It("returns a job with the appropriate spec", func() {
							job := resources.Job
							Expect(job).ToNot(BeNil())

							Expect(job.GetName()).To(HavePrefix("kratix-%s-%s", promise.GetName(), pipeline.GetName()))
							Expect(job.GetNamespace()).To(Equal(factory.Namespace))
							for _, definedLabels := range []map[string]string{job.GetLabels(), job.Spec.Template.GetLabels()} {
								Expect(definedLabels).To(SatisfyAll(
									HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()),
									HaveKeyWithValue(v1alpha1.WorkflowTypeLabel, string(factory.WorkflowType)),
									HaveKeyWithValue(v1alpha1.WorkflowActionLabel, string(factory.WorkflowAction)),
									HaveKeyWithValue(v1alpha1.PipelineNameLabel, pipeline.GetName()),
									HaveKeyWithValue(v1alpha1.KratixResourceHashLabel, promiseHash(promise)),
									Not(HaveKey(v1alpha1.ResourceNameLabel)),
								))
							}
							podSpec := job.Spec.Template.Spec
							Expect(podSpec.ServiceAccountName).To(Equal(serviceAccount.GetName()))
							Expect(podSpec.ImagePullSecrets).To(ConsistOf(pipeline.Spec.ImagePullSecrets))
							Expect(podSpec.InitContainers).To(HaveLen(4))
							var initContainerNames []string
							var initContainerImages []string
							for _, container := range podSpec.InitContainers {
								initContainerNames = append(initContainerNames, container.Name)
								initContainerImages = append(initContainerImages, container.Image)
							}
							Expect(initContainerNames).To(Equal([]string{
								"reader",
								pipeline.Spec.Containers[0].Name,
								pipeline.Spec.Containers[1].Name,
								"work-writer",
							}))

							Expect(podSpec.InitContainers[0].SecurityContext).To(Equal(defaultKratixSecurityContext))
							Expect(podSpec.InitContainers[len(podSpec.InitContainers)-1].SecurityContext).To(Equal(defaultKratixSecurityContext))
							Expect(podSpec.Containers[0].SecurityContext).To(Equal(defaultKratixSecurityContext))
							Expect(initContainerImages).To(Equal([]string{
								pipelineAdapterImage,
								pipeline.Spec.Containers[0].Image,
								pipeline.Spec.Containers[1].Image,
								pipelineAdapterImage,
							}))
							Expect(podSpec.Containers).To(HaveLen(1))
							Expect(podSpec.Containers[0].Name).To(Equal("status-writer"))
							Expect(podSpec.RestartPolicy).To(Equal(corev1.RestartPolicyOnFailure))
							Expect(podSpec.Volumes).To(HaveLen(5))
							var volumeNames []string
							for _, volume := range podSpec.Volumes {
								volumeNames = append(volumeNames, volume.Name)
							}
							Expect(volumeNames).To(ConsistOf(
								"promise-scheduling",
								"shared-input", "shared-output", "shared-metadata",
								pipeline.Spec.Volumes[0].Name,
							))
							Expect(podSpec.NodeSelector).To(HaveLen(1))
							Expect(podSpec.NodeSelector["customNodeSelector"]).To(Equal("nodeValue"))
							Expect(podSpec.Tolerations).To(HaveLen(1))
							Expect(podSpec.Tolerations[0].Key).To(Equal("customToleration"))
						})
					})

					When("building a job for the delete action", func() {
						BeforeEach(func() {
							factory.WorkflowAction = v1alpha1.WorkflowActionDelete
							var err error
							resources, err = factory.Resources(nil)
							Expect(err).ToNot(HaveOccurred())
						})

						It("returns a job with the appropriate spec", func() {
							job := resources.Job
							Expect(job).ToNot(BeNil())

							podSpec := job.Spec.Template.Spec
							Expect(podSpec.InitContainers).To(HaveLen(2))
							var initContainerNames []string
							for _, container := range podSpec.InitContainers {
								initContainerNames = append(initContainerNames, container.Name)
							}
							Expect(initContainerNames).To(Equal([]string{"reader", pipeline.Spec.Containers[0].Name}))
							Expect(podSpec.Containers).To(HaveLen(1))
							Expect(podSpec.Containers[0].Name).To(Equal(pipeline.Spec.Containers[1].Name))
							Expect(podSpec.Containers[0].Image).To(Equal(pipeline.Spec.Containers[1].Image))
						})
					})
				})

				When("building a job for a resource pipeline", func() {
					When("building a job for the configure action", func() {
						BeforeEach(func() {
							factory.ResourceWorkflow = true
							var err error
							resources, err = factory.Resources(nil)
							Expect(err).ToNot(HaveOccurred())
						})

						It("returns a job with the appropriate spec", func() {
							job := resources.Job
							Expect(job).ToNot(BeNil())

							Expect(job.GetName()).To(HavePrefix("kratix-%s-%s-%s", promise.GetName(), resourceRequest.GetName(), pipeline.GetName()))
							podTemplate := job.Spec.Template
							Expect(job.GetNamespace()).To(Equal(factory.Namespace))
							for _, definedLabels := range []map[string]string{job.GetLabels(), podTemplate.GetLabels()} {
								Expect(definedLabels).To(SatisfyAll(
									HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()),
									HaveKeyWithValue(v1alpha1.WorkflowTypeLabel, string(factory.WorkflowType)),
									HaveKeyWithValue(v1alpha1.WorkflowActionLabel, string(factory.WorkflowAction)),
									HaveKeyWithValue(v1alpha1.PipelineNameLabel, pipeline.GetName()),
									HaveKeyWithValue(v1alpha1.KratixResourceHashLabel, combinedHash(promiseHash(promise), resourceHash(resourceRequest))),
									HaveKeyWithValue(v1alpha1.ResourceNameLabel, resourceRequest.GetName()),
								))
							}

							By("injecting the pipeline labels and annotations into the templated pod", func() {
								for key, val := range pipeline.GetLabels() {
									Expect(podTemplate.GetLabels()).To(HaveKeyWithValue(key, val))
								}
								for key, val := range pipeline.GetAnnotations() {
									Expect(podTemplate.GetAnnotations()).To(HaveKeyWithValue(key, val))
								}
							})

							podSpec := podTemplate.Spec
							Expect(podSpec.ServiceAccountName).To(Equal(serviceAccount.GetName()))
							Expect(podSpec.ImagePullSecrets).To(ConsistOf(pipeline.Spec.ImagePullSecrets))
							Expect(podSpec.InitContainers).To(HaveLen(4))
							var initContainerNames []string
							var initContainerImages []string
							for _, container := range podSpec.InitContainers {
								initContainerNames = append(initContainerNames, container.Name)
								initContainerImages = append(initContainerImages, container.Image)
							}
							Expect(initContainerNames).To(Equal([]string{
								"reader",
								pipeline.Spec.Containers[0].Name,
								pipeline.Spec.Containers[1].Name,
								"work-writer",
							}))
							Expect(initContainerImages).To(Equal([]string{
								pipelineAdapterImage,
								pipeline.Spec.Containers[0].Image,
								pipeline.Spec.Containers[1].Image,
								pipelineAdapterImage,
							}))
							Expect(podSpec.Containers).To(HaveLen(1))
							Expect(podSpec.Containers[0].Name).To(Equal("status-writer"))
							Expect(podSpec.RestartPolicy).To(Equal(corev1.RestartPolicyOnFailure))
							Expect(podSpec.Volumes).To(HaveLen(5))
							var volumeNames []string
							for _, volume := range podSpec.Volumes {
								volumeNames = append(volumeNames, volume.Name)
							}
							Expect(volumeNames).To(ConsistOf(
								"promise-scheduling",
								"shared-input", "shared-output", "shared-metadata",
								pipeline.Spec.Volumes[0].Name,
							))
							Expect(podSpec.NodeSelector).To(HaveLen(1))
							Expect(podSpec.NodeSelector["customNodeSelector"]).To(Equal("nodeValue"))
							Expect(podSpec.Tolerations).To(HaveLen(1))
							Expect(podSpec.Tolerations[0].Key).To(Equal("customToleration"))
						})
					})

					When("building a job for the delete action", func() {
						BeforeEach(func() {
							factory.WorkflowAction = v1alpha1.WorkflowActionDelete
							var err error
							resources, err = factory.Resources(nil)
							Expect(err).ToNot(HaveOccurred())
						})

						It("returns a job with the appropriate spec", func() {
							job := resources.Job
							Expect(job).ToNot(BeNil())

							podTemplate := job.Spec.Template
							By("injecting the pipeline labels and annotations into the templated pod", func() {
								for key, val := range pipeline.GetLabels() {
									Expect(podTemplate.GetLabels()).To(HaveKeyWithValue(key, val))
								}
								for key, val := range pipeline.GetAnnotations() {
									Expect(podTemplate.GetAnnotations()).To(HaveKeyWithValue(key, val))
								}
							})

							podSpec := podTemplate.Spec
							Expect(podSpec.InitContainers).To(HaveLen(2))
							var initContainerNames []string
							for _, container := range podSpec.InitContainers {
								initContainerNames = append(initContainerNames, container.Name)
							}
							Expect(initContainerNames).To(Equal([]string{"reader", pipeline.Spec.Containers[0].Name}))
							Expect(podSpec.Containers).To(HaveLen(1))
							Expect(podSpec.Containers[0].Name).To(Equal(pipeline.Spec.Containers[1].Name))
							Expect(podSpec.Containers[0].Image).To(Equal(pipeline.Spec.Containers[1].Image))
						})
					})
				})
			})

			Describe("BackoffLimit", func() {
				When("a global default backoff limit is set", func() {
					BeforeEach(func() {
						val := int32(4)
						v1alpha1.DefaultJobBackoffLimit = &val
					})

					It("sets the job backoff limit to the global default", func() {
						resources, err := factory.Resources(nil)
						Expect(err).ToNot(HaveOccurred())
						Expect(resources.Job.Spec.BackoffLimit).ToNot(BeNil())
						Expect(*resources.Job.Spec.BackoffLimit).To(Equal(int32(4)))
					})
				})

				When("the pipeline specifies a backoff limit", func() {
					BeforeEach(func() {
						val := int32(2)
						pipeline.Spec.JobOptions.BackoffLimit = &val
					})

					It("uses the pipeline value", func() {
						resources, err := factory.Resources(nil)
						Expect(err).ToNot(HaveOccurred())
						Expect(resources.Job.Spec.BackoffLimit).ToNot(BeNil())
						Expect(*resources.Job.Spec.BackoffLimit).To(Equal(int32(2)))
					})
				})

				When("both global and pipeline backoff limits are set", func() {
					BeforeEach(func() {
						global := int32(5)
						v1alpha1.DefaultJobBackoffLimit = &global
						pipelineVal := int32(1)
						pipeline.Spec.JobOptions.BackoffLimit = &pipelineVal
					})

					It("gives precedence to the pipeline value", func() {
						resources, err := factory.Resources(nil)
						Expect(err).ToNot(HaveOccurred())
						Expect(resources.Job.Spec.BackoffLimit).ToNot(BeNil())
						Expect(*resources.Job.Spec.BackoffLimit).To(Equal(int32(1)))
					})
				})

				When("no backoff limit is specified", func() {
					It("leaves the job backoff limit unset", func() {
						resources, err := factory.Resources(nil)
						Expect(err).ToNot(HaveOccurred())
						Expect(resources.Job.Spec.BackoffLimit).To(BeNil())
					})
				})
			})

			Describe("Default Volumes", func() {
				It("returns a list of volumes that contains default volumes", func() {
					volumes := resources.Job.Spec.Template.Spec.Volumes
					volumeMounts := resources.Job.Spec.Template.Spec.InitContainers[1].VolumeMounts
					Expect(volumes).To(HaveLen(5))
					Expect(volumeMounts).To(HaveLen(4))
					emptyDir := corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
					Expect(volumes).To(ContainElements(
						corev1.Volume{Name: "shared-input", VolumeSource: emptyDir},
						corev1.Volume{Name: "shared-output", VolumeSource: emptyDir},
						corev1.Volume{Name: "shared-metadata", VolumeSource: emptyDir},
						corev1.Volume{
							Name: "promise-scheduling",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: resources.Shared.ConfigMap.GetName()},
									Items: []corev1.KeyToPath{
										{Key: "destinationSelectors", Path: "promise-scheduling"},
									},
								},
							},
						},
					))
					Expect(volumeMounts).To(ContainElements(
						corev1.VolumeMount{Name: "shared-input", MountPath: "/kratix/input", ReadOnly: true},
						corev1.VolumeMount{Name: "shared-output", MountPath: "/kratix/output"},
						corev1.VolumeMount{Name: "shared-metadata", MountPath: "/kratix/metadata"},
					))
				})
			})

			Describe("DefaultEnvVars", func() {
				It("should return a list of default environment variables", func() {
					envVars := resources.Job.Spec.Template.Spec.InitContainers[1].Env
					Expect(envVars).To(HaveLen(12))
					Expect(envVars).To(ContainElements(
						corev1.EnvVar{Name: "KRATIX_WORKFLOW_ACTION", Value: "configure"},
						corev1.EnvVar{Name: "KRATIX_WORKFLOW_TYPE", Value: "fakeType"},
						corev1.EnvVar{Name: "KRATIX_PROMISE_NAME", Value: promise.GetName()},
						corev1.EnvVar{Name: "KRATIX_PIPELINE_NAME", Value: "pipelineName"},
					))

					// TODO: Expected because Promise is default, should test for RR
					Expect(envVars).To(ContainElements(
						corev1.EnvVar{Name: "KRATIX_OBJECT_GROUP", Value: promise.GroupVersionKind().Group},
						corev1.EnvVar{Name: "KRATIX_OBJECT_NAME", Value: promise.GetName()},
						corev1.EnvVar{Name: "KRATIX_OBJECT_VERSION", Value: promise.GroupVersionKind().Version},
						corev1.EnvVar{Name: "KRATIX_OBJECT_NAMESPACE", Value: ""},
						corev1.EnvVar{Name: "KRATIX_OBJECT_KIND", Value: "Promise"},
						corev1.EnvVar{Name: "KRATIX_CRD_PLURAL", Value: "promises"},
					))

					var clusterScoped corev1.EnvVar
					for _, envVar := range envVars {
						if envVar.Name == "KRATIX_CLUSTER_SCOPED" {
							clusterScoped = envVar
						}
					}
					Expect(clusterScoped.Value).ToNot(BeEmpty())
				})
			})

			DescribeTable("ReaderContainer", func(isResourceWorkflow bool, act v1alpha1.Action, additionalEnvVars []corev1.EnvVar) {
				factory.ResourceWorkflow = isResourceWorkflow
				if isResourceWorkflow {
					factory.ClusterScoped = false
					factory.CRDPlural = "promiseCrdPlural"
				} else {
					factory.ClusterScoped = true
					factory.CRDPlural = "promises"
				}
				factory.WorkflowAction = act

				var err error
				resources, err = factory.Resources(nil)
				Expect(err).ToNot(HaveOccurred())

				container := resources.Job.Spec.Template.Spec.InitContainers[0]
				Expect(container).ToNot(BeNil())
				Expect(container.Name).To(Equal("reader"))
				Expect(container.Command).To(Equal([]string{"sh", "-c", "reader"}))
				Expect(container.Image).To(Equal(pipelineAdapterImage))
				Expect(container.ImagePullPolicy).To(BeEmpty())
				Expect(container.VolumeMounts).To(ConsistOf([]corev1.VolumeMount{
					{Name: "shared-input", MountPath: "/kratix/input"},
					{Name: "shared-output", MountPath: "/kratix/output"},
				}))

				expectedEnvVars := []corev1.EnvVar{
					{Name: "KRATIX_WORKFLOW_TYPE", Value: string(factory.WorkflowType)},
					{Name: "KRATIX_CLUSTER_SCOPED", Value: fmt.Sprintf("%t", factory.ClusterScoped)},
					{Name: "KRATIX_CRD_PLURAL", Value: factory.CRDPlural},
					{Name: "KRATIX_WORKFLOW_ACTION", Value: "configure"},
					{Name: "KRATIX_PROMISE_NAME", Value: "promiseName"},
					{Name: "KRATIX_PIPELINE_NAME", Value: "pipelineName"},
					{Name: "KRATIX_WORKFLOW_TYPE", Value: "fakeType"},
				}

				if isResourceWorkflow {
					expectedEnvVars = append(expectedEnvVars,
						corev1.EnvVar{Name: "KRATIX_OBJECT_GROUP", Value: resourceRequest.GroupVersionKind().Group},
						corev1.EnvVar{Name: "KRATIX_OBJECT_NAME", Value: resourceRequest.GetName()},
						corev1.EnvVar{Name: "KRATIX_OBJECT_VERSION", Value: resourceRequest.GroupVersionKind().Version},
						corev1.EnvVar{Name: "KRATIX_OBJECT_NAMESPACE", Value: resourceRequest.GetNamespace()},
						corev1.EnvVar{Name: "KRATIX_OBJECT_KIND", Value: "promisecrd"},
						corev1.EnvVar{Name: "KRATIX_CRD_PLURAL", Value: "promiseCrdPlural"},
						corev1.EnvVar{Name: "KRATIX_CLUSTER_SCOPED", Value: "false"},
					)
				} else {
					expectedEnvVars = append(expectedEnvVars,
						corev1.EnvVar{Name: "KRATIX_OBJECT_GROUP", Value: promise.GroupVersionKind().Group},
						corev1.EnvVar{Name: "KRATIX_OBJECT_NAME", Value: promise.GetName()},
						corev1.EnvVar{Name: "KRATIX_OBJECT_VERSION", Value: promise.GroupVersionKind().Version},
						corev1.EnvVar{Name: "KRATIX_OBJECT_NAMESPACE", Value: ""},
						corev1.EnvVar{Name: "KRATIX_OBJECT_KIND", Value: promise.GroupVersionKind().Kind},
						corev1.EnvVar{Name: "KRATIX_CRD_PLURAL", Value: "promises"},
						corev1.EnvVar{Name: "KRATIX_CLUSTER_SCOPED", Value: "true"},
					)
				}

				Expect(container.Env).To(ConsistOf(append(expectedEnvVars, additionalEnvVars...)))
			},
				Entry("resource configure", true, v1alpha1.WorkflowActionConfigure, nil),
				Entry("promise configure", false, v1alpha1.WorkflowActionConfigure, nil),
			)

			Context("ReaderContainer image pull policy", func() {
				When("default image pull policy is set ", func() {
					BeforeEach(func() {
						v1alpha1.DefaultImagePullPolicy = "Always"
					})

					It("set pull policy as the default", func() {
						var err error
						factory.WorkflowAction = "configure"
						resources, err = factory.Resources(nil)
						Expect(err).ToNot(HaveOccurred())

						container := resources.Job.Spec.Template.Spec.InitContainers[0]
						Expect(string(container.ImagePullPolicy)).To(Equal("Always"))
					})
				})
			})

			Describe("WorkCreatorContainer", func() {
				When("building the work creator container for a promise pipeline", func() {
					It("returns a the work creator container with the appropriate command", func() {
						expectedFlags := strings.Join([]string{
							"-input-directory", "/work-creator-files",
							"-promise-name", promise.GetName(),
							"-pipeline-name", pipeline.GetName(),
							"-namespace", factory.Namespace,
							"-workflow-type", string(factory.WorkflowType),
						}, " ")
						containers := resources.Job.Spec.Template.Spec.InitContainers
						container := containers[len(containers)-1]
						Expect(container).ToNot(BeNil())
						Expect(container.Name).To(Equal("work-writer"))
						Expect(container.Image).To(Equal(pipelineAdapterImage))
						Expect(container.ImagePullPolicy).To(BeEmpty())
						Expect(container.Command).To(Equal([]string{"sh", "-c", "work-creator " + expectedFlags}))
						Expect(container.VolumeMounts).To(ConsistOf(
							corev1.VolumeMount{Name: "shared-output", MountPath: "/work-creator-files/input"},
							corev1.VolumeMount{Name: "shared-metadata", MountPath: "/work-creator-files/metadata"},
							corev1.VolumeMount{Name: "promise-scheduling", MountPath: "/work-creator-files/kratix-system"},
						))
					})

					When("default image pull policy is set ", func() {
						BeforeEach(func() {
							v1alpha1.DefaultImagePullPolicy = "Always"
						})

						It("set pull policy as the default", func() {
							var err error
							factory.ResourceWorkflow = false
							factory.WorkflowAction = "configure"
							resources, err = factory.Resources(nil)
							Expect(err).ToNot(HaveOccurred())

							containers := resources.Job.Spec.Template.Spec.InitContainers
							container := containers[len(containers)-1]
							Expect(string(container.ImagePullPolicy)).To(Equal("Always"))
						})
					})
				})
				When("building the work creator container for a resource pipeline", func() {
					It("returns a the work creator container with the appropriate command", func() {
						factory.ResourceWorkflow = true
						var err error
						resources, err = factory.Resources(nil)
						Expect(err).ToNot(HaveOccurred())

						expectedFlags := strings.Join([]string{
							"-input-directory", "/work-creator-files",
							"-promise-name", promise.GetName(),
							"-pipeline-name", pipeline.GetName(),
							"-namespace", factory.Namespace,
							"-workflow-type", string(factory.WorkflowType),
							"-resource-name", resourceRequest.GetName(),
						}, " ")
						containers := resources.Job.Spec.Template.Spec.InitContainers
						container := containers[len(containers)-1]

						Expect(container).ToNot(BeNil())
						Expect(container.Name).To(Equal("work-writer"))
						Expect(container.Image).To(Equal(pipelineAdapterImage))
						Expect(container.ImagePullPolicy).To(BeEmpty())
						Expect(container.Command).To(Equal([]string{"sh", "-c", "work-creator " + expectedFlags}))
						Expect(container.VolumeMounts).To(ConsistOf(
							corev1.VolumeMount{Name: "shared-output", MountPath: "/work-creator-files/input"},
							corev1.VolumeMount{Name: "shared-metadata", MountPath: "/work-creator-files/metadata"},
							corev1.VolumeMount{Name: "promise-scheduling", MountPath: "/work-creator-files/kratix-system"},
						))
					})

					When("default image pull policy is set ", func() {
						BeforeEach(func() {
							v1alpha1.DefaultImagePullPolicy = "Never"
						})

						It("set pull policy as the default", func() {
							factory.ResourceWorkflow = true
							var err error
							resources, err = factory.Resources(nil)
							Expect(err).ToNot(HaveOccurred())

							containers := resources.Job.Spec.Template.Spec.InitContainers
							container := containers[len(containers)-1]
							Expect(string(container.ImagePullPolicy)).To(Equal("Never"))
						})
					})
				})
			})

			Describe("PipelineContainers", func() {
				It("returns the pipeline containers and volumes", func() {
					containers := resources.Job.Spec.Template.Spec.InitContainers
					volumes := resources.Job.Spec.Template.Spec.Volumes
					Expect(containers).To(HaveLen(4))
					Expect(volumes).To(HaveLen(5))

					expectedContainer0 := pipeline.Spec.Containers[0]
					Expect(containers[1]).To(MatchFields(IgnoreExtras, Fields{
						"Name":            Equal(expectedContainer0.Name),
						"Image":           Equal(expectedContainer0.Image),
						"Args":            Equal(expectedContainer0.Args),
						"Command":         Equal(expectedContainer0.Command),
						"Env":             ContainElements(expectedContainer0.Env),
						"EnvFrom":         Equal(expectedContainer0.EnvFrom),
						"VolumeMounts":    ContainElements(expectedContainer0.VolumeMounts),
						"ImagePullPolicy": Equal(expectedContainer0.ImagePullPolicy),
						"SecurityContext": Equal(expectedContainer0.SecurityContext),
					}))

					expectedContainer1 := pipeline.Spec.Containers[1]
					Expect(containers[2]).To(MatchFields(IgnoreExtras, Fields{
						"Name":            Equal(expectedContainer1.Name),
						"Image":           Equal(expectedContainer1.Image),
						"Args":            BeNil(),
						"Command":         BeNil(),
						"EnvFrom":         BeNil(),
						"ImagePullPolicy": BeEmpty(),
						"SecurityContext": Equal(globalDefaultSecurityContext),
					}))
				})

				When("neither a global or container specific security context is provided", func() {
					BeforeEach(func() {
						v1alpha1.DefaultUserProvidedContainersSecurityContext = nil
						pipeline.Spec.Containers[0].SecurityContext = nil
					})

					It("should not set a security context", func() {
						resources, err := factory.Resources(nil)
						Expect(err).ToNot(HaveOccurred())
						containers := resources.Job.Spec.Template.Spec.InitContainers
						Expect(containers[1].SecurityContext).To(BeNil())
					})
				})

				When("default image pull policy is set ", func() {
					When("pipeline container has no image pull policy", func() {
						BeforeEach(func() {
							v1alpha1.DefaultImagePullPolicy = "Never"
							pipeline.Spec.Containers[0].ImagePullPolicy = ""
						})

						It("set pull policy as the default", func() {
							resources, err := factory.Resources(nil)
							Expect(err).ToNot(HaveOccurred())
							containers := resources.Job.Spec.Template.Spec.InitContainers
							Expect(string(containers[1].ImagePullPolicy)).To(Equal("Never"))
						})
					})

					When("pipeline container has image pull policy", func() {
						BeforeEach(func() {
							v1alpha1.DefaultImagePullPolicy = "Never"
							pipeline.Spec.Containers[0].ImagePullPolicy = "Always"
						})

						It("takes precedence over the default", func() {
							resources, err := factory.Resources(nil)
							Expect(err).ToNot(HaveOccurred())
							containers := resources.Job.Spec.Template.Spec.InitContainers
							Expect(string(containers[1].ImagePullPolicy)).To(Equal("Always"))
						})
					})

				})
			})

			Describe("StatusWriterContainer", func() {
				BeforeEach(func() {
					factory.ResourceWorkflow = true
					factory.CRDPlural = "promiseCrdPlural"
					var err error
					resources, err = factory.Resources([]corev1.EnvVar{
						{Name: "env1", Value: "value1"},
						{Name: "env2", Value: "value2"},
					})
					Expect(err).ToNot(HaveOccurred())
				})

				It("returns the appropriate container", func() {
					container := resources.Job.Spec.Template.Spec.Containers[0]
					Expect(container).ToNot(BeNil())
					Expect(container.Name).To(Equal("status-writer"))
					Expect(container.Image).To(Equal(pipelineAdapterImage))
					Expect(container.ImagePullPolicy).To(BeEmpty())
					Expect(container.Command).To(Equal([]string{"sh", "-c", "update-status"}))
					Expect(container.Env).To(ConsistOf(
						corev1.EnvVar{Name: "KRATIX_OBJECT_KIND", Value: resourceRequest.GroupVersionKind().Kind},
						corev1.EnvVar{Name: "KRATIX_OBJECT_GROUP", Value: resourceRequest.GroupVersionKind().Group},
						corev1.EnvVar{Name: "KRATIX_OBJECT_VERSION", Value: resourceRequest.GroupVersionKind().Version},
						corev1.EnvVar{Name: "KRATIX_CRD_PLURAL", Value: promiseCrd.Spec.Names.Plural},
						corev1.EnvVar{Name: "KRATIX_OBJECT_NAME", Value: resourceRequest.GetName()},
						corev1.EnvVar{Name: "KRATIX_OBJECT_NAMESPACE", Value: factory.Namespace},
						corev1.EnvVar{Name: "KRATIX_CLUSTER_SCOPED", Value: "false"},
						corev1.EnvVar{Name: "env1", Value: "value1"},
						corev1.EnvVar{Name: "env2", Value: "value2"},
					))
					Expect(container.VolumeMounts).To(ConsistOf(
						corev1.VolumeMount{Name: "shared-metadata", MountPath: "/work-creator-files/metadata"},
					))
				})

				When("default image pull policy is set ", func() {
					BeforeEach(func() {
						v1alpha1.DefaultImagePullPolicy = "Never"
					})

					It("set pull policy as the default", func() {
						resources, err := factory.Resources(nil)
						Expect(err).ToNot(HaveOccurred())
						statusContainer := resources.Job.Spec.Template.Spec.Containers[0]
						Expect(string(statusContainer.ImagePullPolicy)).To(Equal("Never"))
					})
				})
			})
		})

		When("a service account name is provided", func() {
			It("should create a service account with the provided name", func() {
				factory.Pipeline.Spec.RBAC = v1alpha1.RBAC{
					ServiceAccount: "someServiceAccount",
				}

				resources, err := factory.Resources(nil)
				Expect(err).ToNot(HaveOccurred())
				sa := resources.Shared.ServiceAccount
				Expect(sa).ToNot(BeNil())
				Expect(sa.GetName()).To(Equal("someServiceAccount"))
				Expect(sa.GetNamespace()).To(Equal(factory.Namespace))
				Expect(sa.GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()))
			})
		})

		When("workflow type is neither 'promise' or 'resource'", func() {
			It("should create scheduling configmap with a suffix", func() {
				factory.WorkflowAction = v1alpha1.WorkflowActionConfigure
				factory.WorkflowType = "new-type"
				resources, err := factory.Resources(nil)
				Expect(err).ToNot(HaveOccurred())
				cm := resources.Shared.ConfigMap
				Expect(cm).ToNot(BeNil())
				Expect(cm.GetName()).To(Equal("destination-selectors-promiseName-new-type-773b6"))
			})
		})

		When("user provided permissions within the Pipeline namespace", func() {
			When("promise workflow", func() {
				It("should create the user-provided permission role and role binding", func() {
					factory.Pipeline.Spec.RBAC.Permissions = []v1alpha1.Permission{{PolicyRule: createWatchDeployment()}}

					resources, err := factory.Resources(nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(resources.Name).To(Equal(pipeline.GetName()))

					Expect(resources.Shared.Roles).To(HaveLen(1))
					Expect(resources.Shared.Roles[0].GetName()).To(ContainSubstring(factory.ID))
					Expect(resources.Shared.Roles[0].GetNamespace()).To(Equal("factoryNamespace"))
					Expect(resources.Shared.Roles[0].Rules).To(ConsistOf(rbacv1.PolicyRule{
						Verbs:         []string{"watch", "create"},
						APIGroups:     []string{"", "apps"},
						Resources:     []string{"deployments", "deployments/status"},
						ResourceNames: []string{"a-deployment", "b-deployment"},
					}))
					matchUserPermissionsLabels(&resources, resources.Shared.Roles[0].GetLabels())

					Expect(resources.Shared.RoleBindings).To(HaveLen(1))
					Expect(resources.Shared.RoleBindings[0].GetName()).To(ContainSubstring(factory.ID))
					Expect(resources.Shared.RoleBindings[0].GetNamespace()).To(Equal("factoryNamespace"))
					Expect(resources.Shared.RoleBindings[0].RoleRef.Name).To(ContainSubstring(factory.ID))
					Expect(resources.Shared.RoleBindings[0].RoleRef.Kind).To(Equal("Role"))
					Expect(resources.Shared.RoleBindings[0].RoleRef.APIGroup).To(Equal("rbac.authorization.k8s.io"))
					Expect(resources.Shared.RoleBindings[0].Subjects).To(ConsistOf(rbacv1.Subject{
						Kind:      rbacv1.ServiceAccountKind,
						Namespace: resources.Shared.ServiceAccount.GetNamespace(),
						Name:      resources.Shared.ServiceAccount.GetName(),
					}))
					matchUserPermissionsLabels(&resources, resources.Shared.RoleBindings[0].GetLabels())
				})
			})

			When("resource workflow", func() {
				It("should create the user-provided permission role/binding and the default role/binding", func() {
					factory.ResourceWorkflow = true
					factory.Pipeline.Spec.RBAC.Permissions = []v1alpha1.Permission{
						{
							PolicyRule: createWatchDeployment(),
						},
					}

					resources, err := factory.Resources(nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(resources.Shared.ClusterRoles).To(BeEmpty())
					Expect(resources.Shared.ClusterRoleBindings).To(BeEmpty())
					Expect(resources.Shared.Roles).To(ConsistOf(
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name":      Equal(factory.ID),
								"Namespace": Equal(factory.Namespace),
							}),
						}),
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name":      MatchRegexp(fmt.Sprintf(`^%s-\b\w{5}\b$`, factory.ID)),
								"Namespace": Equal(factory.Namespace),
							}),
							"Rules": ConsistOf(rbacv1.PolicyRule{
								Verbs:         []string{"watch", "create"},
								APIGroups:     []string{"", "apps"},
								Resources:     []string{"deployments", "deployments/status"},
								ResourceNames: []string{"a-deployment", "b-deployment"},
							}),
						}),
					))

					Expect(resources.Shared.RoleBindings).To(ConsistOf(
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name":      Equal(factory.ID),
								"Namespace": Equal(factory.Namespace),
							}),
						}),
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name":      MatchRegexp(fmt.Sprintf(`^%s-\b\w{5}\b$`, factory.ID)),
								"Namespace": Equal(factory.Namespace),
							}),
							"RoleRef": MatchFields(IgnoreExtras, Fields{
								"Name":     MatchRegexp(fmt.Sprintf(`^%s-\b\w{5}\b$`, factory.ID)),
								"Kind":     Equal("Role"),
								"APIGroup": Equal("rbac.authorization.k8s.io"),
							}),
							"Subjects": ConsistOf(rbacv1.Subject{
								Kind:      rbacv1.ServiceAccountKind,
								Namespace: resources.Shared.ServiceAccount.GetNamespace(),
								Name:      resources.Shared.ServiceAccount.GetName(),
							}),
						}),
					))
				})
			})
		})

		When("user provided permissions with a specific resource namespace", func() {
			When("promise workflow", func() {
				It("should create a cluster role and role binding for the specific namespace", func() {
					factory.Pipeline.Spec.RBAC.Permissions = []v1alpha1.Permission{
						{
							ResourceNamespace: "specific-namespace",
							PolicyRule:        createWatchDeployment(),
						},
					}

					resources, err := factory.Resources(nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(resources.Name).To(Equal(pipeline.GetName()))
					Expect(resources.Shared.Roles).To(BeEmpty())
					Expect(resources.Shared.ClusterRoleBindings).To(HaveLen(1))

					Expect(resources.Shared.ClusterRoles).To(ConsistOf(
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name": Equal(factory.ID),
							}),
						}),
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name": MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, "specific-namespace")),
							}),
							"Rules": ConsistOf(rbacv1.PolicyRule{
								Verbs:         []string{"watch", "create"},
								APIGroups:     []string{"", "apps"},
								Resources:     []string{"deployments", "deployments/status"},
								ResourceNames: []string{"a-deployment", "b-deployment"},
							}),
						}),
					))

					Expect(resources.Shared.RoleBindings).To(HaveLen(1))
					matchUserPermissionsLabels(&resources, resources.Shared.RoleBindings[0].GetLabels())
					Expect(resources.Shared.RoleBindings[0].GetNamespace()).To(Equal("specific-namespace"))
					Expect(resources.Shared.RoleBindings[0].RoleRef.Name).To(Equal(resources.Shared.ClusterRoles[1].GetName()))
					Expect(resources.Shared.RoleBindings[0].RoleRef.Kind).To(Equal("ClusterRole"))
					Expect(resources.Shared.RoleBindings[0].RoleRef.APIGroup).To(Equal("rbac.authorization.k8s.io"))
					Expect(resources.Shared.RoleBindings[0].Subjects).To(ConsistOf(rbacv1.Subject{
						Kind:      rbacv1.ServiceAccountKind,
						Namespace: resources.Shared.ServiceAccount.GetNamespace(),
						Name:      resources.Shared.ServiceAccount.GetName(),
					}))
				})
			})

			When("resource workflow", func() {
				It("should create a cluster role and role binding for the specific namespace", func() {
					factory.ResourceWorkflow = true
					factory.Pipeline.Spec.RBAC.Permissions = []v1alpha1.Permission{
						{
							ResourceNamespace: "specific-namespace",
							PolicyRule:        createWatchDeployment(),
						},
					}

					resources, err := factory.Resources(nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(resources.Name).To(Equal(pipeline.GetName()))

					Expect(resources.Shared.Roles).To(HaveLen(1))
					Expect(resources.Shared.ClusterRoleBindings).To(BeEmpty())

					Expect(resources.Shared.ClusterRoles).To(HaveLen(1))
					Expect(resources.Shared.ClusterRoles[0].Rules).To(ConsistOf(rbacv1.PolicyRule{
						Verbs:         []string{"watch", "create"},
						APIGroups:     []string{"", "apps"},
						Resources:     []string{"deployments", "deployments/status"},
						ResourceNames: []string{"a-deployment", "b-deployment"},
					}))
					matchUserPermissionsLabels(&resources, resources.Shared.ClusterRoles[0].GetLabels())
					matchUserPermissionsLabels(&resources, resources.Shared.RoleBindings[1].GetLabels())

					Expect(resources.Shared.RoleBindings).To(ConsistOf(
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name":      Equal(factory.ID),
								"Namespace": Equal(factory.Namespace),
							}),
						}),
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name":      MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, resources.Shared.ServiceAccount.GetNamespace())),
								"Namespace": Equal("specific-namespace"),
							}),
							"RoleRef": MatchFields(IgnoreExtras, Fields{
								"Name":     Equal(resources.Shared.ClusterRoles[0].GetName()),
								"Kind":     Equal("ClusterRole"),
								"APIGroup": Equal("rbac.authorization.k8s.io"),
							}),
							"Subjects": ConsistOf(rbacv1.Subject{
								Kind:      rbacv1.ServiceAccountKind,
								Namespace: resources.Shared.ServiceAccount.GetNamespace(),
								Name:      resources.Shared.ServiceAccount.GetName(),
							}),
						}),
					))
				})
			})
		})

		When("user provided permissions resource namespace is set to all namespaces", func() {
			When("promise workflow", func() {
				It("should create cluster role and cluster role binding", func() {
					factory.Pipeline.Spec.RBAC.Permissions = []v1alpha1.Permission{
						{
							ResourceNamespace: "*",
							PolicyRule:        createWatchDeployment(),
						},
					}

					resources, err := factory.Resources(nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(resources.Name).To(Equal(pipeline.GetName()))

					Expect(resources.Shared.Roles).To(BeEmpty())
					Expect(resources.Shared.RoleBindings).To(BeEmpty())

					Expect(resources.Shared.ClusterRoles).To(HaveLen(2))
					Expect(resources.Shared.ClusterRoles[1].Rules).To(ConsistOf(rbacv1.PolicyRule{
						Verbs:         []string{"watch", "create"},
						APIGroups:     []string{"", "apps"},
						Resources:     []string{"deployments", "deployments/status"},
						ResourceNames: []string{"a-deployment", "b-deployment"},
					}))

					Expect(resources.Shared.ClusterRoles).To(ConsistOf(
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name": Equal(factory.ID),
							}),
						}),
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name": MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, "kratix-all-namespaces")),
							}),
							"Rules": ConsistOf(rbacv1.PolicyRule{
								Verbs:         []string{"watch", "create"},
								APIGroups:     []string{"", "apps"},
								Resources:     []string{"deployments", "deployments/status"},
								ResourceNames: []string{"a-deployment", "b-deployment"},
							}),
						}),
					))

					Expect(resources.Shared.ClusterRoleBindings).To(ConsistOf(
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name": MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, resources.Shared.ServiceAccount.GetNamespace())),
							}),
							"RoleRef": MatchFields(IgnoreExtras, Fields{
								"Name":     MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, "kratix-all-namespaces")),
								"Kind":     Equal("ClusterRole"),
								"APIGroup": Equal("rbac.authorization.k8s.io"),
							}),
							"Subjects": ConsistOf(rbacv1.Subject{
								Kind:      rbacv1.ServiceAccountKind,
								Namespace: resources.Shared.ServiceAccount.GetNamespace(),
								Name:      resources.Shared.ServiceAccount.GetName(),
							}),
						}),
						MatchFields(IgnoreExtras, Fields{
							"ObjectMeta": MatchFields(IgnoreExtras, Fields{
								"Name": Equal(factory.ID),
							}),
						}),
					))
				})
			})

			When("resource workflow", func() {
				It("should create cluster role and cluster role binding", func() {
					factory.ResourceWorkflow = true
					factory.Pipeline.Spec.RBAC.Permissions = []v1alpha1.Permission{
						{
							ResourceNamespace: "*",
							PolicyRule:        createWatchDeployment(),
						},
					}

					resources, err := factory.Resources(nil)
					Expect(err).ToNot(HaveOccurred())
					Expect(resources.Name).To(Equal(pipeline.GetName()))

					Expect(resources.Shared.Roles).To(HaveLen(1))
					Expect(resources.Shared.RoleBindings).To(HaveLen(1))

					Expect(resources.Shared.ClusterRoles).To(HaveLen(1))
					Expect(resources.Shared.ClusterRoles[0].Rules).To(ConsistOf(rbacv1.PolicyRule{
						Verbs:         []string{"watch", "create"},
						APIGroups:     []string{"", "apps"},
						Resources:     []string{"deployments", "deployments/status"},
						ResourceNames: []string{"a-deployment", "b-deployment"},
					}))
					matchUserPermissionsLabels(&resources, resources.Shared.ClusterRoles[0].GetLabels())

					Expect(resources.Shared.ClusterRoleBindings).To(HaveLen(1))
					matchUserPermissionsLabels(&resources, resources.Shared.ClusterRoleBindings[0].GetLabels())
					Expect(resources.Shared.ClusterRoleBindings[0].RoleRef.Name).To(Equal(resources.Shared.ClusterRoles[0].GetName()))
					Expect(resources.Shared.ClusterRoleBindings[0].RoleRef.Kind).To(Equal("ClusterRole"))
					Expect(resources.Shared.ClusterRoleBindings[0].RoleRef.APIGroup).To(Equal("rbac.authorization.k8s.io"))
					Expect(resources.Shared.ClusterRoleBindings[0].Subjects).To(ConsistOf(rbacv1.Subject{
						Kind:      rbacv1.ServiceAccountKind,
						Namespace: resources.Shared.ServiceAccount.GetNamespace(),
						Name:      resources.Shared.ServiceAccount.GetName(),
					}))
				})
			})
		})

		When("there are both specific- and all-namespace resource namespaces", func() {
			It("should create a cluster role and role binding for the specific namespace and a cluster role and cluster role binding for all namespaces", func() {
				factory.Pipeline.Spec.RBAC.Permissions = []v1alpha1.Permission{
					{
						PolicyRule: createWatchDeployment(),
					},
					{
						ResourceNamespace: "specific-namespace",
						PolicyRule: rbacv1.PolicyRule{
							Verbs:         []string{"watch", "create"},
							APIGroups:     []string{"", "apps"},
							Resources:     []string{"deployments", "deployments/status"},
							ResourceNames: []string{"c-deployment", "d-deployment"},
						},
					},
					{
						ResourceNamespace: "*",
						PolicyRule: rbacv1.PolicyRule{
							Verbs:         []string{"watch", "create"},
							APIGroups:     []string{"", "apps"},
							Resources:     []string{"deployments", "deployments/status"},
							ResourceNames: []string{"e-deployment", "f-deployment"},
						},
					},
				}

				resources, err := factory.Resources(nil)
				Expect(err).ToNot(HaveOccurred())
				Expect(resources.Name).To(Equal(pipeline.GetName()))

				Expect(resources.Shared.RoleBindings).To(HaveLen(2))
				Expect(resources.Shared.ClusterRoles).To(HaveLen(3))
				Expect(resources.Shared.ClusterRoleBindings).To(HaveLen(2))

				By("creating the role in the pipeline namespace")
				Expect(resources.Shared.Roles).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      MatchRegexp(fmt.Sprintf(`^%s-\b\w{5}\b$`, factory.ID)),
							"Namespace": Equal(factory.Namespace),
						}),
						"Rules": ConsistOf(rbacv1.PolicyRule{
							Verbs:         []string{"watch", "create"},
							APIGroups:     []string{"", "apps"},
							Resources:     []string{"deployments", "deployments/status"},
							ResourceNames: []string{"a-deployment", "b-deployment"},
						}),
					}),
				))
				matchUserPermissionsLabels(&resources, resources.Shared.Roles[0].GetLabels())

				By("creating the role bindings for the pipeline and specific namespace")
				Expect(resources.Shared.RoleBindings).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      MatchRegexp(fmt.Sprintf(`^%s-\b\w{5}\b$`, factory.ID)),
							"Namespace": Equal(factory.Namespace),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name":     MatchRegexp(fmt.Sprintf(`^%s-\b\w{5}\b$`, factory.ID)),
							"Kind":     Equal("Role"),
							"APIGroup": Equal("rbac.authorization.k8s.io"),
						}),
						"Subjects": ConsistOf(rbacv1.Subject{
							Kind:      rbacv1.ServiceAccountKind,
							Namespace: resources.Shared.ServiceAccount.GetNamespace(),
							Name:      resources.Shared.ServiceAccount.GetName(),
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, resources.Shared.ServiceAccount.GetNamespace())),
							"Namespace": Equal("specific-namespace"),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name":     MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, "specific-namespace")),
							"Kind":     Equal("ClusterRole"),
							"APIGroup": Equal("rbac.authorization.k8s.io"),
						}),
						"Subjects": ConsistOf(rbacv1.Subject{
							Kind:      rbacv1.ServiceAccountKind,
							Namespace: resources.Shared.ServiceAccount.GetNamespace(),
							Name:      resources.Shared.ServiceAccount.GetName(),
						}),
					}),
				))

				By("creating a cluster role for the specific- and all-namespace permissions, and the promise permissions")
				Expect(resources.Shared.ClusterRoles).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(factory.ID),
							"Labels": SatisfyAll(
								HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()),
								HaveLen(1),
							),
						}),
						"Rules": ConsistOf(rbacv1.PolicyRule{
							Verbs:     []string{"get", "list", "update", "create", "patch"},
							APIGroups: []string{v1alpha1.GroupVersion.Group},
							Resources: []string{v1alpha1.PromisePlural, v1alpha1.PromisePlural + "/status", "works"},
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, "specific-namespace")),
							"Labels": SatisfyAll(
								HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()),
								HaveKeyWithValue(v1alpha1.WorkflowTypeLabel, "fakeType"),
								HaveKeyWithValue(v1alpha1.WorkflowActionLabel, "fakeAction"),
								HaveKeyWithValue(v1alpha1.PipelineNameLabel, factory.Pipeline.Name),
								HaveKeyWithValue(v1alpha1.PipelineNamespaceLabel, factory.Namespace),
								HaveKeyWithValue(v1alpha1.UserPermissionResourceNamespaceLabel, "specific-namespace"),
								HaveLen(6),
							),
						}),
						"Rules": ConsistOf(rbacv1.PolicyRule{
							Verbs:         []string{"watch", "create"},
							APIGroups:     []string{"", "apps"},
							Resources:     []string{"deployments", "deployments/status"},
							ResourceNames: []string{"c-deployment", "d-deployment"},
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, "kratix-all-namespaces")),
							"Labels": SatisfyAll(
								HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()),
								HaveKeyWithValue(v1alpha1.WorkflowTypeLabel, "fakeType"),
								HaveKeyWithValue(v1alpha1.WorkflowActionLabel, "fakeAction"),
								HaveKeyWithValue(v1alpha1.PipelineNameLabel, factory.Pipeline.Name),
								HaveKeyWithValue(v1alpha1.PipelineNamespaceLabel, factory.Namespace),
								HaveKeyWithValue(v1alpha1.UserPermissionResourceNamespaceLabel, "kratix_all_namespaces"),
								HaveLen(6),
							),
						}),
						"Rules": ConsistOf(rbacv1.PolicyRule{
							Verbs:         []string{"watch", "create"},
							APIGroups:     []string{"", "apps"},
							Resources:     []string{"deployments", "deployments/status"},
							ResourceNames: []string{"e-deployment", "f-deployment"},
						}),
					}),
				))

				By("creating the cluster role binding for all namespaces, and the promise permissions")
				Expect(resources.Shared.ClusterRoleBindings).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, resources.Shared.ServiceAccount.GetNamespace())),
							"Labels": SatisfyAll(
								HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()),
								HaveKeyWithValue(v1alpha1.WorkflowTypeLabel, "fakeType"),
								HaveKeyWithValue(v1alpha1.WorkflowActionLabel, "fakeAction"),
								HaveKeyWithValue(v1alpha1.PipelineNameLabel, factory.Pipeline.Name),
								HaveKeyWithValue(v1alpha1.PipelineNamespaceLabel, factory.Namespace),
								HaveLen(5),
							),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name":     MatchRegexp(fmt.Sprintf(`^%s-%s-\b\w{5}\b$`, factory.ID, "kratix-all-namespaces")),
							"Kind":     Equal("ClusterRole"),
							"APIGroup": Equal("rbac.authorization.k8s.io"),
						}),
						"Subjects": ConsistOf(rbacv1.Subject{
							Kind:      rbacv1.ServiceAccountKind,
							Namespace: resources.Shared.ServiceAccount.GetNamespace(),
							Name:      resources.Shared.ServiceAccount.GetName(),
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(factory.ID),
							"Labels": SatisfyAll(
								HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()),
								HaveLen(1),
							),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name":     Equal(factory.ID),
							"Kind":     Equal("ClusterRole"),
							"APIGroup": Equal("rbac.authorization.k8s.io"),
						}),
						"Subjects": ConsistOf(rbacv1.Subject{
							Kind:      rbacv1.ServiceAccountKind,
							Namespace: resources.Shared.ServiceAccount.GetNamespace(),
							Name:      resources.Shared.ServiceAccount.GetName(),
						}),
					}),
				))
			})
		})
	})

	Describe("PipelinesFromUnstructured", func() {
		It("generates a list of pipelines from a list of unstructured pipeline objects", func() {
			unstructuredPipelines := []unstructured.Unstructured{
				{
					Object: map[string]interface{}{
						"apiVersion": "platform.kratix.io/v1alpha1",
						"kind":       "Pipeline",
						"metadata": map[string]interface{}{
							"namespace": "default",
							"name":      "pipeline1",
						},
						"spec": map[string]interface{}{
							"containers": []map[string]interface{}{
								{
									"name":  "promise-configure",
									"image": "my-registry.io/configure",
								},
							},
						},
					},
				},
				{
					Object: map[string]interface{}{
						"apiVersion": "platform.kratix.io/v1alpha1",
						"kind":       "Pipeline",
						"metadata": map[string]interface{}{
							"namespace": "default",
							"name":      "pipeline2",
						},
						"spec": map[string]interface{}{
							"containers": []map[string]interface{}{
								map[string]interface{}{
									"name":  "promise-configure",
									"image": "my-registry.io/configure",
								},
							},
						},
					},
				},
			}

			expectedPipelines := []v1alpha1.Pipeline{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pipeline1",
						Namespace: "default",
					},
					APIVersion: "platform.kratix.io/v1alpha1",
					Kind:       "Pipeline",
					Spec: v1alpha1.PipelineSpec{
						Containers: []v1alpha1.Container{
							{Name: "promise-configure", Image: "my-registry.io/configure"},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pipeline2",
						Namespace: "default",
					},
					APIVersion: "platform.kratix.io/v1alpha1",
					Kind:       "Pipeline",
					Spec: v1alpha1.PipelineSpec{
						Containers: []v1alpha1.Container{
							{Name: "promise-configure", Image: "my-registry.io/configure"},
						},
					},
				},
			}

			pipelines, err := v1alpha1.PipelinesFromUnstructured(unstructuredPipelines, logr.Discard())
			Expect(err).ToNot(HaveOccurred())
			Expect(pipelines).To(Equal(expectedPipelines))
		})

		When("the object is not of kind Pipeline", func() {
			It("raises an error", func() {
				unstructuredPipelines := []unstructured.Unstructured{
					{
						Object: map[string]interface{}{
							"apiVersion": "platform.kratix.io/v1alpha1",
							"kind":       "ConfigMap",
							"metadata": map[string]interface{}{
								"namespace": "default",
								"name":      "pipeline1",
							},
							"spec": map[string]interface{}{
								"containers": []map[string]interface{}{
									map[string]interface{}{
										"name":  "promise-configure",
										"image": "my-registry.io/configure",
									},
								},
							},
						},
					},
				}

				_, err := v1alpha1.PipelinesFromUnstructured(unstructuredPipelines, logr.Discard())
				Expect(err).To(MatchError(
					"unsupported pipeline \"pipeline1\" with APIVersion \"ConfigMap/platform.kratix.io/v1alpha1\"",
				),
				)
			})
		})

		When("the object does not have the apiVersion platform.kratix.io/v1alpha1", func() {
			It("raises an error", func() {
				unstructuredPipelines := []unstructured.Unstructured{
					{
						Object: map[string]interface{}{
							"apiVersion": "v1",
							"kind":       "Pipeline",
							"metadata": map[string]interface{}{
								"namespace": "default",
								"name":      "pipeline1",
							},
							"spec": map[string]interface{}{
								"containers": []map[string]interface{}{
									map[string]interface{}{
										"name":  "promise-configure",
										"image": "my-registry.io/configure",
									},
								},
							},
						},
					},
				}

				_, err := v1alpha1.PipelinesFromUnstructured(unstructuredPipelines, logr.Discard())
				Expect(err).To(MatchError(
					"unsupported pipeline \"pipeline1\" with APIVersion \"Pipeline/v1\"",
				))
			})
		})

		When("the given Pipeline unknown fields", func() {
			It("returns an error", func() {
				unstructuredPipelines := []unstructured.Unstructured{
					{
						Object: map[string]interface{}{
							"apiVersion": "platform.kratix.io/v1alpha1",
							"kind":       "Pipeline",
							"metadata": map[string]interface{}{
								"namespace": "default",
								"name":      "pipeline1",
							},
							"rbac": map[string]interface{}{
								"serviceAccount": "should-not-be-here",
							},
							"spec": map[string]interface{}{
								"containers": []map[string]interface{}{
									{
										"name":  "promise-configure",
										"image": "my-registry.io/configure",
									},
								},
							},
						},
					},
				}

				_, err := v1alpha1.PipelinesFromUnstructured(unstructuredPipelines, logr.Discard())
				Expect(err).To(MatchError(ContainSubstring("failed unmarshalling pipeline pipeline1: json: unknown field")))
			})
		})
	})
})

func matchUserPermissionsLabels(pipelineJobResources *v1alpha1.PipelineJobResources, labels map[string]string) {
	jobLabels := pipelineJobResources.Job.GetLabels()
	Expect(labels).To(SatisfyAll(
		HaveKeyWithValue(v1alpha1.PromiseNameLabel, jobLabels[v1alpha1.PromiseNameLabel]),
		HaveKeyWithValue(v1alpha1.PipelineNameLabel, pipelineJobResources.Name),
		HaveKeyWithValue(v1alpha1.WorkflowTypeLabel, jobLabels[v1alpha1.WorkflowTypeLabel]),
		HaveKeyWithValue(v1alpha1.WorkflowActionLabel, jobLabels[v1alpha1.WorkflowActionLabel]),
	))
}

func matchClusterRolesAndBindings(clusterRoles []rbacv1.ClusterRole, clusterRoleBindings []rbacv1.ClusterRoleBinding, factory *v1alpha1.PipelineFactory, sa *corev1.ServiceAccount) {
	ExpectWithOffset(1, clusterRoles).To(HaveLen(1))
	ExpectWithOffset(1, clusterRoles[0].GetName()).To(Equal(factory.ID))
	ExpectWithOffset(1, clusterRoles[0].GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, factory.Promise.GetName()))
	ExpectWithOffset(1, clusterRoles[0].Rules).To(ConsistOf(rbacv1.PolicyRule{
		APIGroups: []string{v1alpha1.GroupVersion.Group},
		Resources: []string{v1alpha1.PromisePlural, v1alpha1.PromisePlural + "/status", "works"},
		Verbs:     []string{"get", "list", "update", "create", "patch"},
	}))

	ExpectWithOffset(1, clusterRoleBindings).To(HaveLen(1))
	ExpectWithOffset(1, clusterRoleBindings[0].GetName()).To(Equal(factory.ID))
	ExpectWithOffset(1, clusterRoleBindings[0].GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, factory.Promise.GetName()))
	ExpectWithOffset(1, clusterRoleBindings[0].RoleRef).To(Equal(rbacv1.RoleRef{
		APIGroup: rbacv1.GroupName,
		Kind:     "ClusterRole",
		Name:     "factoryID",
	}))

	ExpectWithOffset(1, clusterRoleBindings[0].Subjects).To(ConsistOf(rbacv1.Subject{
		Kind:      rbacv1.ServiceAccountKind,
		Namespace: sa.GetNamespace(),
		Name:      sa.GetName(),
	}))
}

func matchResourceRolesAndBindings(roles []rbacv1.Role, bindings []rbacv1.RoleBinding, factory *v1alpha1.PipelineFactory, sa *corev1.ServiceAccount, promiseCrd *apiextensionsv1.CustomResourceDefinition) {
	Expect(roles).To(HaveLen(1))
	Expect(roles[0].GetName()).To(Equal(factory.ID))
	Expect(roles[0].GetNamespace()).To(Equal(factory.Namespace))
	Expect(roles[0].GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, factory.Promise.GetName()))
	Expect(roles[0].Rules).To(ConsistOf(rbacv1.PolicyRule{
		APIGroups: []string{promiseCrd.Spec.Group},
		Resources: []string{promiseCrd.Spec.Names.Plural, promiseCrd.Spec.Names.Plural + "/status"},
		Verbs:     []string{"get", "list", "update", "create", "patch"},
	}, rbacv1.PolicyRule{
		APIGroups: []string{v1alpha1.GroupVersion.Group},
		Resources: []string{"works"},
		Verbs:     []string{"*"},
	}))

	Expect(bindings).To(HaveLen(1))
	Expect(bindings[0].GetName()).To(Equal(factory.ID))
	Expect(bindings[0].GetNamespace()).To(Equal(factory.Namespace))
	Expect(bindings[0].GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, factory.Promise.GetName()))
	Expect(bindings[0].RoleRef).To(Equal(rbacv1.RoleRef{
		APIGroup: rbacv1.GroupName,
		Kind:     "Role",
		Name:     "factoryID",
	}))
	Expect(bindings[0].Subjects).To(ConsistOf(rbacv1.Subject{
		Kind:      rbacv1.ServiceAccountKind,
		Namespace: sa.GetNamespace(),
		Name:      sa.GetName(),
	}))
}

func matchConfigureConfigmap(c *corev1.ConfigMap, factory *v1alpha1.PipelineFactory) {
	ExpectWithOffset(1, c.GetName()).To(Equal("destination-selectors-" + factory.Promise.GetName()))
	ExpectWithOffset(1, c.GetNamespace()).To(Equal(factory.Namespace))
	ExpectWithOffset(1, c.GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, factory.Promise.GetName()))
	ExpectWithOffset(1, c.Data).To(
		HaveKeyWithValue(
			"destinationSelectors",
			"- matchLabels:\n    label: value\n  source: promise\n- matchLabels:\n    another-label: another-value\n  source: promise\n"))
}

func promiseHash(promise *v1alpha1.Promise) string {
	uPromise, err := promise.ToUnstructured()
	Expect(err).ToNot(HaveOccurred())
	h, err := hash.ComputeHashForResource(uPromise)
	Expect(err).ToNot(HaveOccurred())
	return h
}

func resourceHash(resource *unstructured.Unstructured) string {
	h, err := hash.ComputeHashForResource(resource)
	Expect(err).ToNot(HaveOccurred())
	return h
}

func combinedHash(hashes ...string) string {
	return hash.ComputeHash(strings.Join(hashes, "-"))
}

func createWatchDeployment() rbacv1.PolicyRule {
	return rbacv1.PolicyRule{
		Verbs:         []string{"watch", "create"},
		APIGroups:     []string{"", "apps"},
		Resources:     []string{"deployments", "deployments/status"},
		ResourceNames: []string{"a-deployment", "b-deployment"},
	}
}
