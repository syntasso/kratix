package v1alpha1_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
)

var _ = Describe("Work", func() {
	w := &v1alpha1.Work{
		Spec: v1alpha1.WorkSpec{
			WorkloadCoreFields: v1alpha1.WorkloadCoreFields{
				WorkloadGroups: []v1alpha1.WorkloadGroup{
					{
						Directory: ".",
						DestinationSelectors: []v1alpha1.WorkloadGroupScheduling{
							{
								MatchLabels: map[string]string{"label": "default-promise-workflow-label"},
								Source:      "promise-workflow",
							},
							{
								MatchLabels: map[string]string{"label": "default-promise-label"},
								Source:      "promise",
							},
						},
					},
					{
						Directory: "platform",
						DestinationSelectors: []v1alpha1.WorkloadGroupScheduling{
							{
								MatchLabels: map[string]string{"label": "platform-promise-workflow-label"},
								Source:      "promise-workflow",
							},
						},
					},
				},
			},
		},
	}

	Describe("GetWorkloadGroupScheduling", func() {
		It("should return the scheduling for the given source and directory", func() {
			Expect(*w.GetWorkloadGroupScheduling("promise-workflow", ".")).To(Equal(v1alpha1.WorkloadGroupScheduling{
				MatchLabels: map[string]string{"label": "default-promise-workflow-label"},
				Source:      "promise-workflow",
			}))
			Expect(*w.GetWorkloadGroupScheduling("promise", ".")).To(Equal(v1alpha1.WorkloadGroupScheduling{
				MatchLabels: map[string]string{"label": "default-promise-label"},
				Source:      "promise",
			}))
			Expect(*w.GetWorkloadGroupScheduling("promise-workflow", "platform")).To(Equal(v1alpha1.WorkloadGroupScheduling{
				MatchLabels: map[string]string{"label": "platform-promise-workflow-label"},
				Source:      "promise-workflow",
			}))
			Expect(w.GetWorkloadGroupScheduling("promise", "platform")).To(BeNil())
		})
	})

	Describe("GetDefaultScheduling", func() {
		It("should return the default scheduling for the given source", func() {
			Expect(*w.GetDefaultScheduling("promise-workflow")).To(Equal(v1alpha1.WorkloadGroupScheduling{
				MatchLabels: map[string]string{
					"label": "default-promise-workflow-label",
				},
				Source: "promise-workflow",
			}))
		})
	})

})
