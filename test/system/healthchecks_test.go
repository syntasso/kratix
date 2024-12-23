package system_test

import (
	"context"
	"fmt"
	"github.com/syntasso/kratix/lib/compression"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"time"

	"github.com/onsi/ginkgo/v2/types"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

		createDestination(bashPromiseName, dstLabels)

		healthPipeline := v1alpha1.Pipeline{
			ObjectMeta: metav1.ObjectMeta{Name: healthPipelineName},
			Spec: v1alpha1.PipelineSpec{
				Containers: []v1alpha1.Container{{Name: "a-container-name", Image: "test-image:latest"}},
			},
		}
		healthObjMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&healthPipeline)
		Expect(err).NotTo(HaveOccurred())
		unstructuredHealthPipeline := &unstructured.Unstructured{Object: healthObjMap}
		unstructuredHealthPipeline.SetAPIVersion("platform.kratix.io/v1alpha1")
		unstructuredHealthPipeline.SetKind("Pipeline")

		bashPromise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{
			{MatchLabels: dstLabels},
		}
		bashPromise.Spec.HealthChecks = &v1alpha1.HealthChecks{
			Resource: &v1alpha1.HealthCheckDefinition{
				Schedule: "5m",
				Workflow: unstructuredHealthPipeline,
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

	Describe("Promise with HealthChecks", func() {
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

			By("creating work with health definition", func() {
				parsedSelector, err := labels.Parse(healthPipelineLabels)
				Expect(err).NotTo(HaveOccurred())
				var works v1alpha1.WorkList
				err = k8sClient.List(context.TODO(), &works, &client.ListOptions{
					LabelSelector: parsedSelector,
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(works.Items).To(HaveLen(1))
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
			StateStoreRef: &v1alpha1.StateStoreReference{
				Kind: "BucketStateStore", Name: "default",
			},
		},
	}))
}
