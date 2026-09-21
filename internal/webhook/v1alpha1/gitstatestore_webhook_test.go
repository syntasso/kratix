package v1alpha1_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/internal/ptr"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	kratixWebhook "github.com/syntasso/kratix/internal/webhook/v1alpha1"
)

var _ = Describe("GitStateStore Webhook", func() {
	var (
		obj       *v1alpha1.GitStateStore
		oldObj    *v1alpha1.GitStateStore
		validator kratixWebhook.GitStateStoreCustomValidator
	)

	BeforeEach(func() {
		obj = &v1alpha1.GitStateStore{}
		oldObj = &v1alpha1.GitStateStore{}
		validator = kratixWebhook.GitStateStoreCustomValidator{}
	})

	Describe("spec.insecure on a non-HTTPS url", func() {
		expectedWarning := "spec.insecure only applies to https urls; it has no effect on ssh://git@github.com/syntasso/kratix.git"

		It("warns on create", func() {
			obj.Spec.URL = "ssh://git@github.com/syntasso/kratix.git"
			obj.Spec.Insecure = ptr.True()

			warnings, err := validator.ValidateCreate(context.TODO(), obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ConsistOf(expectedWarning))
		})

		It("warns on update", func() {
			obj.Spec.URL = "ssh://git@github.com/syntasso/kratix.git"
			obj.Spec.Insecure = ptr.False()

			warnings, err := validator.ValidateUpdate(context.TODO(), oldObj, obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ConsistOf(expectedWarning))
		})

		It("does not warn when insecure is unset", func() {
			obj.Spec.URL = "ssh://git@github.com/syntasso/kratix.git"

			warnings, err := validator.ValidateCreate(context.TODO(), obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(BeEmpty())
		})
	})

	It("does not warn when insecure is set on an https url", func() {
		obj.Spec.URL = "https://github.com/syntasso/kratix.git"
		obj.Spec.Insecure = ptr.True()

		warnings, err := validator.ValidateCreate(context.TODO(), obj)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
	})

	It("does not warn on delete", func() {
		obj.Spec.URL = "ssh://git@github.com/syntasso/kratix.git"
		obj.Spec.Insecure = ptr.True()

		warnings, err := validator.ValidateDelete(context.TODO(), obj)
		Expect(err).NotTo(HaveOccurred())
		Expect(warnings).To(BeEmpty())
	})
})
