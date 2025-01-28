package system_test

import (
	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"
)

var _ = FDescribe("PromiseRelease", func() {
	BeforeEach(func() {
		platform.kubectl("apply", "-f", "assets/promise-release/promise-release.yaml")
		platform.kubectl("apply", "-f", "assets/promise-release/deployment.yaml")
	})

	AfterEach(func() {
		if CurrentSpecReport().State.Is(types.SpecStatePassed) {
			platform.kubectl("delete", "-f", "assets/promise-release/deployment.yaml")
		}
	})

	When("fetching Promise from plain http endpoint", func() {
		It("can create and delete the Promise", func() {
			platform.eventuallyKubectl("get", "promiserelease", "insecure")
			platform.eventuallyKubectl("get", "promise", "insecurepro")

			platform.eventuallyKubectlDelete("promiserelease", "insecure")
			Eventually(func(g Gomega) {
				g.Expect(platform.kubectl("get", "promise")).ShouldNot(ContainSubstring("insecurepro"))
				g.Expect(platform.kubectl("get", "promiserelease")).ShouldNot(ContainSubstring("insecure"))
			}).Should(Succeed())
		})
	})

	When("fetching Promise from http endpoint with authorization", func() {
		It("can fetch and apply the Promise", func() {
			platform.eventuallyKubectl("get", "promiserelease", "secure")
			platform.eventuallyKubectl("get", "promise", "securepro")

			platform.eventuallyKubectlDelete("promiserelease", "secure")
			Eventually(func(g Gomega) {
				g.Expect(platform.kubectl("get", "promise")).ShouldNot(ContainSubstring("securepro"))
				g.Expect(platform.kubectl("get", "promiserelease")).ShouldNot(ContainSubstring("secure"))
			}).Should(Succeed())
		})
	})
})
