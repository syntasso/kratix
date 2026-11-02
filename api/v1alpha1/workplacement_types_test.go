package v1alpha1_test

import (
	"encoding/json"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

var _ = Describe("WorkPlacement", func() {
	var workPlacement *v1alpha1.WorkPlacement

	BeforeEach(func() {
		workPlacement = &v1alpha1.WorkPlacement{
			ObjectMeta: metav1.ObjectMeta{Generation: 4},
			Spec:       v1alpha1.WorkPlacementSpec{WorkGeneration: 7},
		}
	})

	Describe("IsWrittenAtCurrentGeneration", func() {
		It("is true when the current generation of the Work has been written", func() {
			workPlacement.SetWriteSucceededCondition()

			Expect(workPlacement.IsWrittenAtCurrentGeneration(7)).To(BeTrue())
		})

		It("is false when the WorkPlacement has changed since the last write", func() {
			workPlacement.SetWriteSucceededCondition()
			workPlacement.Generation = 5

			Expect(workPlacement.IsWrittenAtCurrentGeneration(7)).To(BeFalse())
		})

		It("is false when the write failed", func() {
			workPlacement.SetWriteFailedCondition(errors.New("bad credentials"))

			Expect(workPlacement.IsWrittenAtCurrentGeneration(7)).To(BeFalse())
		})
	})

	Describe("Status", func() {
		var statusWithCustomFields map[string]any

		BeforeEach(func() {
			statusWithCustomFields = map[string]any{"status": map[string]any{
				"versionID":   "abc123",
				"deliveryURL": "https://github.com/acme/platform/pull/41",
				"conditions": []any{
					map[string]any{"type": "Ready", "status": "True", "reason": "Ready", "message": "Ready", "lastTransitionTime": "2026-01-01T00:00:00Z"},
				},
				"pullRequest": map[string]any{"url": "https://github.com/acme/platform/pull/42", "generation": int64(4)},
			}}
		})

		It("keeps custom fields through JSON", func() {
			data, err := json.Marshal(statusWithCustomFields)
			Expect(err).NotTo(HaveOccurred())

			workPlacement := &v1alpha1.WorkPlacement{}
			Expect(json.Unmarshal(data, workPlacement)).To(Succeed())
			Expect(workPlacement.Status.VersionID).To(Equal("abc123"))
			Expect(workPlacement.Status.DeliveryURL).To(Equal("https://github.com/acme/platform/pull/41"))
			Expect(workPlacement.Status.Custom).NotTo(HaveKey("deliveryURL"))
			Expect(workPlacement.Status.Conditions).To(HaveLen(1))
			Expect(workPlacement.Status.Custom).To(HaveKey("pullRequest"))

			out, err := json.Marshal(workPlacement)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(ContainSubstring(`"pullRequest":{"generation":4,"url":"https://github.com/acme/platform/pull/42"}`))
			Expect(string(out)).To(ContainSubstring(`"versionID":"abc123"`))
			Expect(string(out)).To(ContainSubstring(`"deliveryURL":"https://github.com/acme/platform/pull/41"`))
		})

		It("keeps custom fields through the unstructured converter", func() {
			workPlacement := &v1alpha1.WorkPlacement{}
			Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(statusWithCustomFields, workPlacement)).To(Succeed())
			Expect(workPlacement.Status.Custom).To(HaveKey("pullRequest"))

			back, err := runtime.DefaultUnstructuredConverter.ToUnstructured(workPlacement)
			Expect(err).NotTo(HaveOccurred())
			Expect(back["status"]).To(HaveKey("pullRequest"))
		})

		It("keeps custom fields through DeepCopy", func() {
			workPlacement := &v1alpha1.WorkPlacement{}
			Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(statusWithCustomFields, workPlacement)).To(Succeed())

			copied := workPlacement.DeepCopy()
			data, err := json.Marshal(copied)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring(`"url":"https://github.com/acme/platform/pull/42"`))
		})

		It("lets Kratix-owned fields win over custom fields with the same name", func() {
			workPlacement := &v1alpha1.WorkPlacement{}
			workPlacement.Status.Custom = map[string]apiextensionsv1.JSON{
				"versionID": {Raw: []byte(`"from custom"`)},
			}
			workPlacement.Status.VersionID = "from kratix"

			data, err := json.Marshal(&workPlacement.Status)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring(`"versionID":"from kratix"`))
			Expect(string(data)).NotTo(ContainSubstring("from custom"))
		})
	})
})
