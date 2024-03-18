package resourceutil_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/lib/resourceutil"
)

var _ = Describe("Name Utils", func() {
	When("the given name does not reach the maximum character length", func() {
		It("is appended with the sha", func() {
			name := resourceutil.GenerateObjectName("a-short-name")
			Expect(name).To(MatchRegexp(`^a-short-name-\b\w{5}\b$`))
		})
	})

	When("the given name does exceeds maximum character length", func() {
		It("is shortened and appended with the sha", func() {
			name := resourceutil.GenerateObjectName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaatrimmed")
			Expect(name).To(MatchRegexp(`^aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-\b\w{5}\b$`))

		})
	})

	When("the given name is the character limit", func() {
		It("is shortened and appended with the sha", func() {
			name := resourceutil.GenerateObjectName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab")
			Expect(name).To(MatchRegexp(`^aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab-\b\w{5}\b$`))

		})
	})
})
