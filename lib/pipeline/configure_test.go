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

	Describe("Pipeline Request Hash", func() {
		const expectedHash = "9bb58f26192e4ba00f01e2e7b136bbd8"

		It("is included as a label to the pipeline job", func() {
			logger := logr.Logger{}
			job, err := pipeline.ConfigurePipeline(rr, pipelines, pipelineResources, "test-promise", false, logger)
			Expect(err).NotTo(HaveOccurred())

			Expect(job.Labels).To(HaveKeyWithValue("kratix-resource-hash", expectedHash))
		})
	})
})
