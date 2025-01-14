package system_test

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/onsi/ginkgo/v2/types"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

var _ = Describe("Kratix Healthcheck", func() {
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

		bashPromise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{
			{MatchLabels: dstLabels},
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
					LastRun:     time.Now().Unix(),
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
