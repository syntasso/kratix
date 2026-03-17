package lib_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/work-creator/lib"
)

var _ = Describe("StatusUpdater", func() {

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

	Describe("NonMessageStatusKeys", func() {
		It("returns non-message keys", func() {
			keys := lib.NonMessageStatusKeys(map[string]any{
				"message": "ok",
				"bear":    "1",
				"lizard":  "2",
			})

			Expect(keys).To(ConsistOf("lizard", "bear"))
		})

		It("returns an empty list when 'message' is the only key", func() {
			keys := lib.NonMessageStatusKeys(map[string]any{
				"message": "ok",
			})

			Expect(keys).To(BeEmpty())
		})
	})

	Describe("MarkAsCompleted", func() {
		Describe("The Message", func() {
			It("updates to 'Resource requested' if it is 'Pending' and a Resource workflow", func() {
				status := map[string]any{
					"message": "Pending",
				}
				result := lib.MarkAsCompleted(status, v1alpha1.WorkflowTypeResource)
				Expect(result).To(HaveKeyWithValue("message", "Resource requested"))
			})

			It("updates to 'Promise configured' if it is Pending and a Promise workflow", func() {
				status := map[string]any{
					"message": "Pending",
				}
				result := lib.MarkAsCompleted(status, v1alpha1.WorkflowTypePromise)
				Expect(result).To(HaveKeyWithValue("message", "Promise configured"))
			})

			It("does not update if it is not 'Pending'", func() {
				status := map[string]any{
					"message": "Howdy",
				}
				result := lib.MarkAsCompleted(status, v1alpha1.WorkflowTypeResource)
				Expect(result).To(HaveKeyWithValue("message", "Howdy"))

				result = lib.MarkAsCompleted(status, v1alpha1.WorkflowTypePromise)
				Expect(result).To(HaveKeyWithValue("message", "Howdy"))
			})
		})

		Describe("The Conditions", func() {
			for _, workflowType := range []v1alpha1.Type{v1alpha1.WorkflowTypeResource, v1alpha1.WorkflowTypePromise} {
				It("sets the ConfigureWorkflowCompleted condition", func() {
					result := lib.MarkAsCompleted(map[string]any{}, workflowType)
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
					}, workflowType)
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
					}, workflowType)
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
			}
		})
	})

	Describe("MarkPipelineAsSuspended", func() {
		It("marks the pipeline as suspended and sets the message", func() {
			status := map[string]any{
				"kratix": map[string]any{
					"workflows": map[string]any{
						"pipelines": []any{
							map[string]any{
								"name":  "pipeline-a",
								"phase": "Succeeded",
							},
							map[string]any{
								"name":  "pipeline-b",
								"phase": "Running",
							},
						},
					},
				},
				"message": "leave me alone",
			}

			result, err := lib.MarkPipelineAsSuspended(status, "pipeline-b", "waiting for approval")

			Expect(err).NotTo(HaveOccurred())
			workflows := result["kratix"].(map[string]any)["workflows"].(map[string]any)
			pipelines := workflows["pipelines"].([]any)
			Expect(pipelines).To(HaveLen(2))
			Expect(pipelines[0]).To(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-a"),
				HaveKeyWithValue("phase", "Succeeded"),
			))
			Expect(pipelines[1]).To(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-b"),
				HaveKeyWithValue("phase", "Suspended"),
				HaveKeyWithValue("message", "waiting for approval"),
			))
			Expect(result).To(HaveKeyWithValue("message", "leave me alone"))
		})

		It("clears any existing message when a new one is not provided", func() {
			status := map[string]any{
				"kratix": map[string]any{
					"workflows": map[string]any{
						"pipelines": []any{
							map[string]any{
								"name":    "pipeline-a",
								"phase":   "Suspended",
								"message": "old message",
							},
						},
					},
				},
			}

			result, err := lib.MarkPipelineAsSuspended(status, "pipeline-a", "")

			Expect(err).NotTo(HaveOccurred())
			workflows := result["kratix"].(map[string]any)["workflows"].(map[string]any)
			pipeline := workflows["pipelines"].([]any)[0]
			Expect(pipeline).To(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-a"),
				HaveKeyWithValue("phase", "Suspended"),
				Not(HaveKey("message")),
			))
		})

		It("fails when the pipeline does not exist", func() {
			status := map[string]any{
				"kratix": map[string]any{
					"workflows": map[string]any{
						"pipelines": []any{
							map[string]any{
								"name":  "pipeline-a",
								"phase": "Running",
							},
						},
					},
				},
			}

			_, err := lib.MarkPipelineAsSuspended(status, "pipeline-b", "")

			Expect(err).To(MatchError(ContainSubstring("\"pipeline-b\" not found in status.kratix.workflows.pipelines")))
		})

		It("fails when workflow pipeline status is missing", func() {
			status := map[string]any{}

			_, err := lib.MarkPipelineAsSuspended(status, "pipeline-a", "")

			Expect(err).To(MatchError(ContainSubstring("missing status.kratix")))
		})

		It("fails when a pipeline entry has an invalid shape", func() {
			status := map[string]any{
				"kratix": map[string]any{
					"workflows": map[string]any{
						"pipelines": []any{"not a valid pipeline execution status"},
					},
				},
			}

			_, err := lib.MarkPipelineAsSuspended(status, "pipeline-a", "")

			Expect(err).To(MatchError(ContainSubstring("invalid pipeline status type")))
		})
	})
})
