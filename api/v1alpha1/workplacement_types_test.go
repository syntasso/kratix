package v1alpha1_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("WorkPlacement", func() {
	var workPlacement *v1alpha1.WorkPlacement

	BeforeEach(func() {
		workPlacement = &v1alpha1.WorkPlacement{
			ObjectMeta: metav1.ObjectMeta{Generation: 4},
			Spec:       v1alpha1.WorkPlacementSpec{WorkGeneration: 7},
		}
	})

	Describe("SetWriteSucceededCondition", func() {
		It("records the generation that was written", func() {
			workPlacement.SetWriteSucceededCondition()

			writeSucceeded := meta.FindStatusCondition(workPlacement.Status.Conditions, v1alpha1.WriteSucceededConditionType)
			Expect(writeSucceeded.ObservedGeneration).To(Equal(int64(4)))
		})

		It("reports a change when a new generation is written", func() {
			workPlacement.SetWriteSucceededCondition()
			workPlacement.Generation = 5

			Expect(workPlacement.SetWriteSucceededCondition()).To(BeTrue())
		})
	})

	Describe("SetWriteFailedCondition", func() {
		It("records the generation that failed to write", func() {
			workPlacement.SetWriteFailedCondition(errors.New("bad credentials"))

			writeSucceeded := meta.FindStatusCondition(workPlacement.Status.Conditions, v1alpha1.WriteSucceededConditionType)
			Expect(writeSucceeded.ObservedGeneration).To(Equal(int64(4)))
		})
	})

	Describe("IsWrittenAtCurrentGeneration", func() {
		It("is true when the current generation of the Work has been written", func() {
			workPlacement.SetWriteSucceededCondition()

			Expect(workPlacement.IsWrittenAtCurrentGeneration(7)).To(BeTrue())
		})

		It("is false when the WorkPlacement holds an older generation of the Work", func() {
			workPlacement.SetWriteSucceededCondition()

			Expect(workPlacement.IsWrittenAtCurrentGeneration(8)).To(BeFalse())
		})

		It("is false when the last write was of an older WorkPlacement generation", func() {
			workPlacement.SetWriteSucceededCondition()
			workPlacement.Generation = 5

			Expect(workPlacement.IsWrittenAtCurrentGeneration(7)).To(BeFalse())
		})

		It("is false when the write failed", func() {
			workPlacement.SetWriteFailedCondition(errors.New("bad credentials"))

			Expect(workPlacement.IsWrittenAtCurrentGeneration(7)).To(BeFalse())
		})

		It("is false before anything has been written", func() {
			Expect(workPlacement.IsWrittenAtCurrentGeneration(7)).To(BeFalse())
		})
	})
})
