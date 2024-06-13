package pipeline_test

import (
	"os"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/onsi/gomega/types"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/pipeline"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ = Describe("Configure Pipeline", func() {
	var (
		rr                *unstructured.Unstructured
		p                 v1alpha1.Pipeline
		pipelineResources pipeline.PipelineArgs
		logger            logr.Logger
		job               *batchv1.Job
		err               error
		labelsMatcher     types.GomegaMatcher
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
			ObjectMeta: metav1.ObjectMeta{
				Name: "configure-step",
			},
			Spec: v1alpha1.PipelineSpec{
				Containers: []v1alpha1.Container{
					{Name: "test-container", Image: "test-image"},
				},
			},
		}
		logger = logr.Logger{}

		pipelineResources = pipeline.NewPipelineArgs("test-promise", "", "configure-step", "test-name", "test-namespace")
	})

	Describe("Promise Configure Pipeline", func() {
		const expectedHash = "9bb58f26192e4ba00f01e2e7b136bbd8"
		BeforeEach(func() {
			job, err = pipeline.ConfigurePipeline(rr, expectedHash, p, pipelineResources, "test-promise", false, logger)
			Expect(err).NotTo(HaveOccurred())

			labelsMatcher = MatchAllKeys(Keys{
				"kratix.io/hash":                  Equal(expectedHash),
				"kratix-workflow-action":          Equal("configure"),
				"kratix-workflow-pipeline-name":   Equal("configure-step"),
				"kratix.io/pipeline-name":         Equal("configure-step"),
				"kratix-workflow-type":            Equal("promise"),
				"kratix-workflow-kind":            Equal("pipeline.platform.kratix.io"),
				"kratix-workflow-promise-version": Equal("v1alpha1"),
				"kratix.io/work-type":             Equal("promise"),
				"kratix.io/promise-name":          Equal("test-promise"),
			})
		})

		It("creates a job with the expected metadata", func() {
			Expect(job.ObjectMeta).To(MatchFields(IgnoreExtras, Fields{
				"Name":      HavePrefix("kratix-test-promise-configure-step-"),
				"Namespace": Equal("test-namespace"),
				"Labels":    labelsMatcher,
			}))
		})

		Context("when the pipeline name would exceed the 63 character limit", func() {
			BeforeEach(func() {
				promise_identifier := "long-long-long-long-promise"
				pipeline_name := "also-very-verbose-pipeline"
				pipelineResources = pipeline.NewPipelineArgs(promise_identifier, "", pipeline_name, "test-name", "test-namespace")

				job, err = pipeline.ConfigurePipeline(rr, expectedHash, p, pipelineResources, "test-promise", false, logger)

				labelsMatcher = MatchAllKeys(Keys{
					"kratix.io/hash":                  Equal(expectedHash),
					"kratix-workflow-action":          Equal("configure"),
					"kratix-workflow-pipeline-name":   Equal(pipeline_name),
					"kratix.io/pipeline-name":         Equal(pipeline_name),
					"kratix-workflow-type":            Equal("promise"),
					"kratix-workflow-kind":            Equal("pipeline.platform.kratix.io"),
					"kratix-workflow-promise-version": Equal("v1alpha1"),
					"kratix.io/work-type":             Equal("promise"),
					"kratix.io/promise-name":          Equal(promise_identifier),
				})
			})

			It("concatenates the pipeline name to ensure it fits the 63 character limit", func() {
				Expect(job.ObjectMeta.Name).To(HaveLen(62))
				Expect(job.ObjectMeta).To(MatchFields(IgnoreExtras, Fields{
					"Name":      HavePrefix("kratix-long-long-long-long-promise-also-very-verbose-pip-"),
					"Namespace": Equal("test-namespace"),
					"Labels":    labelsMatcher,
				}))
			})
		})
	})

	Describe("Resource Configure Pipeline", func() {
		const expectedHash = "9bb58f26192e4ba00f01e2e7b136bbd8"
		BeforeEach(func() {
			pipelineResources = pipeline.NewPipelineArgs("test-promise", "test-promise-test-rr", "configure-step", "test-rr", "test-namespace")
			job, err = pipeline.ConfigurePipeline(rr, expectedHash, p, pipelineResources, "test-promise", false, logger)
			Expect(err).NotTo(HaveOccurred())

			labelsMatcher = MatchAllKeys(Keys{
				"kratix.io/hash":                     Equal(expectedHash),
				"kratix-workflow-action":             Equal("configure"),
				"kratix-workflow-pipeline-name":      Equal("configure-step"),
				"kratix.io/pipeline-name":            Equal("configure-step"),
				"kratix-workflow-type":               Equal("resource"),
				"kratix-workflow-kind":               Equal("pipeline.platform.kratix.io"),
				"kratix-workflow-promise-version":    Equal("v1alpha1"),
				"kratix.io/work-type":                Equal("resource"),
				"kratix.io/promise-name":             Equal("test-promise"),
				"kratix-promise-resource-request-id": Equal("test-promise-test-rr"),
				"kratix.io/resource-name":            Equal("test-rr"),
			})
		})

		It("creates a job with the expected metadata", func() {
			Expect(job.ObjectMeta).To(MatchFields(IgnoreExtras, Fields{
				"Name":      HavePrefix("kratix-test-promise-test-rr-configure-step-"),
				"Namespace": Equal("test-namespace"),
				"Labels":    labelsMatcher,
			}))
		})

		Context("when the pipeline name would exceed the 63 character limit", func() {
			BeforeEach(func() {
				promise_identifier := "long-long-long-long-promise"
				pipelineResources = pipeline.NewPipelineArgs(promise_identifier, "long-long-long-long-promise-test-resource", "configure-step", "test-resource", "test-namespace")

				job, err = pipeline.ConfigurePipeline(rr, expectedHash, p, pipelineResources, "test-promise", false, logger)

				labelsMatcher = MatchAllKeys(Keys{
					"kratix.io/hash":                     Equal(expectedHash),
					"kratix-workflow-action":             Equal("configure"),
					"kratix-workflow-pipeline-name":      Equal("configure-step"),
					"kratix.io/pipeline-name":            Equal("configure-step"),
					"kratix-workflow-type":               Equal("resource"),
					"kratix-workflow-kind":               Equal("pipeline.platform.kratix.io"),
					"kratix-workflow-promise-version":    Equal("v1alpha1"),
					"kratix.io/work-type":                Equal("resource"),
					"kratix.io/promise-name":             Equal(promise_identifier),
					"kratix-promise-resource-request-id": Equal("long-long-long-long-promise-test-resource"),
					"kratix.io/resource-name":            Equal("test-resource"),
				})
			})

			It("concatenates the pipeline name to ensure it fits the 63 character limit", func() {
				Expect(job.ObjectMeta.Name).To(HaveLen(62))
				Expect(job.ObjectMeta).To(MatchFields(IgnoreExtras, Fields{
					"Name":      HavePrefix("kratix-long-long-long-long-promise-test-resource-configu-"),
					"Namespace": Equal("test-namespace"),
					"Labels":    labelsMatcher,
				}))
			})
		})
	})

	Describe("WorkWriter", func() {
		When("its a promise", func() {
			It("runs the work-creator with the correct arguments", func() {
				p.Spec.Containers = append(p.Spec.Containers, v1alpha1.Container{
					Name:    "another-container",
					Image:   "another-image",
					Args:    []string{"arg1", "arg2"},
					Command: []string{"command1", "command2"},
				})
				job, err := pipeline.ConfigurePipeline(rr, "hash", p, pipelineResources, "test-promise", true, logger)
				Expect(err).NotTo(HaveOccurred())

				Expect(job.Spec.Template.Spec.InitContainers[3].Command).To(ConsistOf(
					"sh",
					"-c",
					"./work-creator -input-directory /work-creator-files -promise-name test-promise -pipeline-name configure-step -namespace kratix-platform-system -workflow-type promise",
				))
			})
		})

		When("its a resource request", func() {
			It("runs the work-creator with the correct arguments", func() {
				p.Spec.Containers = append(p.Spec.Containers, v1alpha1.Container{
					Name:    "another-container",
					Image:   "another-image",
					Args:    []string{"arg1", "arg2"},
					Command: []string{"command1", "command2"},
				})
				job, err := pipeline.ConfigurePipeline(rr, "hash", p, pipelineResources, "test-promise", false, logger)
				Expect(err).NotTo(HaveOccurred())

				Expect(job.Spec.Template.Spec.InitContainers[3].Command).To(ConsistOf(
					"sh",
					"-c",
					"./work-creator -input-directory /work-creator-files -promise-name test-promise -pipeline-name configure-step -namespace test-namespace -resource-name test-pod -workflow-type resource",
				))
			})
		})
	})

	Describe("optional workflow configs", func() {
		It("can include args and commands", func() {
			p.Spec.Containers = append(p.Spec.Containers, v1alpha1.Container{
				Name:    "another-container",
				Image:   "another-image",
				Args:    []string{"arg1", "arg2"},
				Command: []string{"command1", "command2"},
			})
			job, err := pipeline.ConfigurePipeline(rr, "hash", p, pipelineResources, "test-promise", false, logger)
			Expect(err).NotTo(HaveOccurred())

			Expect(job.Spec.Template.Spec.InitContainers[1].Args).To(BeEmpty())
			Expect(job.Spec.Template.Spec.InitContainers[1].Command).To(BeEmpty())
			Expect(job.Spec.Template.Spec.InitContainers[2].Args).To(Equal([]string{"arg1", "arg2"}))
			Expect(job.Spec.Template.Spec.InitContainers[2].Command).To(Equal([]string{"command1", "command2"}))
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
			job, err := pipeline.ConfigurePipeline(rr, "hash", p, pipelineResources, "test-promise", false, logger)
			Expect(err).NotTo(HaveOccurred())

			Expect(job.Spec.Template.Spec.InitContainers[1].Env).To(ContainElements(
				corev1.EnvVar{Name: "KRATIX_WORKFLOW_ACTION", Value: "configure"},
				corev1.EnvVar{Name: "KRATIX_WORKFLOW_TYPE", Value: "resource"},
			))
			Expect(job.Spec.Template.Spec.InitContainers[2].Env).To(ContainElements(
				corev1.EnvVar{Name: "KRATIX_WORKFLOW_ACTION", Value: "configure"},
				corev1.EnvVar{Name: "KRATIX_WORKFLOW_TYPE", Value: "resource"},
				corev1.EnvVar{Name: "env1", Value: "value1"},
			))

			Expect(job.Spec.Template.Spec.InitContainers[1].EnvFrom).To(BeNil())
			Expect(job.Spec.Template.Spec.InitContainers[2].EnvFrom).To(ContainElements(
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
			job, err := pipeline.ConfigurePipeline(rr, "hash", p, pipelineResources, "test-promise", false, logger)
			Expect(err).NotTo(HaveOccurred())

			Expect(job.Spec.Template.Spec.InitContainers[1].VolumeMounts).To(HaveLen(3), "default volume mounts should've been included")
			Expect(job.Spec.Template.Spec.InitContainers[1].Command).To(BeEmpty())
			Expect(job.Spec.Template.Spec.InitContainers[2].VolumeMounts).To(ContainElement(
				corev1.VolumeMount{Name: "test-volume-mount", MountPath: "/test-mount-path"},
			))
			Expect(job.Spec.Template.Spec.Volumes).To(ContainElement(
				corev1.Volume{Name: "test-volume", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			))
		})

		It("can include imagePullPolicy and imagePullSecrets", func() {
			os.Setenv("WC_PULL_SECRET", "registry-secret")
			p.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "test-secret"}, {Name: "another-secret"}}
			p.Spec.Containers = append(p.Spec.Containers, v1alpha1.Container{
				Name:            "another-container",
				Image:           "another-image",
				ImagePullPolicy: corev1.PullAlways,
			})
			job, err := pipeline.ConfigurePipeline(rr, "hash", p, pipelineResources, "test-promise", false, logger)
			Expect(err).NotTo(HaveOccurred())

			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(HaveLen(3), "imagePullSecrets should've been included")
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(ContainElements(
				corev1.LocalObjectReference{Name: "registry-secret"},
				corev1.LocalObjectReference{Name: "test-secret"},
				corev1.LocalObjectReference{Name: "another-secret"},
			), "imagePullSecrets should've been included")
			Expect(job.Spec.Template.Spec.InitContainers[1].ImagePullPolicy).To(BeEmpty())
			Expect(job.Spec.Template.Spec.InitContainers[2].ImagePullPolicy).To(Equal(corev1.PullAlways))
		})
	})
})
