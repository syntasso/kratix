package pipeline_test

import (
	. "github.com/onsi/ginkgo"
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

		pipelineResources = pipeline.NewPipelineArgs("test-promise", "test-resource-request", "test-namespace")
	})

	Describe("Pipeline Labels", func() {
		It("includes all labels", func() {
			job, err := pipeline.ConfigurePipeline(rr, pipelines, pipelineResources, "test-promise")
			Expect(err).NotTo(HaveOccurred())

			Expect(job.Labels).To(HaveKeyWithValue("kratix-promise-id", "test-promise"))
			Expect(job.Labels).To(HaveKeyWithValue("kratix-promise-resource-request-id", "test-resource-request"))
			Expect(job.Labels).To(HaveKeyWithValue("kratix-pipeline-type", "configure"))
		})
	})
})
