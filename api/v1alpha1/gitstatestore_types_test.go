package v1alpha1_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/internal/ptr"

	"github.com/syntasso/kratix/api/v1alpha1"
)

var _ = Describe("GitStateStoreSpec", func() {
	Describe("TLSVerificationDisabled", func() {
		It("is disabled when insecure is unset, preserving pre-existing behaviour", func() {
			spec := v1alpha1.GitStateStoreSpec{}
			Expect(spec.TLSVerificationDisabled()).To(BeTrue())
		})

		It("is disabled when insecure is explicitly true", func() {
			spec := v1alpha1.GitStateStoreSpec{Insecure: ptr.True()}
			Expect(spec.TLSVerificationDisabled()).To(BeTrue())
		})

		It("is enabled when insecure is explicitly false", func() {
			spec := v1alpha1.GitStateStoreSpec{Insecure: ptr.False()}
			Expect(spec.TLSVerificationDisabled()).To(BeFalse())
		})
	})
})
