package v1alpha1_test

import (
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
)

var _ = Describe("Promise", func() {
	Describe("Scheduling", func() {
		It("generates the correct set of matchLabels", func() {
			input := []platformv1alpha1.SchedulingConfig{
				{
					Target: platformv1alpha1.Target{
						MatchLabels: map[string]string{
							"environment": "dev",
						},
					},
				},
				{
					Target: platformv1alpha1.Target{
						MatchLabels: map[string]string{
							"environment": "prod",
							"pci":         "false",
						},
					},
				},
				{
					Target: platformv1alpha1.Target{
						MatchLabels: map[string]string{
							"pci":    "true",
							"secure": "false",
						},
					},
				},
			}

			promise := platformv1alpha1.Promise{
				Spec: platformv1alpha1.PromiseSpec{
					Scheduling: input,
				},
			}

			selectors := promise.GetSchedulingSelectors()
			Expect(labels.FormatLabels(selectors)).To(Equal(`environment=dev,pci=false,secure=false`))
		})
	})

})
