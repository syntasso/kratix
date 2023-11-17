package pipeline_test

import (
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/pipeline"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ = Describe("Configure Pipeline", func() {
	var (
		rr                *unstructured.Unstructured
		pipelines         []platformv1alpha1.Pipeline
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

		pipelines = []platformv1alpha1.Pipeline{
			{
				Spec: platformv1alpha1.PipelineSpec{
					Containers: []platformv1alpha1.Container{
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

	When("a container contains args and command", func() {
		It("is included in the pipeline job", func() {
			pipelines[0].Spec.Containers = append(pipelines[0].Spec.Containers, platformv1alpha1.Container{
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
	})
})
