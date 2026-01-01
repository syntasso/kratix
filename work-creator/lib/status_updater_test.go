package lib_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/work-creator/lib"
)

var _ = Describe("StatusUpdater", func() {

	Describe("MergeLabels", func() {
		It("merges the two maps", func() {
			existing := map[string]string{
				"existing-key": "existing-value",
				"shared-key":   "old-value",
			}
			incoming := map[string]string{
				"incoming-key": "incoming-value",
				"shared-key":   "new-value",
			}
			result := lib.MergeLabels(existing, incoming)
			Expect(result).To(SatisfyAll(
				HaveKeyWithValue("existing-key", "existing-value"),
				HaveKeyWithValue("incoming-key", "incoming-value"),
				HaveKeyWithValue("shared-key", "new-value"),
			))
			Expect(result).To(HaveLen(3))
		})

		It("returns incoming labels when existing is empty", func() {
			existing := map[string]string{}
			incoming := map[string]string{
				"key1": "value1",
				"key2": "value2",
			}
			result := lib.MergeLabels(existing, incoming)
			Expect(result).To(Equal(incoming))
		})

		It("returns existing labels when incoming is empty", func() {
			existing := map[string]string{
				"key1": "value1",
				"key2": "value2",
			}
			incoming := map[string]string{}
			result := lib.MergeLabels(existing, incoming)
			Expect(result).To(Equal(existing))
		})

		It("returns empty map when both are empty", func() {
			existing := map[string]string{}
			incoming := map[string]string{}
			result := lib.MergeLabels(existing, incoming)
			Expect(result).To(BeEmpty())
		})
	})

	Describe("MergeStatuses", func() {
		It("merges the two maps", func() {
			existing := map[string]any{
				"message": "Pending",
				"extra":   "value",
				"slices":  []string{"A", "B"},
				"conditions": []any{
					map[string]any{
						"type":   "PreviousCondition",
						"status": "False",
					},
					map[string]any{
						"message": "Some other reason",
						"type":    "SomeOtherCondition",
						"status":  "False",
					},
				},
			}
			incoming := map[string]any{
				"message": "Resource requested",
				"new":     "value",
				"slices":  []string{"C", "D"},
				"conditions": []any{
					map[string]any{
						"type":   "PreviousCondition",
						"status": "True",
					},
					map[string]any{
						"message": "Another reason",
						"type":    "AnotherCondition",
						"status":  "True",
					},
				},
			}
			result := lib.MergeStatuses(existing, incoming)
			Expect(result).To(SatisfyAll(
				HaveKeyWithValue("message", "Resource requested"),
				HaveKeyWithValue("extra", "value"),
				HaveKeyWithValue("new", "value"),
				HaveKeyWithValue("slices", ConsistOf("C", "D")),
				HaveKeyWithValue("conditions", ConsistOf(
					SatisfyAll(
						HaveKeyWithValue("type", "PreviousCondition"),
						HaveKeyWithValue("status", "True"),
					),
					SatisfyAll(
						HaveKeyWithValue("message", "Some other reason"),
						HaveKeyWithValue("type", "SomeOtherCondition"),
						HaveKeyWithValue("status", "False"),
					),
					SatisfyAll(
						HaveKeyWithValue("message", "Another reason"),
						HaveKeyWithValue("type", "AnotherCondition"),
						HaveKeyWithValue("status", "True"),
					),
				)),
			))
			Expect(result).To(HaveLen(5))
		})
	})

	Describe("MarkAsCompleted", func() {
		Describe("The Message", func() {
			It("updates to 'Resource requested' if it is 'Pending'", func() {
				status := map[string]any{
					"message": "Pending",
				}
				result := lib.MarkAsCompleted(status)
				Expect(result).To(HaveKeyWithValue("message", "Resource requested"))
			})

			It("does not update if it is not 'Pending'", func() {
				status := map[string]any{
					"message": "Howdy",
				}
				result := lib.MarkAsCompleted(status)
				Expect(result).To(HaveKeyWithValue("message", "Howdy"))
			})
		})

		Describe("The Conditions", func() {
			It("sets the ConfigureWorkflowCompleted condition", func() {
				result := lib.MarkAsCompleted(map[string]any{})
				Expect(result).To(SatisfyAll(
					HaveKeyWithValue("conditions", ConsistOf(
						SatisfyAll(
							HaveKeyWithValue("message", "Pipelines completed"),
							HaveKeyWithValue("lastTransitionTime", Not(BeNil())),
							HaveKeyWithValue("status", "True"),
							HaveKeyWithValue("type", string(resourceutil.ConfigureWorkflowCompletedCondition)),
							HaveKeyWithValue("reason", resourceutil.PipelinesExecutedSuccessfully),
						),
					)),
				))
			})

			It("overrides any existing ConfigureWorkflowCompleted condition", func() {
				result := lib.MarkAsCompleted(map[string]any{
					"conditions": []any{
						map[string]any{
							"message": "Some other reason",
							"type":    string(resourceutil.ConfigureWorkflowCompletedCondition),
							"status":  "False",
						},
					},
				})
				Expect(result).To(SatisfyAll(
					HaveKeyWithValue("conditions", ConsistOf(
						SatisfyAll(
							HaveKeyWithValue("message", "Pipelines completed"),
							HaveKeyWithValue("type", string(resourceutil.ConfigureWorkflowCompletedCondition)),
							HaveKeyWithValue("status", "True"),
						),
					)),
				))
			})

			It("preserves other conditions", func() {
				result := lib.MarkAsCompleted(map[string]any{
					"conditions": []any{
						map[string]any{
							"message": "Some other reason",
							"type":    "SomeOtherCondition",
							"status":  "False",
						},
					},
				})
				Expect(result).To(SatisfyAll(
					HaveKeyWithValue("conditions", ContainElement(
						SatisfyAll(
							HaveKeyWithValue("message", "Some other reason"),
							HaveKeyWithValue("type", "SomeOtherCondition"),
							HaveKeyWithValue("status", "False"),
						),
					)),
				))
			})
		})
	})
})
