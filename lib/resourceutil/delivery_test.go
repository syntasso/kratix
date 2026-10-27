package resourceutil_test

import (
	"strings"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	"github.com/syntasso/kratix/lib/resourceutil"
)

var _ = Describe("DeliveryEntries", func() {
	placement := func(name, destination, reason, message string) v1alpha1.WorkPlacementWriteStatus {
		return v1alpha1.WorkPlacementWriteStatus{Name: name, Destination: destination, Reason: reason, Message: message}
	}

	It("returns the delivery WorkPlacements by name", func() {
		works := []v1alpha1.Work{
			{Status: v1alpha1.WorkStatus{WorkPlacementStatuses: []v1alpha1.WorkPlacementWriteStatus{
				placement("work-b-z", "dev", v1alpha1.WorkloadsRejectedReason, "rejected"),
				placement("work-b-failed", "test", "WorkloadsFailedWrite", "authentication required"),
				placement("work-b-written", "test", "WorkloadsWrittenToStateStore", "Workloads written to State Store"),
			}}},
			{Status: v1alpha1.WorkStatus{WorkPlacementStatuses: []v1alpha1.WorkPlacementWriteStatus{
				placement("work-a-a", "prod", v1alpha1.WorkloadsAwaitingDeliveryReason, "waiting"),
			}}},
		}

		Expect(resourceutil.DeliveryEntries(works)).To(Equal([]resourceutil.DeliveryEntry{
			{WorkPlacement: "work-a-a", Destination: "prod", Reason: v1alpha1.WorkloadsAwaitingDeliveryReason, Message: "waiting"},
			{WorkPlacement: "work-b-z", Destination: "dev", Reason: v1alpha1.WorkloadsRejectedReason, Message: "rejected"},
		}))
	})

	It("returns nothing for a Work with no recorded write state", func() {
		Expect(resourceutil.DeliveryEntries([]v1alpha1.Work{{}})).To(BeEmpty())
	})
})

var _ = Describe("IsOrdinaryWorksFailure", func() {
	work := func(readyReason, readyMessage string, writeStatus metav1.ConditionStatus, writeReason string, placements ...v1alpha1.WorkPlacementWriteStatus) v1alpha1.Work {
		w := v1alpha1.Work{Status: v1alpha1.WorkStatus{
			Conditions:            []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: readyReason, Message: readyMessage}},
			WorkPlacementStatuses: placements,
		}}
		if writeReason != "" {
			w.Status.Conditions = append(w.Status.Conditions, metav1.Condition{
				Type: v1alpha1.WriteSucceededConditionType, Status: writeStatus, Reason: writeReason,
			})
		}
		return w
	}
	rejected := v1alpha1.WorkPlacementWriteStatus{Name: "wp-r", Destination: "prod", Status: "False",
		Reason: v1alpha1.WorkloadsRejectedReason, Message: "Rejected"}

	It("is true when a Work failed to write", func() {
		works := []v1alpha1.Work{
			work("WorkplacementsFailing", "Failing", metav1.ConditionFalse, v1alpha1.WorkplacementsFailingReason),
			work("WorkplacementsFailing", "Failing", metav1.ConditionFalse, v1alpha1.WorkplacementsRejectedReason, rejected),
		}
		Expect(resourceutil.IsOrdinaryWorksFailure(works)).To(BeTrue())
	})

	It("ignores Ready", func() {
		works := []v1alpha1.Work{work("Misplaced", "Misplaced", metav1.ConditionFalse, v1alpha1.WorkplacementsFailingReason)}
		Expect(resourceutil.IsOrdinaryWorksFailure(works)).To(BeTrue())
	})

	It("is false when a failing Work has no write state", func() {
		works := []v1alpha1.Work{work("WorkplacementsFailing", "Failing", "", "")}
		Expect(resourceutil.IsOrdinaryWorksFailure(works)).To(BeFalse())
	})

	It("is false when the only failing Work was rejected", func() {
		works := []v1alpha1.Work{work("WorkplacementsFailing", "Failing", metav1.ConditionFalse, v1alpha1.WorkplacementsRejectedReason, rejected)}
		Expect(resourceutil.IsOrdinaryWorksFailure(works)).To(BeFalse())
	})
})

