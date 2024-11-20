package migrations_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/migrations"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Conditions", func() {
	When("The condition is present", func() {
		It("should remove deprecated condition", func() {
			gvk := v1alpha1.GroupVersion.Group + "/" + v1alpha1.GroupVersion.Version
			promise := &v1alpha1.Promise{
				TypeMeta: metav1.TypeMeta{
					APIVersion: gvk,
					Kind:       "Promise",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-promise",
				},
				Status: v1alpha1.PromiseStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "PipelineCompleted",
							Status: metav1.ConditionTrue,
						},
						{
							Type:   "ConfigureWorkflowCompleted",
							Status: metav1.ConditionTrue,
						},
					},
				},
			}

			Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
			promise = &v1alpha1.Promise{}
			Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Name: "test-promise"}, promise)).To(Succeed())

			usPromise, err := promise.ToUnstructured()
			Expect(err).ToNot(HaveOccurred())
			usPromise.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Promise"))

			requeue, err := migrations.RemoveDeprecatedConditions(ctx, fakeK8sClient, usPromise, logger)
			Expect(err).NotTo(HaveOccurred())
			Expect(requeue).NotTo(BeNil())
			Expect(requeue.Requeue).To(BeTrue())

			promise = &v1alpha1.Promise{}
			Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Name: "test-promise"}, promise)).To(Succeed())
			Expect(promise.Status.Conditions).To(HaveLen(1))
			Expect(promise.Status.Conditions[0].Type).To(Equal("ConfigureWorkflowCompleted"))
		})
	})

	When("The condition is not present", func() {
		It("should no-op", func() {

			gvk := v1alpha1.GroupVersion.Group + "/" + v1alpha1.GroupVersion.Version
			promise := &v1alpha1.Promise{
				TypeMeta: metav1.TypeMeta{
					APIVersion: gvk,
					Kind:       "Promise",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-promise",
				},
				Status: v1alpha1.PromiseStatus{
					Conditions: []metav1.Condition{
						{
							Type:   "ConfigureWorkflowCompleted",
							Status: metav1.ConditionTrue,
						},
					},
				},
			}

			Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
			promise = &v1alpha1.Promise{}
			Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Name: "test-promise"}, promise)).To(Succeed())

			usPromise, err := promise.ToUnstructured()
			Expect(err).ToNot(HaveOccurred())
			usPromise.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Promise"))

			requeue, err := migrations.RemoveDeprecatedConditions(ctx, fakeK8sClient, usPromise, logger)
			Expect(err).NotTo(HaveOccurred())
			Expect(requeue).To(BeNil())
			promise = &v1alpha1.Promise{}
			Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Name: "test-promise"}, promise)).To(Succeed())
			Expect(promise.Status.Conditions).To(HaveLen(1))
			Expect(promise.Status.Conditions[0].Type).To(Equal("ConfigureWorkflowCompleted"))
		})
	})

})
