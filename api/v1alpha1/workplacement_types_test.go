package v1alpha1_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
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
})
