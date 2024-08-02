package objectutil_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/objectutil"
)

var _ = Describe("Name Utils", func() {
	Describe("GenerateObjectName", func() {
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

	Describe("GenerateDeterministicObjectName", func() {
		When("the given name does not reach the maximum character length", func() {
			It("is appended with the hash", func() {
				resource_name := "a-short-name"
				hashed_name := hash.ComputeHash(resource_name)
				name := objectutil.GenerateDeterministicObjectName(resource_name)
				Expect(name).To(Equal(resource_name + "-" + string(hashed_name[0:5])))
			})
		})

		When("the given name does exceeds maximum character length", func() {
			It("is shortened and appended with the hash", func() {
				resource_name := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaatrimmed"
				hashed_name := hash.ComputeHash(resource_name)
				name := objectutil.GenerateDeterministicObjectName(resource_name)
				Expect(name).To(Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-" + string(hashed_name[0:5])))
			})
		})

		When("the given name is the character limit", func() {
			It("is shortened and appended with the hash", func() {
				resource_name := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab"
				hashed_name := hash.ComputeHash(resource_name)
				name := objectutil.GenerateDeterministicObjectName(resource_name)
				Expect(name).To(Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaab-" + string(hashed_name[0:5])))
			})
		})

		When("truncating the name would result in an invalid name", func() {
			It("truncates it properly", func() {
				longName := "kratix-promise-1.-@$#%&*()_+{}|:<>?/\\`~[]"
				hashed_name := hash.ComputeHash(longName)
				name := objectutil.GenerateDeterministicObjectName(longName)
				Expect(name).To(Equal("kratix-promise-1-" + string(hashed_name[0:5])))
			})
		})
	})
})
