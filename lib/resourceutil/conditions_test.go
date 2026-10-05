package resourceutil_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	"github.com/syntasso/kratix/lib/resourceutil"
)

var _ = Describe("SetConditionKeepingTransitionTime", func() {
	var (
		obj       *unstructured.Unstructured
		anHourAgo metav1.Time
		condition clusterv1.Condition
	)

	BeforeEach(func() {
		anHourAgo = metav1.NewTime(time.Now().Add(-time.Hour).UTC().Truncate(time.Second))
		obj = &unstructured.Unstructured{Object: map[string]any{}}
		condition = clusterv1.Condition{
			Type:    resourceutil.HealthChecksSucceededCondition,
			Status:  v1.ConditionTrue,
			Reason:  resourceutil.HealthChecksAllRecordsHealthyReason,
			Message: "2 of 2 records have reported at v2.0.0",
		}
	})

	It("adds a missing condition with a transition time of now", func() {
		before := time.Now().Add(-time.Second)
		Expect(resourceutil.SetConditionKeepingTransitionTime(obj, &condition)).To(BeTrue())

		written := resourceutil.GetCondition(obj, condition.Type)
		Expect(written).NotTo(BeNil())
		Expect(written.Status).To(Equal(v1.ConditionTrue))
		Expect(written.Message).To(Equal("2 of 2 records have reported at v2.0.0"))
		Expect(written.LastTransitionTime.Time).To(BeTemporally(">=", before.Truncate(time.Second)))
	})

	When("the condition already exists", func() {
		BeforeEach(func() {
			existing := condition
			existing.LastTransitionTime = anHourAgo
			resourceutil.SetCondition(obj, &existing)
			Expect(resourceutil.GetCondition(obj, condition.Type).LastTransitionTime.Time).To(BeTemporally("==", anHourAgo.Time))
		})

		It("does not write when status, reason and message are unchanged", func() {
			Expect(resourceutil.SetConditionKeepingTransitionTime(obj, &condition)).To(BeFalse())
			Expect(resourceutil.GetCondition(obj, condition.Type).LastTransitionTime.Time).To(BeTemporally("==", anHourAgo.Time))
		})

		It("keeps the transition time when only the message changes", func() {
			condition.Message = "3 of 2 records have reported at v2.0.0"
			Expect(resourceutil.SetConditionKeepingTransitionTime(obj, &condition)).To(BeTrue())

			written := resourceutil.GetCondition(obj, condition.Type)
			Expect(written.Message).To(Equal("3 of 2 records have reported at v2.0.0"))
			Expect(written.LastTransitionTime.Time).To(BeTemporally("==", anHourAgo.Time))
		})

		It("keeps the transition time when only the reason changes", func() {
			condition.Reason = "AnotherReason"
			Expect(resourceutil.SetConditionKeepingTransitionTime(obj, &condition)).To(BeTrue())

			written := resourceutil.GetCondition(obj, condition.Type)
			Expect(written.Reason).To(Equal("AnotherReason"))
			Expect(written.LastTransitionTime.Time).To(BeTemporally("==", anHourAgo.Time))
		})

		It("moves the transition time when the status changes", func() {
			condition.Status = v1.ConditionFalse
			condition.Reason = resourceutil.HealthChecksUnhealthyReason
			Expect(resourceutil.SetConditionKeepingTransitionTime(obj, &condition)).To(BeTrue())

			written := resourceutil.GetCondition(obj, condition.Type)
			Expect(written.Status).To(Equal(v1.ConditionFalse))
			Expect(written.LastTransitionTime.Time).To(BeTemporally(">", anHourAgo.Time))
		})

		It("leaves other conditions in place", func() {
			other := &clusterv1.Condition{Type: resourceutil.WorksSucceededCondition, Status: v1.ConditionTrue}
			resourceutil.SetCondition(obj, other)
			condition.Status = v1.ConditionFalse
			Expect(resourceutil.SetConditionKeepingTransitionTime(obj, &condition)).To(BeTrue())

			Expect(resourceutil.GetCondition(obj, resourceutil.WorksSucceededCondition)).NotTo(BeNil())
			Expect(resourceutil.GetCondition(obj, condition.Type).Status).To(Equal(v1.ConditionFalse))
		})
	})
})