var _ = Describe("delivery text on the resource request's conditions", func() {
	var rr *unstructured.Unstructured

	BeforeEach(func() {
		rr = &unstructured.Unstructured{Object: map[string]interface{}{}}
	})

	reconciled := func() *clusterv1.Condition {
		GinkgoHelper()
		condition := resourceutil.GetCondition(rr, resourceutil.ReconciledCondition)
		Expect(condition).NotTo(BeNil())
		return condition
	}

	Describe("WorksSucceeded", func() {
		It("cuts the message to 4096 characters", func() {
			var entries []resourceutil.DeliveryEntry
			for range 6 {
				entries = append(entries, resourceutil.DeliveryEntry{
					WorkPlacement: "wp", Destination: "dev", Reason: v1alpha1.WorkloadsRejectedReason,
					Message: strings.Repeat("é", 1000),
				})
			}

			resourceutil.MarkResourceRequestAsWorksFailed(rr, []string{"my-work"}, entries)

			message := resourceutil.GetCondition(rr, resourceutil.WorksSucceededCondition).Message
			Expect(utf8.ValidString(message)).To(BeTrue())
			Expect(utf8.RuneCountInString(message)).To(Equal(4096))
			Expect(message).To(HavePrefix("Some works associated with this resource failed: [my-work]. Destination dev: é"))
			Expect(message).To(HaveSuffix("…"))
		})
	})

	Describe("Reconciled", func() {
		It("keeps a 120-character text whole", func() {
			text := strings.Repeat("a", 120)
			resourceutil.MarkReconciledWorksPending(rr, []resourceutil.DeliveryEntry{
				{WorkPlacement: "wp", Reason: v1alpha1.WorkloadsAwaitingDeliveryReason, Message: text},
			})

			Expect(reconciled().Message).To(Equal(text))
		})

		It("counts characters, not bytes", func() {
			text := strings.Repeat("€", 60)
			Expect(len(text)).To(BeNumerically(">", 120))
			resourceutil.MarkReconciledWorksPending(rr, []resourceutil.DeliveryEntry{
				{WorkPlacement: "wp", Reason: v1alpha1.WorkloadsAwaitingDeliveryReason, Message: text},
			})

			Expect(reconciled().Message).To(Equal(text))
		})

		Describe("when Works are failing", func() {
			entries := []resourceutil.DeliveryEntry{
				{WorkPlacement: "a-waiting", Reason: v1alpha1.WorkloadsAwaitingDeliveryReason, Message: "Waiting for approval: a"},
				{WorkPlacement: "b-rejected", Reason: v1alpha1.WorkloadsRejectedReason, Message: "Rejected in b with no comment"},
			}

			It("prefers the rejection over a waiting WorkPlacement", func() {
				resourceutil.MarkReconciledWorksFailing(rr, entries, false)

				Expect(string(reconciled().Status)).To(Equal("False"))
				Expect(reconciled().Reason).To(Equal("WorksRejected"))
				Expect(reconciled().Message).To(Equal("Rejected in b with no comment"))
			})

			It("shows Failing for an ordinary failure", func() {
				resourceutil.MarkReconciledWorksFailing(rr, entries, true)

				Expect(reconciled().Reason).To(Equal("WorksFailing"))
				Expect(reconciled().Message).To(Equal("Failing"))
			})

			It("shows the waiting text when nothing was rejected", func() {
				resourceutil.MarkReconciledWorksFailing(rr, entries[:1], false)

				Expect(string(reconciled().Status)).To(Equal("Unknown"))
				Expect(reconciled().Reason).To(Equal("WorksAwaitingDelivery"))
				Expect(reconciled().Message).To(Equal("Waiting for approval: a"))
			})

			It("shows Failing when there is no delivery text", func() {
				resourceutil.MarkReconciledWorksFailing(rr, nil, false)

				Expect(reconciled().Reason).To(Equal("WorksFailing"))
			})
		})
	})
})
