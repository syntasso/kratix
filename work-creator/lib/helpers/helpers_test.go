package helpers_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
)

var _ = Describe("GetParametersFromEnv", func() {
	It("reads the promise version from KRATIX_PROMISE_VERSION", func() {
		GinkgoT().Setenv(v1alpha1.KratixPromiseVersionEnvVar, "v2.0.0")

		Expect(helpers.GetParametersFromEnv().PromiseVersion).To(Equal("v2.0.0"))
	})
})
