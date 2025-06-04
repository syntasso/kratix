package resourceutil_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("Work utilities", func() {
	var (
		fakeClient   client.Client
		namespace    string
		promiseName  string
		resourceName string
		ctx          context.Context
	)

	BeforeEach(func() {
		Expect(v1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
		fakeClient = clientfake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		namespace = v1alpha1.SystemNamespace
		promiseName = "test-promise"
		resourceName = "test-resource"
		ctx = context.TODO()
	})

	Describe("CalculateWorkflowStats", func() {
		Context("when no works exist", func() {
			It("returns zeros", func() {
				total, succeeded, failed, err := resourceutil.CalculateWorkflowStats(fakeClient, namespace, promiseName, resourceName)
				Expect(err).NotTo(HaveOccurred())
				Expect(total).To(Equal(0))
				Expect(succeeded).To(Equal(0))
				Expect(failed).To(Equal(0))
			})
		})

		Context("when works exist with various statuses", func() {
			BeforeEach(func() {
				works := []v1alpha1.Work{
					{
						TypeMeta: metav1.TypeMeta{APIVersion: "platform.kratix.io/v1alpha1", Kind: "Work"},
						ObjectMeta: metav1.ObjectMeta{
							Name:      "w1",
							Namespace: namespace,
							Labels: map[string]string{
								v1alpha1.PromiseNameLabel:  promiseName,
								v1alpha1.ResourceNameLabel: resourceName,
							},
						},
						Status: v1alpha1.WorkStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}},
					},
					{
						TypeMeta: metav1.TypeMeta{APIVersion: "platform.kratix.io/v1alpha1", Kind: "Work"},
						ObjectMeta: metav1.ObjectMeta{
							Name:      "w2",
							Namespace: namespace,
							Labels: map[string]string{
								v1alpha1.PromiseNameLabel:  promiseName,
								v1alpha1.ResourceNameLabel: resourceName,
							},
						},
						Status: v1alpha1.WorkStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse}}},
					},
				}
				for i := range works {
					Expect(fakeClient.Create(ctx, &works[i])).To(Succeed())
				}
			})

			It("returns counts of total, succeeded and failed", func() {
				total, succeeded, failed, err := resourceutil.CalculateWorkflowStats(fakeClient, namespace, promiseName, resourceName)
				Expect(err).NotTo(HaveOccurred())
				Expect(total).To(Equal(2))
				Expect(succeeded).To(Equal(1))
				Expect(failed).To(Equal(1))
			})
		})
	})

	Describe("AggregateWorkStatus", func() {
		var works []v1alpha1.Work

		JustBeforeEach(func() {
			for i := range works {
				works[i].TypeMeta = metav1.TypeMeta{APIVersion: "platform.kratix.io/v1alpha1", Kind: "Work"}
				works[i].ObjectMeta.Namespace = namespace
				works[i].ObjectMeta.Labels = map[string]string{
					v1alpha1.PromiseNameLabel:  promiseName,
					v1alpha1.ResourceNameLabel: resourceName,
				}
				Expect(fakeClient.Create(ctx, &works[i])).To(Succeed())
			}
		})

		Context("with no works", func() {
			BeforeEach(func() { works = []v1alpha1.Work{} })

			It("returns unknown status", func() {
				total, succeeded, failed, misplaced, status, err := resourceutil.AggregateWorkStatus(fakeClient, namespace, promiseName, resourceName)
				Expect(err).NotTo(HaveOccurred())
				Expect(total).To(Equal(0))
				Expect(succeeded).To(Equal(0))
				Expect(failed).To(Equal(0))
				Expect(misplaced).To(BeFalse())
				Expect(status).To(Equal(v1.ConditionUnknown))
			})
		})

		Context("with all works ready", func() {
			BeforeEach(func() {
				works = []v1alpha1.Work{
					{ObjectMeta: metav1.ObjectMeta{Name: "w1"}, Status: v1alpha1.WorkStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}}},
					{ObjectMeta: metav1.ObjectMeta{Name: "w2"}, Status: v1alpha1.WorkStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}}},
				}
			})

			It("returns all succeeded and condition true", func() {
				total, succeeded, failed, misplaced, status, err := resourceutil.AggregateWorkStatus(fakeClient, namespace, promiseName, resourceName)
				Expect(err).NotTo(HaveOccurred())
				Expect(total).To(Equal(2))
				Expect(succeeded).To(Equal(2))
				Expect(failed).To(Equal(0))
				Expect(misplaced).To(BeFalse())
				Expect(status).To(Equal(v1.ConditionTrue))
			})
		})

		Context("with failing and misplaced work", func() {
			BeforeEach(func() {
				works = []v1alpha1.Work{
					{ObjectMeta: metav1.ObjectMeta{Name: "w1"}, Status: v1alpha1.WorkStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}}},
					{ObjectMeta: metav1.ObjectMeta{Name: "w2"}, Status: v1alpha1.WorkStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Misplaced"}}}},
				}
			})

			It("returns failed count and misplaced true", func() {
				total, succeeded, failed, misplaced, status, err := resourceutil.AggregateWorkStatus(fakeClient, namespace, promiseName, resourceName)
				Expect(err).NotTo(HaveOccurred())
				Expect(total).To(Equal(2))
				Expect(succeeded).To(Equal(1))
				Expect(failed).To(Equal(1))
				Expect(misplaced).To(BeTrue())
				Expect(status).To(Equal(v1.ConditionFalse))
			})
		})

		Context("with unknown works", func() {
			BeforeEach(func() {
				works = []v1alpha1.Work{
					{ObjectMeta: metav1.ObjectMeta{Name: "w1"}, Status: v1alpha1.WorkStatus{Conditions: []metav1.Condition{}}},
					{ObjectMeta: metav1.ObjectMeta{Name: "w2"}, Status: v1alpha1.WorkStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}}},
				}
			})

			It("returns unknown condition status", func() {
				total, succeeded, failed, misplaced, status, err := resourceutil.AggregateWorkStatus(fakeClient, namespace, promiseName, resourceName)
				Expect(err).NotTo(HaveOccurred())
				Expect(total).To(Equal(2))
				Expect(succeeded).To(Equal(1))
				Expect(failed).To(Equal(0))
				Expect(misplaced).To(BeFalse())
				Expect(status).To(Equal(v1.ConditionUnknown))
			})
		})
	})
})
