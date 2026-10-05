package v1alpha1_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"sigs.k8s.io/yaml"
)

var _ = Describe("health status wire names", func() {
	It("reads data.promiseVersion on a HealthRecord", func() {
		record := &platformv1alpha1.HealthRecord{}
		Expect(yaml.Unmarshal([]byte("data:\n  state: healthy\n  promiseVersion: v2.0.0\n"), record)).To(Succeed())
		Expect(record.Data.PromiseVersion).To(Equal("v2.0.0"))
	})

	It("reads status.healthStatus on a ResourceBinding, keeping an expectedRecords of 0", func() {
		binding := &platformv1alpha1.ResourceBinding{}
		raw := "status:\n  healthStatus:\n    state: healthy\n    expectedPromiseVersion: v2.0.0\n    expectedRecords: 0\n"
		Expect(yaml.Unmarshal([]byte(raw), binding)).To(Succeed())
		Expect(binding.Status.HealthStatus).NotTo(BeNil())
		Expect(binding.Status.HealthStatus.State).To(Equal("healthy"))
		Expect(binding.Status.HealthStatus.ExpectedPromiseVersion).To(Equal("v2.0.0"))
		Expect(binding.Status.HealthStatus.ExpectedRecords).To(HaveValue(BeEquivalentTo(0)))

		out, err := yaml.Marshal(binding.Status)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(out)).To(ContainSubstring("expectedRecords: 0"))
	})
})
