package v1alpha1_test

import (
	"encoding/json"
	"strings"

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
)

var _ = Describe("Pipeline", func() {
	var (
		pipeline        *v1alpha1.Pipeline
		promise         *v1alpha1.Promise
		promiseCrd      *apiextensionsv1.CustomResourceDefinition
		resourceRequest *unstructured.Unstructured
	)

	BeforeEach(func() {
		secretRef := &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "secretName"}}

		pipeline = &v1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pipelineName",
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
					},
					{Name: "container-1", Image: "container-1-image"},
				},
				Volumes:          []corev1.Volume{{Name: "customVolume", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: "imagePullSecret"}},
			},
		}
		promiseCrd = &apiextensionsv1.CustomResourceDefinition{
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "promise.crd.group",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural: "promiseCrdPlural",
				},
			},
		}

		rawCrd, err := json.Marshal(promiseCrd)
		Expect(err).ToNot(HaveOccurred())
		promise = &v1alpha1.Promise{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "fake.promise.group/v1",
				Kind:       "promisekind",
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
				"apiVersion": "fake.resource.group/v1",
				"kind":       "promisekind",
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
				Expect(f.ResourceWorkflow).To(BeFalse())
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
				Expect(f.ResourceWorkflow).To(BeTrue())
			})
		})
	})

	Describe("PipelineFactory", func() {
		var (
			factory *v1alpha1.PipelineFactory
		)

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

		Describe("Resources", func() {
			When("building resources for the configure action", func() {
				It("should return a list of resources", func() {
					factory.WorkflowAction = v1alpha1.WorkflowActionConfigure
					env := []corev1.EnvVar{{Name: "env1", Value: "value1"}}
					role, err := factory.ObjectRole()
					Expect(err).ToNot(HaveOccurred())
					serviceAccount := factory.ServiceAccount()
					configMap, err := factory.ConfigMap(promise.GetWorkloadGroupScheduling())
					Expect(err).ToNot(HaveOccurred())
					job, err := factory.PipelineJob(configMap, serviceAccount, env)
					Expect(err).ToNot(HaveOccurred())

					resources, err := factory.Resources(env)
					Expect(err).ToNot(HaveOccurred())
					Expect(resources.Name).To(Equal(pipeline.GetName()))
					Expect(resources.RequiredResources).To(HaveLen(4))
					Expect(resources.RequiredResources).To(ConsistOf(
						serviceAccount, role, factory.ObjectRoleBinding(role.GetName(), serviceAccount), configMap,
					))
					Expect(resources.RequiredResources[0]).To(BeAssignableToTypeOf(&corev1.ServiceAccount{}))
					Expect(resources.RequiredResources[0].GetName()).To(Equal("factoryID"))
					Expect(resources.Job.Name).To(HavePrefix("kratix-%s-%s", promise.GetName(), pipeline.GetName()))
					job.Name = resources.Job.Name
					Expect(resources.Job).To(Equal(job))
				})
			})

			When("building resources for the delete action", func() {
				It("should return a list of resources", func() {
					factory.WorkflowAction = v1alpha1.WorkflowActionDelete
					env := []corev1.EnvVar{{Name: "env1", Value: "value1"}}
					role, err := factory.ObjectRole()
					Expect(err).ToNot(HaveOccurred())
					serviceAccount := factory.ServiceAccount()
					configMap, err := factory.ConfigMap(promise.GetWorkloadGroupScheduling())
					Expect(err).ToNot(HaveOccurred())
					job, err := factory.PipelineJob(configMap, serviceAccount, env)
					Expect(err).ToNot(HaveOccurred())

					resources, err := factory.Resources(env)
					Expect(err).ToNot(HaveOccurred())
					Expect(resources.Name).To(Equal(pipeline.GetName()))
					Expect(resources.RequiredResources).To(HaveLen(3))
					Expect(resources.RequiredResources).To(ConsistOf(
						serviceAccount, role, factory.ObjectRoleBinding(role.GetName(), serviceAccount),
					))

					Expect(resources.Job.Name).To(HavePrefix("kratix-%s-%s", promise.GetName(), pipeline.GetName()))
					job.Name = resources.Job.Name
					Expect(resources.Job).To(Equal(job))
				})
			})
		})

		Describe("ServiceAccount", func() {
			It("should return a service account", func() {
				sa := factory.ServiceAccount()
				Expect(sa).ToNot(BeNil())
				Expect(sa.GetName()).To(Equal(factory.ID))
				Expect(sa.GetNamespace()).To(Equal(factory.Namespace))
				Expect(sa.GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()))
			})
		})

		Describe("ObjectRole", func() {
			When("building a role for a promise pipeline", func() {
				It("returns a cluster role", func() {
					objectRole, err := factory.ObjectRole()
					Expect(err).ToNot(HaveOccurred())
					Expect(objectRole).ToNot(BeNil())
					Expect(objectRole).To(BeAssignableToTypeOf(&rbacv1.ClusterRole{}))

					clusterRole := objectRole.(*rbacv1.ClusterRole)
					Expect(clusterRole.GetName()).To(Equal(factory.ID))
					Expect(clusterRole.GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()))

					Expect(clusterRole.Rules).To(ConsistOf(rbacv1.PolicyRule{
						APIGroups: []string{v1alpha1.GroupVersion.Group},
						Resources: []string{v1alpha1.PromisePlural, v1alpha1.PromisePlural + "/status", "works"},
						Verbs:     []string{"get", "list", "update", "create", "patch"},
					}))
				})
			})

			When("building a role for a resource pipeline", func() {
				It("returns a role", func() {
					factory.ResourceWorkflow = true

					objectRole, err := factory.ObjectRole()
					Expect(err).ToNot(HaveOccurred())
					Expect(objectRole).ToNot(BeNil())
					Expect(objectRole).To(BeAssignableToTypeOf(&rbacv1.Role{}))

					role := objectRole.(*rbacv1.Role)
					Expect(role.GetName()).To(Equal(factory.ID))
					Expect(role.GetNamespace()).To(Equal(factory.Namespace))
					Expect(role.GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()))

					Expect(role.Rules).To(ConsistOf(rbacv1.PolicyRule{
						APIGroups: []string{promiseCrd.Spec.Group},
						Resources: []string{promiseCrd.Spec.Names.Plural, promiseCrd.Spec.Names.Plural + "/status"},
						Verbs:     []string{"get", "list", "update", "create", "patch"},
					}, rbacv1.PolicyRule{
						APIGroups: []string{v1alpha1.GroupVersion.Group},
						Resources: []string{"works"},
						Verbs:     []string{"*"},
					}))
				})
			})
		})

		Describe("ObjectRoleBinding", func() {
			var serviceAccount *corev1.ServiceAccount
			BeforeEach(func() {
				serviceAccount = &corev1.ServiceAccount{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "serviceAccountName",
						Namespace: "serviceAccountNamespace",
					},
				}
			})

			When("building a role binding for a promise pipeline", func() {
				It("returns a cluster role binding", func() {
					objectRoleBinding := factory.ObjectRoleBinding("aClusterRole", serviceAccount)
					Expect(objectRoleBinding).ToNot(BeNil())
					Expect(objectRoleBinding).To(BeAssignableToTypeOf(&rbacv1.ClusterRoleBinding{}))

					clusterRoleBinding := objectRoleBinding.(*rbacv1.ClusterRoleBinding)
					Expect(clusterRoleBinding.GetName()).To(Equal(factory.ID))
					Expect(clusterRoleBinding.GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()))

					Expect(clusterRoleBinding.RoleRef).To(Equal(rbacv1.RoleRef{
						APIGroup: rbacv1.GroupName,
						Kind:     "ClusterRole",
						Name:     "aClusterRole",
					}))

					Expect(clusterRoleBinding.Subjects).To(ConsistOf(rbacv1.Subject{
						Kind:      rbacv1.ServiceAccountKind,
						Namespace: serviceAccount.GetNamespace(),
						Name:      serviceAccount.GetName(),
					}))
				})
			})

			When("building a role for a resource pipeline", func() {
				It("returns a role", func() {
					factory.ResourceWorkflow = true

					objectRoleBinding := factory.ObjectRoleBinding("aNamespacedRole", serviceAccount)
					Expect(objectRoleBinding).ToNot(BeNil())
					Expect(objectRoleBinding).To(BeAssignableToTypeOf(&rbacv1.RoleBinding{}))

					roleBinding := objectRoleBinding.(*rbacv1.RoleBinding)
					Expect(roleBinding.GetName()).To(Equal(factory.ID))
					Expect(roleBinding.GetNamespace()).To(Equal(factory.Namespace))
					Expect(roleBinding.GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()))

					Expect(roleBinding.RoleRef).To(Equal(rbacv1.RoleRef{
						APIGroup: rbacv1.GroupName,
						Kind:     "Role",
						Name:     "aNamespacedRole",
					}))
					Expect(roleBinding.Subjects).To(ConsistOf(rbacv1.Subject{
						Kind:      rbacv1.ServiceAccountKind,
						Namespace: serviceAccount.GetNamespace(),
						Name:      serviceAccount.GetName(),
					}))
				})
			})
		})

		Describe("ConfigMap", func() {
			It("should return a config map", func() {
				workloadGroupScheduling := []v1alpha1.WorkloadGroupScheduling{
					{MatchLabels: map[string]string{"label": "value"}, Source: "promise"},
					{MatchLabels: map[string]string{"another-label": "another-value"}, Source: "resource"},
				}
				cm, err := factory.ConfigMap(workloadGroupScheduling)
				Expect(err).ToNot(HaveOccurred())
				Expect(cm).ToNot(BeNil())
				Expect(cm.GetName()).To(Equal("destination-selectors-" + factory.Promise.GetName()))
				Expect(cm.GetNamespace()).To(Equal(factory.Namespace))
				Expect(cm.GetLabels()).To(HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()))
				Expect(cm.Data).To(HaveKeyWithValue("destinationSelectors", "- matchlabels:\n    label: value\n  source: promise\n- matchlabels:\n    another-label: another-value\n  source: resource\n"))
			})
		})

		Describe("DefaultVolumes", func() {
			It("should return a list of default volumes", func() {
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: "aConfigMap",
					},
				}
				volumes := factory.DefaultVolumes(configMap)
				Expect(volumes).To(HaveLen(1))
				Expect(volumes).To(ConsistOf(corev1.Volume{
					Name: "promise-scheduling",
					VolumeSource: corev1.VolumeSource{
						ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: configMap.GetName()},
							Items: []corev1.KeyToPath{
								{Key: "destinationSelectors", Path: "promise-scheduling"},
							},
						},
					},
				}))
			})
		})

		Describe("DefaultPipelineVolumes", func() {
			It("should return a list of default pipeline volumes", func() {
				volumes, volumeMounts := factory.DefaultPipelineVolumes()
				Expect(volumes).To(HaveLen(3))
				Expect(volumeMounts).To(HaveLen(3))
				emptyDir := corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
				Expect(volumes).To(ConsistOf(
					corev1.Volume{Name: "shared-input", VolumeSource: emptyDir},
					corev1.Volume{Name: "shared-output", VolumeSource: emptyDir},
					corev1.Volume{Name: "shared-metadata", VolumeSource: emptyDir},
				))
				Expect(volumeMounts).To(ConsistOf(
					corev1.VolumeMount{Name: "shared-input", MountPath: "/kratix/input", ReadOnly: true},
					corev1.VolumeMount{Name: "shared-output", MountPath: "/kratix/output"},
					corev1.VolumeMount{Name: "shared-metadata", MountPath: "/kratix/metadata"},
				))
			})
		})

		Describe("DefaultEnvVars", func() {
			It("should return a list of default environment variables", func() {
				envVars := factory.DefaultEnvVars()
				Expect(envVars).To(HaveLen(3))
				Expect(envVars).To(ConsistOf(
					corev1.EnvVar{Name: "KRATIX_WORKFLOW_ACTION", Value: "fakeAction"},
					corev1.EnvVar{Name: "KRATIX_WORKFLOW_TYPE", Value: "fakeType"},
					corev1.EnvVar{Name: "KRATIX_PROMISE_NAME", Value: promise.GetName()},
				))
			})
		})

		Describe("ReaderContainer", func() {
			When("building the reader container for a promise pipeline", func() {
				It("returns a the reader container with the promise information", func() {
					container := factory.ReaderContainer()
					Expect(container).ToNot(BeNil())
					Expect(container.Name).To(Equal("reader"))
					Expect(container.Command).To(Equal([]string{"sh", "-c", "reader"}))
					Expect(container.Image).To(Equal(workCreatorImage))
					Expect(container.Env).To(ConsistOf(
						corev1.EnvVar{Name: "OBJECT_KIND", Value: promise.GroupVersionKind().Kind},
						corev1.EnvVar{Name: "OBJECT_GROUP", Value: promise.GroupVersionKind().Group},
						corev1.EnvVar{Name: "OBJECT_NAME", Value: promise.GetName()},
						corev1.EnvVar{Name: "OBJECT_NAMESPACE", Value: factory.Namespace},
						corev1.EnvVar{Name: "KRATIX_WORKFLOW_TYPE", Value: string(factory.WorkflowType)},
					))
					Expect(container.VolumeMounts).To(ConsistOf(
						corev1.VolumeMount{Name: "shared-input", MountPath: "/kratix/input"},
						corev1.VolumeMount{Name: "shared-output", MountPath: "/kratix/output"},
					))
				})
			})

			When("building the reader container for a resource pipeline", func() {
				It("returns a the reader container with the resource information", func() {
					factory.ResourceWorkflow = true
					container := factory.ReaderContainer()
					Expect(container).ToNot(BeNil())
					Expect(container.Name).To(Equal("reader"))
					Expect(container.Image).To(Equal(workCreatorImage))
					Expect(container.Env).To(ConsistOf(
						corev1.EnvVar{Name: "OBJECT_KIND", Value: resourceRequest.GroupVersionKind().Kind},
						corev1.EnvVar{Name: "OBJECT_GROUP", Value: resourceRequest.GroupVersionKind().Group},
						corev1.EnvVar{Name: "OBJECT_NAME", Value: resourceRequest.GetName()},
						corev1.EnvVar{Name: "OBJECT_NAMESPACE", Value: factory.Namespace},
						corev1.EnvVar{Name: "KRATIX_WORKFLOW_TYPE", Value: string(factory.WorkflowType)},
					))
					Expect(container.VolumeMounts).To(ConsistOf(
						corev1.VolumeMount{Name: "shared-input", MountPath: "/kratix/input"},
						corev1.VolumeMount{Name: "shared-output", MountPath: "/kratix/output"},
					))
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
					container := factory.WorkCreatorContainer()
					Expect(container).ToNot(BeNil())
					Expect(container.Name).To(Equal("work-writer"))
					Expect(container.Image).To(Equal(workCreatorImage))
					Expect(container.Command).To(Equal([]string{"sh", "-c", "./work-creator " + expectedFlags}))
					Expect(container.VolumeMounts).To(ConsistOf(
						corev1.VolumeMount{Name: "shared-output", MountPath: "/work-creator-files/input"},
						corev1.VolumeMount{Name: "shared-metadata", MountPath: "/work-creator-files/metadata"},
						corev1.VolumeMount{Name: "promise-scheduling", MountPath: "/work-creator-files/kratix-system"},
					))

				})
			})
			When("building the work creator container for a resource pipeline", func() {
				It("returns a the work creator container with the appropriate command", func() {
					factory.ResourceWorkflow = true

					expectedFlags := strings.Join([]string{
						"-input-directory", "/work-creator-files",
						"-promise-name", promise.GetName(),
						"-pipeline-name", pipeline.GetName(),
						"-namespace", factory.Namespace,
						"-workflow-type", string(factory.WorkflowType),
						"-resource-name", resourceRequest.GetName(),
					}, " ")
					container := factory.WorkCreatorContainer()
					Expect(container).ToNot(BeNil())
					Expect(container.Name).To(Equal("work-writer"))
					Expect(container.Image).To(Equal(workCreatorImage))
					Expect(container.Command).To(Equal([]string{"sh", "-c", "./work-creator " + expectedFlags}))
					Expect(container.VolumeMounts).To(ConsistOf(
						corev1.VolumeMount{Name: "shared-output", MountPath: "/work-creator-files/input"},
						corev1.VolumeMount{Name: "shared-metadata", MountPath: "/work-creator-files/metadata"},
						corev1.VolumeMount{Name: "promise-scheduling", MountPath: "/work-creator-files/kratix-system"},
					))
				})
			})
		})

		Describe("PipelineContainers", func() {
			var defaultEnvVars []corev1.EnvVar
			var defaultVolumes []corev1.Volume
			var defaultVolumeMounts []corev1.VolumeMount

			BeforeEach(func() {
				defaultEnvVars = factory.DefaultEnvVars()
				defaultVolumes, defaultVolumeMounts = factory.DefaultPipelineVolumes()
			})
			It("returns the pipeline containers and volumes", func() {
				containers, volumes := factory.PipelineContainers()
				Expect(containers).To(HaveLen(2))
				Expect(volumes).To(HaveLen(4))

				expectedContainer0 := pipeline.Spec.Containers[0]
				Expect(containers[0]).To(MatchFields(IgnoreExtras, Fields{
					"Name":            Equal(expectedContainer0.Name),
					"Image":           Equal(expectedContainer0.Image),
					"Args":            Equal(expectedContainer0.Args),
					"Command":         Equal(expectedContainer0.Command),
					"Env":             Equal(append(defaultEnvVars, expectedContainer0.Env...)),
					"EnvFrom":         Equal(expectedContainer0.EnvFrom),
					"VolumeMounts":    Equal(append(defaultVolumeMounts, expectedContainer0.VolumeMounts...)),
					"ImagePullPolicy": Equal(expectedContainer0.ImagePullPolicy),
				}))
				Expect(volumes).To(Equal(append(defaultVolumes, pipeline.Spec.Volumes...)))

				expectedContainer1 := pipeline.Spec.Containers[1]
				Expect(containers[1]).To(MatchFields(IgnoreExtras, Fields{
					"Name":            Equal(expectedContainer1.Name),
					"Image":           Equal(expectedContainer1.Image),
					"Args":            BeNil(),
					"Command":         BeNil(),
					"Env":             Equal(defaultEnvVars),
					"EnvFrom":         BeNil(),
					"VolumeMounts":    Equal(defaultVolumeMounts),
					"ImagePullPolicy": BeEmpty(),
				}))
			})
		})

		Describe("StatusWriterContainer", func() {
			var obj *unstructured.Unstructured
			var envVars []corev1.EnvVar
			BeforeEach(func() {
				obj = &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "some.api.group/someVersion",
						"kind":       "somekind",
						"metadata": map[string]interface{}{
							"name":      "someName",
							"namespace": "someNamespace",
						},
					},
				}

				envVars = []corev1.EnvVar{
					{Name: "env1", Value: "value1"},
					{Name: "env2", Value: "value2"},
				}
			})

			It("returns the appropriate container", func() {
				container := factory.StatusWriterContainer(obj, envVars)

				Expect(container).ToNot(BeNil())
				Expect(container.Name).To(Equal("status-writer"))
				Expect(container.Image).To(Equal(workCreatorImage))
				Expect(container.Command).To(Equal([]string{"sh", "-c", "update-status"}))
				Expect(container.Env).To(ConsistOf(
					corev1.EnvVar{Name: "OBJECT_KIND", Value: obj.GroupVersionKind().Kind},
					corev1.EnvVar{Name: "OBJECT_GROUP", Value: obj.GroupVersionKind().Group},
					corev1.EnvVar{Name: "OBJECT_NAME", Value: obj.GetName()},
					corev1.EnvVar{Name: "OBJECT_NAMESPACE", Value: factory.Namespace},
					corev1.EnvVar{Name: "env1", Value: "value1"},
					corev1.EnvVar{Name: "env2", Value: "value2"},
				))
				Expect(container.VolumeMounts).To(ConsistOf(
					corev1.VolumeMount{Name: "shared-metadata", MountPath: "/work-creator-files/metadata"},
				))
			})
		})

		Describe("PipelineJob", func() {
			var (
				serviceAccount *corev1.ServiceAccount
				configMap      *corev1.ConfigMap
				envVars        []corev1.EnvVar
			)
			BeforeEach(func() {
				var err error
				serviceAccount = factory.ServiceAccount()
				configMap, err = factory.ConfigMap(promise.GetWorkloadGroupScheduling())
				Expect(err).ToNot(HaveOccurred())
				envVars = []corev1.EnvVar{
					{Name: "env1", Value: "value1"},
					{Name: "env2", Value: "value2"},
				}
			})

			When("building a job for a promise pipeline", func() {
				When("building a job for the configure action", func() {
					It("returns a job with the appropriate spec", func() {
						job, err := factory.PipelineJob(configMap, serviceAccount, envVars)
						Expect(job).ToNot(BeNil())
						Expect(err).ToNot(HaveOccurred())

						Expect(job.GetName()).To(HavePrefix("kratix-%s-%s", promise.GetName(), pipeline.GetName()))
						Expect(job.GetNamespace()).To(Equal(factory.Namespace))
						for _, definedLabels := range []map[string]string{job.GetLabels(), job.Spec.Template.GetLabels()} {
							Expect(definedLabels).To(SatisfyAll(
								HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()),
								HaveKeyWithValue(v1alpha1.WorkTypeLabel, string(factory.WorkflowType)),
								HaveKeyWithValue(v1alpha1.WorkActionLabel, string(factory.WorkflowAction)),
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
						Expect(initContainerImages).To(Equal([]string{
							workCreatorImage,
							pipeline.Spec.Containers[0].Image,
							pipeline.Spec.Containers[1].Image,
							workCreatorImage,
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
					})
				})

				When("building a job for the delete action", func() {
					BeforeEach(func() {
						factory.WorkflowAction = v1alpha1.WorkflowActionDelete
					})

					It("returns a job with the appropriate spec", func() {
						job, err := factory.PipelineJob(configMap, serviceAccount, envVars)
						Expect(job).ToNot(BeNil())
						Expect(err).ToNot(HaveOccurred())

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
				BeforeEach(func() {
					factory.ResourceWorkflow = true
				})

				When("building a job for the configure action", func() {
					It("returns a job with the appropriate spec", func() {
						job, err := factory.PipelineJob(configMap, serviceAccount, envVars)
						Expect(job).ToNot(BeNil())
						Expect(err).ToNot(HaveOccurred())

						Expect(job.GetName()).To(HavePrefix("kratix-%s-%s-%s", promise.GetName(), resourceRequest.GetName(), pipeline.GetName()))
						Expect(job.GetNamespace()).To(Equal(factory.Namespace))
						for _, definedLabels := range []map[string]string{job.GetLabels(), job.Spec.Template.GetLabels()} {
							Expect(definedLabels).To(SatisfyAll(
								HaveKeyWithValue(v1alpha1.PromiseNameLabel, promise.GetName()),
								HaveKeyWithValue(v1alpha1.WorkTypeLabel, string(factory.WorkflowType)),
								HaveKeyWithValue(v1alpha1.WorkActionLabel, string(factory.WorkflowAction)),
								HaveKeyWithValue(v1alpha1.PipelineNameLabel, pipeline.GetName()),
								HaveKeyWithValue(v1alpha1.KratixResourceHashLabel, combinedHash(promiseHash(promise), resourceHash(resourceRequest))),
								HaveKeyWithValue(v1alpha1.ResourceNameLabel, resourceRequest.GetName()),
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
						Expect(initContainerImages).To(Equal([]string{
							workCreatorImage,
							pipeline.Spec.Containers[0].Image,
							pipeline.Spec.Containers[1].Image,
							workCreatorImage,
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
					})
				})

				When("building a job for the delete action", func() {
					BeforeEach(func() {
						factory.WorkflowAction = v1alpha1.WorkflowActionDelete
					})

					It("returns a job with the appropriate spec", func() {
						job, err := factory.PipelineJob(configMap, serviceAccount, envVars)
						Expect(job).ToNot(BeNil())
						Expect(err).ToNot(HaveOccurred())

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
		})
	})
})

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
