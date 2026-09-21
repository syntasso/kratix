package controller_test

import (
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/internal/controller"
)

var _ = Describe("WithTLSValidationReason", func() {
	DescribeTable("names the failure when the server certificate cannot be verified",
		func(gitOutput string) {
			err := controller.WithTLSValidationReason(errors.New(gitOutput))

			Expect(err).To(MatchError(ContainSubstring("TLS certificate validation failed")))
			Expect(err).To(MatchError(ContainSubstring(gitOutput)))
		},
		Entry("openssl-backed git", "fatal: unable to access 'https://gitea/repo': SSL certificate problem: self-signed certificate"),
		Entry("gnutls-backed git", "fatal: unable to access 'https://gitea/repo': server certificate verification failed"),
		Entry("go http client", "Get \"https://api.github.com\": x509: certificate signed by unknown authority"),
	)

	It("leaves unrelated failures untouched", func() {
		original := errors.New("fatal: Authentication failed for 'https://gitea/repo'")

		Expect(controller.WithTLSValidationReason(original)).To(BeIdenticalTo(original))
	})

	It("preserves the wrapped error for callers unwrapping it", func() {
		original := errors.New("SSL certificate problem: self-signed certificate")

		Expect(errors.Is(controller.WithTLSValidationReason(fmt.Errorf("clone: %w", original)), original)).To(BeTrue())
	})
})
