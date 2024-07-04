package objectutil_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/lib/objectutil"
)

var _ = Describe("Name Utils", func() {
	When("the given name does not reach the maximum character length", func() {
		It("is appended with the sha", func() {
			name := objectutil.GenerateObjectName("a-short-name")
			Expect(name).To(MatchRegexp(`^a-short-name-\b\w{5}\b$`))
		})
	})

	When("the given name does exceeds maximum character length", func() {
		It("is shortened and appended with the sha", func() {
			name := objectutil.GenerateObjectName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaatrimmed")
			Expect(name).To(MatchRegexp(`^aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-\b\w{5}\b$`))
		})
	})

	When("the given name is the character limit", func() {
		It("is shortened and appended with the sha", func() {
			name := objectutil.GenerateObjectName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab")
			Expect(name).To(MatchRegexp(`^aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab-\b\w{5}\b$`))
		})
	})

	When("truncating the name would result in an invalid name", func() {
		It("truncates it properly", func() {
			longName := "kratix-promise-1.-@$#%&*()_+{}|:<>?/\\`~[]"
			name := objectutil.GenerateObjectName(longName)
			Expect(name).To(MatchRegexp(`^kratix-promise-1-\b\w{5}\b$`))
		})
	})
})
