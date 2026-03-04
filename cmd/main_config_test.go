package main_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	cmd "github.com/syntasso/kratix/cmd"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

var _ = Describe("Kratix config pod ttl", func() {
	Describe("GetPodTTLSecondsAfterFinishedForTest", func() {
		It("returns nil when config is nil", func() {
			Expect(cmd.GetPodTTLSecondsAfterFinishedForTest(nil)).To(BeNil())
		})

		It("returns nil when ttl is not configured", func() {
			Expect(cmd.GetPodTTLSecondsAfterFinishedForTest(&cmd.KratixConfig{})).To(BeNil())
		})

		It("returns nil when ttl is zero", func() {
			got := cmd.GetPodTTLSecondsAfterFinishedForTest(&cmd.KratixConfig{
				Workflows: cmd.Workflows{
					JobOptions: cmd.JobOptions{
						PodTTLSecondsAfterFinished: &metav1.Duration{Duration: 0},
					},
				},
			})
			Expect(got).To(BeNil())
		})

		It("returns nil when ttl is negative", func() {
			got := cmd.GetPodTTLSecondsAfterFinishedForTest(&cmd.KratixConfig{
				Workflows: cmd.Workflows{
					JobOptions: cmd.JobOptions{
						PodTTLSecondsAfterFinished: &metav1.Duration{Duration: -1 * time.Second},
					},
				},
			})
			Expect(got).To(BeNil())
		})

		It("returns configured ttl", func() {
			expected := 60 * time.Second
			got := cmd.GetPodTTLSecondsAfterFinishedForTest(&cmd.KratixConfig{
				Workflows: cmd.Workflows{
					JobOptions: cmd.JobOptions{
						PodTTLSecondsAfterFinished: &metav1.Duration{Duration: expected},
					},
				},
			})
			Expect(got).ToNot(BeNil())
			Expect(*got).To(Equal(expected))
		})
	})

	It("unmarshals podTTLSecondsAfterFinished from workflows.jobOptions", func() {
		config := `
workflows:
  jobOptions:
    podTTLSecondsAfterFinished: 60s
`
		kratixConfig := cmd.KratixConfig{}
		Expect(yaml.Unmarshal([]byte(config), &kratixConfig)).To(Succeed())

		got := cmd.GetPodTTLSecondsAfterFinishedForTest(&kratixConfig)
		Expect(got).ToNot(BeNil())
		Expect(*got).To(Equal(time.Minute))
	})
})
