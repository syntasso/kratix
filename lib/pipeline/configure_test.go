package pipeline_test

import (
	"os"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/pipeline"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ = Describe("Configure Pipeline", func() {
	var (
		rr                *unstructured.Unstructured
		pipelines         []v1alpha1.Pipeline
		pipelineResources pipeline.PipelineArgs
		logger            logr.Logger
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

		pipelines = []v1alpha1.Pipeline{
			{
				Spec: v1alpha1.PipelineSpec{
					Containers: []v1alpha1.Container{
						{Name: "test-container", Image: "test-image"},
					},
				},
			},
		}
		logger = logr.Logger{}

		pipelineResources = pipeline.NewPipelineArgs("test-promise", "test-resource-request", "test-namespace")
	})

	Describe("Pipeline Request Hash", func() {
		const expectedHash = "9bb58f26192e4ba00f01e2e7b136bbd8"

		It("is included as a label to the pipeline job", func() {
			job, err := pipeline.ConfigurePipeline(rr, pipelines, pipelineResources, "test-promise", false, logger)
			Expect(err).NotTo(HaveOccurred())

			Expect(job.Labels).To(HaveKeyWithValue("kratix-resource-hash", expectedHash))
		})
	})

	Describe("optional workflow configs", func() {
		It("can include args and commands", func() {
			pipelines[0].Spec.Containers = append(pipelines[0].Spec.Containers, v1alpha1.Container{
				Name:    "another-container",
				Image:   "another-image",
				Args:    []string{"arg1", "arg2"},
				Command: []string{"command1", "command2"},
			})
			job, err := pipeline.ConfigurePipeline(rr, pipelines, pipelineResources, "test-promise", false, logger)
			Expect(err).NotTo(HaveOccurred())

			Expect(job.Spec.Template.Spec.InitContainers[1].Args).To(BeEmpty())
			Expect(job.Spec.Template.Spec.InitContainers[1].Command).To(BeEmpty())
			Expect(job.Spec.Template.Spec.InitContainers[2].Args).To(Equal([]string{"arg1", "arg2"}))
			Expect(job.Spec.Template.Spec.InitContainers[2].Command).To(Equal([]string{"command1", "command2"}))
		})

		It("can include env and envFrom", func() {
			pipelines[0].Spec.Containers = append(pipelines[0].Spec.Containers, v1alpha1.Container{
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
			job, err := pipeline.ConfigurePipeline(rr, pipelines, pipelineResources, "test-promise", false, logger)
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
			pipelines[0].Spec.Volumes = []corev1.Volume{
				{Name: "test-volume", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			}
			pipelines[0].Spec.Containers = append(pipelines[0].Spec.Containers, v1alpha1.Container{
				Name:  "another-container",
				Image: "another-image",
				VolumeMounts: []corev1.VolumeMount{
					{Name: "test-volume-mount", MountPath: "/test-mount-path"},
				},
			})
			job, err := pipeline.ConfigurePipeline(rr, pipelines, pipelineResources, "test-promise", false, logger)
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
			pipelines[0].Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "test-secret"}, {Name: "another-secret"}}
			pipelines[0].Spec.Containers = append(pipelines[0].Spec.Containers, v1alpha1.Container{
				Name:            "another-container",
				Image:           "another-image",
				ImagePullPolicy: corev1.PullAlways,
			})
			job, err := pipeline.ConfigurePipeline(rr, pipelines, pipelineResources, "test-promise", false, logger)
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
