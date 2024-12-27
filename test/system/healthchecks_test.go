package system_test

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/syntasso/kratix/lib/compression"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onsi/ginkgo/v2/types"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

var _ = Describe("Kratix Healthcheck", func() {
	const healthPipelineName = "bash-check"
	var (
		rrName    string
		dstLabels = map[string]string{"env": "healthchecks-test"}
	)

	BeforeEach(func() {
		var err error

		bashPromise = generateUniquePromise(promisePath)
		bashPromiseName = bashPromise.Name
		dstLabels["promise"] = bashPromiseName
		createDestination(bashPromiseName, dstLabels)

		uHealthPipeline := pipelineToUnstructured(v1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{Name: healthPipelineName},
			Spec: v1alpha1.PipelineSpec{
				Containers: []v1alpha1.Container{{Name: "a-container-name", Image: "test-image:latest"}},
			},
		})

		bashPromise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{
			{MatchLabels: dstLabels},
		}
		bashPromise.Spec.HealthChecks = &v1alpha1.HealthChecks{
			Resource: &v1alpha1.HealthCheckDefinition{
				Schedule: "5m",
				Workflow: uHealthPipeline,
			},
		}

		platform.eventuallyKubectl("apply", "-f", cat(bashPromise))

		_, crd, err = bashPromise.GetAPI()
		Expect(err).NotTo(HaveOccurred())
		platform.eventuallyKubectl("get", "crd", crd.Name)

		rrName = bashPromiseName + "with-health"
		platform.kubectl("apply", "-f", requestWithNameAndCommand(rrName, "echo 'hello world'"))

		platform.eventuallyKubectl("wait", "--for=condition=ConfigureWorkflowCompleted", bashPromiseName, rrName, pipelineTimeout)
	})

	AfterEach(func() {
		if CurrentSpecReport().State.Is(types.SpecStatePassed) {
			platform.eventuallyKubectlDelete("promise", bashPromiseName)
			platform.eventuallyKubectlDelete("destination", bashPromiseName)
		}
	})

	Describe("Requesting a resource", func() {
		It("creates an addition health workflow at resource request", func() {
			healthPipelineLabels := fmt.Sprintf(
				"kratix.io/promise-name=%s,kratix.io/resource-name=%s,kratix.io/pipeline-name=%s,kratix.io/work-type=%s",
				bashPromiseName,
				rrName,
				healthPipelineName,
				"resource",
			)

			By("executing the healthcheck workflow", func() {
				Eventually(func() string {
					return platform.eventuallyKubectl("get", "pods", "--selector", healthPipelineLabels)
				}, timeout, interval).Should(ContainSubstring("Completed"))
			})

			By("respecting the destinationSelectors defined in the promise", func() {
				var wpList v1alpha1.WorkPlacementList
				Eventually(func(g Gomega) {
					wpSelector := "kratix.io/pipeline-name=bash-check"
					wpJson := platform.kubectl("get", "workplacements", "--selector", wpSelector, "-o", "json")
					err := json.Unmarshal([]byte(wpJson), &wpList)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(wpList.Items).To(HaveLen(1))
				}, timeout, interval).Should(Succeed())

				Expect(wpList.Items[0].Spec.TargetDestinationName).To(Equal(bashPromiseName))
			})

			By("creating work with health definition", func() {
				var works v1alpha1.WorkList

				Eventually(func(g Gomega) {
					worksJson := platform.kubectl("get", "works", "-o", "json", "-A", "--selector", healthPipelineLabels)
					err := json.Unmarshal([]byte(worksJson), &works)
					g.Expect(err).NotTo(HaveOccurred())
					g.Expect(works.Items).To(HaveLen(1))
				}, timeout, interval).Should(Succeed())

				work := works.Items[0]
				Expect(work.Spec.WorkloadGroups).To(HaveLen(1))
				wGroup := work.Spec.WorkloadGroups[0]
				Expect(wGroup.Workloads[0].Filepath).To(Equal("healthdefinition.yaml"))
				Expect(compression.DecompressContent([]byte(wGroup.Workloads[0].Content))).To(SatisfyAll(
					ContainSubstring("schedule: 5m"),
					ContainSubstring("name: a-container-name"),
					ContainSubstring("image: test-image:latest"),
				))
			})
		})
	})

	Describe("Resource with HealthRecord", func() {
		var healthRecord *v1alpha1.HealthRecord

		BeforeEach(func() {
			healthRecord = &v1alpha1.HealthRecord{
				TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "HealthRecord"},
				ObjectMeta: metav1.ObjectMeta{Name: rrName, Namespace: "default"},
				Data: v1alpha1.HealthRecordData{
					PromiseRef:  v1alpha1.PromiseRef{Name: bashPromiseName},
					ResourceRef: v1alpha1.ResourceRef{Name: rrName, Namespace: "default", Generation: 1},
					State:       "healthy",
					LastRun:     fmt.Sprintf("%d", time.Now().Unix()),
					Details: &runtime.RawExtension{
						Raw: []byte(`{"info":"message", "errors": [{"message": "error"}]}`),
					},
				},
			}
		})

		AfterEach(func() {
			if CurrentSpecReport().State.Is(types.SpecStatePassed) {
				platform.eventuallyKubectlDelete("healthrecord", rrName)
			}
		})

		It("updates the resource status healthRecord with the HealthRecord data", func() {
			platform.eventuallyKubectl("apply", "-f", cat(healthRecord))

			Eventually(func(g Gomega) {

				rawRR := platform.kubectl("get", "-o", "json", bashPromiseName, rrName)
				rr := &unstructured.Unstructured{}
				g.Expect(rr.UnmarshalJSON([]byte(rawRR))).To(Succeed())

				status, _, err := unstructured.NestedMap(rr.Object, "status")
				g.Expect(err).NotTo(HaveOccurred(), "status not found")

				actualRecord, _, err := unstructured.NestedMap(status, "healthRecord")
				g.Expect(err).NotTo(HaveOccurred(), "healthRecord not found")

				g.Expect(actualRecord).To(SatisfyAll(
					HaveKeyWithValue("state", "healthy"),
					HaveKeyWithValue("details", SatisfyAll(
						HaveKeyWithValue("info", "message"),
						HaveKeyWithValue("errors", SatisfyAll(
							ConsistOf(
								HaveKeyWithValue("message", "error"),
							),
						)),
					)),
				))
			}, timeout, interval).Should(Succeed())
		})
	})
})

func createDestination(name string, labels map[string]string) {
	platform.kubectl("apply", "-f", cat(&v1alpha1.Destination{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.GroupVersion.String(),
			Kind:       "Destination",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: v1alpha1.DestinationSpec{
			StrictMatchLabels: true,
			StateStoreRef: &v1alpha1.StateStoreReference{
				Kind: "BucketStateStore", Name: "default",
			},
		},
	}))
}

func pipelineToUnstructured(pipeline v1alpha1.Pipeline) *unstructured.Unstructured {
	healthObjMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pipeline)
	Expect(err).NotTo(HaveOccurred())
	uPipeline := &unstructured.Unstructured{Object: healthObjMap}
	uPipeline.SetAPIVersion("platform.kratix.io/v1alpha1")
	uPipeline.SetKind("Pipeline")
	return uPipeline
}
