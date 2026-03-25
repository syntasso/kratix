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

				It("clears the suspended generation when workflows are completed", func() {
					result := lib.MarkAsCompleted(map[string]any{
						"kratix": map[string]any{
							"workflows": map[string]any{
								"suspendedGeneration": int64(2),
							},
						},
					}, workflowType)

					workflows := result["kratix"].(map[string]any)["workflows"].(map[string]any)
					Expect(workflows).NotTo(HaveKey("suspendedGeneration"))
				})
			}
		})

	})

	Context("MarkPipelineAsSuspended", func() {
		It("marks the pipeline as suspended", func() {
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

			result, err := lib.MarkPipelineAsSuspended(status, "pipeline-b", "waiting for approval", "", 7)

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
			Expect(workflows).To(HaveKeyWithValue("suspendedGeneration", int64(7)))
			Expect(result).To(HaveKeyWithValue("message", "leave me alone"))
		})

		It("cleans up existing message when message is not provided anymore", func() {
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

			result, err := lib.MarkPipelineAsSuspended(status, "pipeline-a", "", "", 3)

			Expect(err).NotTo(HaveOccurred())
			workflows := result["kratix"].(map[string]any)["workflows"].(map[string]any)
			pipeline := workflows["pipelines"].([]any)[0]
			Expect(pipeline).To(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-a"),
				HaveKeyWithValue("phase", "Suspended"),
				Not(HaveKey("message")),
			))
			Expect(workflows).To(HaveKeyWithValue("suspendedGeneration", int64(3)))
		})

		It("fails when it cannot find the pipeline", func() {
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

			_, err := lib.MarkPipelineAsSuspended(status, "pipeline-b", "", "", 0)

			Expect(err).To(MatchError(ContainSubstring("\"pipeline-b\" not found in status.kratix.workflows.pipelines")))
		})

		When("retryAt is set", func() {
			It("sets the timestamp and increments the attempts counter", func() {
				status := map[string]any{
					"kratix": map[string]any{
						"workflows": map[string]any{
							"pipelines": []any{
								map[string]any{
									"name":  "pipeline-a",
									"phase": "Succeeded",
								},
								map[string]any{
									"name":     "pipeline-b",
									"phase":    "Running",
									"attempts": int64(17),
								},
							},
						},
					},
					"message": "leave me alone",
				}

				expectedTimestamp := "2026-03-25T14:22:00Z"

				result, err := lib.MarkPipelineAsSuspended(status, "pipeline-b", "waiting for approval", expectedTimestamp, 7)

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
					HaveKeyWithValue("nextRetryAt", expectedTimestamp),
					HaveKeyWithValue("attempts", int64(18)),
				))
				Expect(workflows).To(HaveKeyWithValue("suspendedGeneration", int64(7)))
				Expect(result).To(HaveKeyWithValue("message", "leave me alone"))
			})
			It("can increments the attempts counter when it wasn't set before", func() {
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

				expectedTimestamp := "2026-03-25T14:22:00Z"

				result, err := lib.MarkPipelineAsSuspended(status, "pipeline-b", "waiting for approval", expectedTimestamp, 7)

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
					HaveKeyWithValue("nextRetryAt", expectedTimestamp),
					HaveKeyWithValue("attempts", int64(1)),
				))
				Expect(workflows).To(HaveKeyWithValue("suspendedGeneration", int64(7)))
				Expect(result).To(HaveKeyWithValue("message", "leave me alone"))
			})
		})
	})

	Describe("ClearPipelineSuspension", func() {
		It("resets a suspended pipeline to running and clears the message", func() {
			status := map[string]any{
				"kratix": map[string]any{
					"workflows": map[string]any{
						"pipelines": []any{
							map[string]any{
								"name":  "pipeline-a",
								"phase": "Succeeded",
							},
							map[string]any{
								"name":    "pipeline-b",
								"phase":   "Suspended",
								"message": "waiting for approval",
							},
						},
					},
				},
			}

			result, err := lib.ClearPipelineSuspension(status, "pipeline-b")

			Expect(err).NotTo(HaveOccurred())
			workflows := result["kratix"].(map[string]any)["workflows"].(map[string]any)
			pipelines := workflows["pipelines"].([]any)
			Expect(pipelines[0]).To(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-a"),
				HaveKeyWithValue("phase", "Succeeded"),
			))
			Expect(pipelines[1]).To(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-b"),
				HaveKeyWithValue("phase", "Running"),
				Not(HaveKey("message")),
			))
		})

		It("leaves non-suspended pipelines unchanged", func() {
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

			result, err := lib.ClearPipelineSuspension(status, "pipeline-a")

			Expect(err).NotTo(HaveOccurred())
			workflows := result["kratix"].(map[string]any)["workflows"].(map[string]any)
			Expect(workflows["pipelines"].([]any)[0]).To(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-a"),
				HaveKeyWithValue("phase", "Running"),
			))
		})

		It("fails when the pipeline does not exist", func() {
			status := map[string]any{
				"kratix": map[string]any{
					"workflows": map[string]any{
						"pipelines": []any{
							map[string]any{
								"name":  "pipeline-a",
								"phase": "Suspended",
							},
						},
					},
				},
			}

			_, err := lib.ClearPipelineSuspension(status, "pipeline-b")

			Expect(err).To(MatchError(ContainSubstring("\"pipeline-b\" not found in status.kratix.workflows.pipelines")))
		})
	})
})
