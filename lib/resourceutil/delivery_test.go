package resourceutil_test

import (
	"context"
	"strings"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/syntasso/kratix/lib/resourceutil"
)

var _ = Describe("ListDeliveryEntries", func() {
	var (
		ctx       context.Context
		k8sClient client.Client
		work      v1alpha1.Work
	)

	placement := func(namespace, name, workName, destination, reason, message string) *v1alpha1.WorkPlacement {
		return &v1alpha1.WorkPlacement{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{v1alpha1.WorkLabel: workName},
			},
			Spec: v1alpha1.WorkPlacementSpec{TargetDestinationName: destination},
			Status: v1alpha1.WorkPlacementStatus{Conditions: []metav1.Condition{{
				Type:    v1alpha1.WriteSucceededConditionType,
				Status:  metav1.ConditionFalse,
				Reason:  reason,
				Message: message,
			}}},
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		scheme := runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		work = v1alpha1.Work{ObjectMeta: metav1.ObjectMeta{Name: "my-work", Namespace: "pipelines"}}

		k8sClient = fake.NewClientBuilder().WithScheme(scheme).WithObjects(
			placement("pipelines", "my-work-z", "my-work", "dev", v1alpha1.WorkloadsRejectedReason, "rejected"),
			placement("pipelines", "my-work-a", "my-work", "prod", v1alpha1.WorkloadsAwaitingDeliveryReason, "waiting"),
			placement("pipelines", "my-work-failed", "my-work", "test", "WorkloadsFailedWrite", "authentication required"),
			placement("pipelines", "other-work-a", "other-work", "prod", v1alpha1.WorkloadsAwaitingDeliveryReason, "other work"),
			placement("default", "my-work-elsewhere", "my-work", "prod", v1alpha1.WorkloadsAwaitingDeliveryReason, "wrong namespace"),
		).Build()
	})

	It("returns the delivery WorkPlacements of the Works, in their own namespace, by name", func() {
		entries, err := resourceutil.ListDeliveryEntries(ctx, k8sClient, []v1alpha1.Work{work})
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(Equal([]resourceutil.DeliveryEntry{
			{WorkPlacement: "my-work-a", Destination: "prod", Reason: v1alpha1.WorkloadsAwaitingDeliveryReason, Message: "waiting"},
			{WorkPlacement: "my-work-z", Destination: "dev", Reason: v1alpha1.WorkloadsRejectedReason, Message: "rejected"},
		}))
	})

	It("skips a WorkPlacement that is being deleted", func() {
		deleting := placement("pipelines", "my-work-deleting", "my-work", "dev", v1alpha1.WorkloadsRejectedReason, "rejected")
		deleting.Finalizers = []string{"kratix.io/test"}
		deleting.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
		scheme := runtime.NewScheme()
		Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
		k8sClient = fake.NewClientBuilder().WithScheme(scheme).WithObjects(deleting).Build()

		entries, err := resourceutil.ListDeliveryEntries(ctx, k8sClient, []v1alpha1.Work{work})
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
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
		It("cuts the whole message to 4096 characters, keeping the Work names", func() {
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

		It("counts characters, not bytes, so a short text of wide characters stays whole", func() {
			text := strings.Repeat("€", 60)
			Expect(len(text)).To(BeNumerically(">", 120))
			resourceutil.MarkReconciledWorksPending(rr, []resourceutil.DeliveryEntry{
				{WorkPlacement: "wp", Reason: v1alpha1.WorkloadsAwaitingDeliveryReason, Message: text},
			})

			Expect(reconciled().Message).To(Equal(text))
		})

		It("shows the rejection, not a WorkPlacement still waiting that sorts first", func() {
			resourceutil.MarkReconciledWorksFailing(rr, []resourceutil.DeliveryEntry{
				{WorkPlacement: "a-waiting", Reason: v1alpha1.WorkloadsAwaitingDeliveryReason, Message: "Waiting for approval: a"},
				{WorkPlacement: "b-rejected", Reason: v1alpha1.WorkloadsRejectedReason, Message: "Rejected in b with no comment"},
			})

			Expect(reconciled().Reason).To(Equal("WorksRejected"))
			Expect(reconciled().Message).To(Equal("Rejected in b with no comment"))
		})
	})
})
