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
			platform.Kubectl("apply", "-f", "assets/healthchecks/healthy-health-record.yaml")
			platform.Kubectl("apply", "-f", "assets/healthchecks/unhealthy-health-record.yaml")
		})

		AfterEach(func() {
			if CurrentSpecReport().State.Is(types.SpecStatePassed) {
				platform.Kubectl("delete", "-f", "assets/healthchecks/healthy-health-record.yaml")
				platform.Kubectl("delete", "-f", "assets/healthchecks/unhealthy-health-record.yaml")
				platform.EventuallyKubectlDelete(promiseName, resourcename)
				platform.EventuallyKubectlDelete("promise", promiseName)
			}
		})

		It("updates the resource status healthRecord with the HealthRecord data", func() {
			By("reporting the health state of the healthrecord", func() {
				Eventually(func() string {
					return platform.Kubectl("get", "healthrecords", "healthy-example")
				}).Should(ContainSubstring("healthy"))

				Eventually(func() string {
					return platform.Kubectl("get", "healthrecords", "unhealthy-example")
				}).Should(ContainSubstring("unhealthy"))
			})

			By("updating the associated resource", func() {
				Eventually(func(g Gomega) {
					rawRR := platform.Kubectl("get", "-o", "json", promiseName, resourcename)
					rr := &unstructured.Unstructured{}
					g.Expect(rr.UnmarshalJSON([]byte(rawRR))).To(Succeed())

					status, _, err := unstructured.NestedMap(rr.Object, "status")
					g.Expect(err).NotTo(HaveOccurred(), "status not found")

					healthStatus, _, err := unstructured.NestedMap(status, "healthStatus")
					g.Expect(err).NotTo(HaveOccurred(), "healthRecord not found")

					g.Expect(healthStatus).To(HaveKeyWithValue("state", "unhealthy"))
					records, _, err := unstructured.NestedSlice(healthStatus, "healthRecords")
					g.Expect(err).NotTo(HaveOccurred(), "healthRecord.healthRecords not found")
					g.Expect(records).To(HaveLen(2))
				}).Should(Succeed())
			})
		})
	})
})
