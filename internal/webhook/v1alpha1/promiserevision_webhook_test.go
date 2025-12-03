package v1alpha1_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kratixWebhook "github.com/syntasso/kratix/internal/webhook/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

var _ = Describe("PromiseRevision Webhook", func() {
	var (
		obj       *platformv1alpha1.PromiseRevision
		validator *kratixWebhook.PromiseRevisionCustomValidator
	)

	BeforeEach(func() {
		obj = &platformv1alpha1.PromiseRevision{}
		validator = &kratixWebhook.PromiseRevisionCustomValidator{}
		Expect(validator).NotTo(BeNil())
		Expect(obj).NotTo(BeNil())
	})

	Context("request comes from a regular user", func() {
		When("deleting PromiseRevision under Validating Webhook", func() {
			It("should deny deletion if revision .status.latest is true", func() {
				obj.Status.Latest = true
				ctx := newCtxWithUserInfo("unit-test-user")
				Expect(validator.ValidateDelete(ctx, obj)).Error().To(HaveOccurred())
			})

			It("allows deletion when revision .status.latest is not set to true", func() {
				ctx := newCtxWithUserInfo("unit-test-user")
				Expect(validator.ValidateDelete(ctx, obj)).Error().NotTo(HaveOccurred())
			})
		})
	})

	Context("request comes from a service account from the kratix-platform-system namespace", func() {
		When("deleting PromiseRevision under Validating Webhook", func() {
			It("allows deletion even if revision .status.latest is true", func() {
				obj.Status.Latest = true
				ctx := newCtxWithUserInfo("system:serviceaccount:kratix-platform-system:kratix-platform-controller-manager")
				Expect(validator.ValidateDelete(ctx, obj)).Error().NotTo(HaveOccurred())
			})
		})
	})

})

func newCtxWithUserInfo(username string) context.Context {
	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UserInfo: authv1.UserInfo{
				Username: username,
			},
		},
	}
	return admission.NewContextWithRequest(context.Background(), req)
}
