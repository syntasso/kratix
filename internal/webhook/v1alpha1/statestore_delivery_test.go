package v1alpha1_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func describeDeliveryModeUnderAggregatedYAML[T client.Object](
	kind string,
	newStore func(name string, delivery *v1alpha1.Delivery) T,
	newValidator func(client.Client) admission.Validator[T],
) {
	Describe("setting a delivery mode", func() {
		const refusal = `delivery.mode cannot be set while Destination "a-aggregated" uses filepath.mode aggregatedYAML ` +
			`on this state store; see https://docs.kratix.io/main/reference/destinations/intro`

		var (
			ctx        = context.TODO()
			fakeClient client.Client
			validator  admission.Validator[T]
			ref        *v1alpha1.StateStoreReference
		)

		BeforeEach(func() {
			fakeClient = newFakeClientWithStateStoreRefIndex()
			validator = newValidator(fakeClient)
			ref = &v1alpha1.StateStoreReference{Kind: kind, Name: "mode-store"}
		})

		When("aggregatedYAML Destinations reference the state store", func() {
			BeforeEach(func() {
				createDestination(ctx, fakeClient, "b-aggregated", v1alpha1.FilepathModeAggregatedYAML, ref)
				createDestination(ctx, fakeClient, "a-aggregated", v1alpha1.FilepathModeAggregatedYAML, ref)
				createDestination(ctx, fakeClient, "nested", v1alpha1.FilepathModeNestedByMetadata, ref)
			})

			It("refuses a mode on create, naming the first such Destination", func() {
				_, err := validator.ValidateCreate(ctx, newStore("mode-store", &v1alpha1.Delivery{Mode: "x"}))
				Expect(err).To(MatchError(refusal))
			})

			It("refuses a mode on update", func() {
				_, err := validator.ValidateUpdate(ctx,
					newStore("mode-store", nil),
					newStore("mode-store", &v1alpha1.Delivery{Mode: "x"}),
				)
				Expect(err).To(MatchError(refusal))
			})

			It("allows removing the mode", func() {
				withMode := newStore("mode-store", &v1alpha1.Delivery{Mode: "x"})

				_, err := validator.ValidateUpdate(ctx, withMode, newStore("mode-store", nil))
				Expect(err).NotTo(HaveOccurred())

				_, err = validator.ValidateUpdate(ctx, withMode, newStore("mode-store", &v1alpha1.Delivery{}))
				Expect(err).NotTo(HaveOccurred())
			})

			It("allows the update of a state store that is being deleted", func() {
				withMode := newStore("mode-store", &v1alpha1.Delivery{Mode: "x"})
				beingDeleted := newStore("mode-store", &v1alpha1.Delivery{Mode: "x"})
				beingDeleted.SetDeletionTimestamp(&metav1.Time{})
				beingDeleted.SetFinalizers([]string{"kratix.io/test"})

				_, err := validator.ValidateUpdate(ctx, withMode, beingDeleted)
				Expect(err).NotTo(HaveOccurred())

				_, err = validator.ValidateUpdate(ctx, withMode, withMode)
				Expect(err).To(MatchError(refusal))
			})

			It("allows a mode on a different state store", func() {
				_, err := validator.ValidateCreate(ctx, newStore("other-store", &v1alpha1.Delivery{Mode: "x"}))
				Expect(err).NotTo(HaveOccurred())
			})
		})

		When("only a nestedByMetadata Destination references the state store", func() {
			BeforeEach(func() {
				createDestination(ctx, fakeClient, "nested", v1alpha1.FilepathModeNestedByMetadata, ref)
			})

			It("allows a mode on create and update", func() {
				_, err := validator.ValidateCreate(ctx, newStore("mode-store", &v1alpha1.Delivery{Mode: "x"}))
				Expect(err).NotTo(HaveOccurred())

				_, err = validator.ValidateUpdate(ctx,
					newStore("mode-store", nil),
					newStore("mode-store", &v1alpha1.Delivery{Mode: "x"}),
				)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})
}
