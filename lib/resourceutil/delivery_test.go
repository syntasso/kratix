package resourceutil_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
})
