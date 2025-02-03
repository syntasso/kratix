package system_test

import (
	"time"

	"github.com/syntasso/kratix/test/kubeutils"

	"github.com/onsi/ginkgo/v2/types"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ = Describe("Kratix Healthcheck", func() {
	promiseName, resourcename := "healthchecktest", "example"

	BeforeEach(func() {
		SetDefaultEventuallyTimeout(2 * time.Minute)
		SetDefaultEventuallyPollingInterval(2 * time.Second)
		kubeutils.SetTimeoutAndInterval(2*time.Minute, 2*time.Second)

		platform.Kubectl("apply", "-f", "assets/healthchecks/promise.yaml")
		Eventually(func() string {
			return platform.Kubectl("get", "crd")
		}).Should(ContainSubstring(promiseName))
		platform.Kubectl("apply", "-f", "assets/healthchecks/resource-request.yaml")
	})

	AfterEach(func() {
		if CurrentSpecReport().State.Is(types.SpecStatePassed) {
			platform.EventuallyKubectlDelete("promise", promiseName)
		}
	})

	Describe("Resource with HealthRecord", func() {
		BeforeEach(func() {
			platform.Kubectl("apply", "-f", "assets/healthchecks/health-record.yaml")
		})

		AfterEach(func() {
			if CurrentSpecReport().State.Is(types.SpecStatePassed) {
				platform.Kubectl("delete", "-f", "assets/healthchecks/health-record.yaml")
				platform.EventuallyKubectlDelete(promiseName, resourcename)
				platform.EventuallyKubectlDelete("promise", promiseName)
			}
		})

		It("updates the resource status healthRecord with the HealthRecord data", func() {
			By("reporting the health state of the healthrecord", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "healthrecords", "example")
				}).Should(ContainSubstring("healthy"))
			})

			By("updating the associated resource", func() {
				Eventually(func(g Gomega) {
					rawRR := platform.Kubectl("get", "-o", "json", promiseName, resourcename)
					rr := &unstructured.Unstructured{}
					g.Expect(rr.UnmarshalJSON([]byte(rawRR))).To(Succeed())

					status, _, err := unstructured.NestedMap(rr.Object, "status")
					g.Expect(err).NotTo(HaveOccurred(), "status not found")

					actualRecord, _, err := unstructured.NestedMap(status, "healthRecord")
					g.Expect(err).NotTo(HaveOccurred(), "healthRecord not found")

					// defined in assets/healthchecks/health-record.yaml
					g.Expect(actualRecord).To(HaveKeyWithValue("state", "healthy"))
					details, _, err := unstructured.NestedMap(actualRecord, "details")
					g.Expect(err).NotTo(HaveOccurred(), "healthRecord.details not found")
					g.Expect(details).To(HaveKeyWithValue("info", "message"))

					errors, _, err := unstructured.NestedMap(details, "errors")
					g.Expect(err).NotTo(HaveOccurred(), "healthRecord.details.errors not found")
					g.Expect(errors).To(HaveKeyWithValue("message", "error"))
				}).Should(Succeed())
			})
		})
	})
})
