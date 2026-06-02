package v1alpha1_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("ResourceBinding", func() {
	Describe("InFlightVersion", func() {
		var binding *platformv1alpha1.ResourceBinding

		BeforeEach(func() {
			binding = &platformv1alpha1.ResourceBinding{}
		})

		setCondition := func(status metav1.ConditionStatus, reason, message string) {
			binding.Status.Conditions = []metav1.Condition{
				{
					Type:    platformv1alpha1.UpgradeSucceededCondition,
					Status:  status,
					Reason:  reason,
					Message: message,
				},
			}
		}

		It("returns empty when no UpgradeSucceeded condition is present", func() {
			Expect(binding.InFlightVersion()).To(BeEmpty())
		})

		It("returns empty when the condition status is not Unknown", func() {
			setCondition(metav1.ConditionTrue, platformv1alpha1.UpgradeCompleteReason, "Upgrade to version v1.2.3 succeeded")
			Expect(binding.InFlightVersion()).To(BeEmpty())

			setCondition(metav1.ConditionFalse, platformv1alpha1.UpgradeFailedReason, "Upgrade to version v1.2.3 failed")
			Expect(binding.InFlightVersion()).To(BeEmpty())
		})

		It("returns empty when the reason is not UpgradeInProgress", func() {
			setCondition(metav1.ConditionUnknown, "SomethingElse", "Upgrade to version v1.2.3 is in progress")
			Expect(binding.InFlightVersion()).To(BeEmpty())
		})

		It("returns the version embedded in the upgrade-in-progress message", func() {
			setCondition(metav1.ConditionUnknown, platformv1alpha1.UpgradeInProgressReason, "Upgrade to version v1.2.3 is in progress")
			Expect(binding.InFlightVersion()).To(Equal("v1.2.3"))
		})

		It("returns the version embedded in the manual reconciliation message", func() {
			setCondition(metav1.ConditionUnknown, platformv1alpha1.UpgradeInProgressReason, "Reconciliation requested for version v0.0.2")
			Expect(binding.InFlightVersion()).To(Equal("v0.0.2"))
		})

		It("returns empty when the message has no version marker", func() {
			setCondition(metav1.ConditionUnknown, platformv1alpha1.UpgradeInProgressReason, "no marker here")
			Expect(binding.InFlightVersion()).To(BeEmpty())
		})
	})
})
