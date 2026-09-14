package lib_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/work-creator/lib"
)

const (
	configureKey = "configure"
	deleteKey    = "delete"

	// foreignKey stands for an entry under status.kratix.workflows this container
	// never owns. Every spec plants it and asserts it comes back unchanged, so a
	// write that widens to the whole workflows map is caught.
	foreignKey = "portal-x"
)

// statusWithWorkflows builds a status whose kratix.workflows holds the given
// entries plus the foreign entry, and returns it with a func that reports
// whether the foreign entry is still byte-for-byte what was planted.
func statusWithWorkflows(entries map[string]any) (map[string]any, func() string) {
	entries[foreignKey] = map[string]any{
		"pipelines": []any{
			map[string]any{
				"name":  "their-pipeline",
				"phase": "Suspended",
			},
		},
		"suspendedGeneration": int64(11),
	}

	status := map[string]any{
		"kratix": map[string]any{
			"workflows": entries,
		},
	}

	foreignJSON := func() string {
		kratix, ok := status["kratix"].(map[string]any)
		if !ok {
			return "status.kratix is gone"
		}
		workflows, ok := kratix["workflows"].(map[string]any)
		if !ok {
			return "status.kratix.workflows is gone"
		}
		entry, ok := workflows[foreignKey]
		if !ok {
			return "the foreign workflow entry is gone"
		}
		out, err := json.Marshal(entry)
		if err != nil {
			return err.Error()
		}
		return string(out)
	}

	return status, foreignJSON
}

const untouchedForeignEntry = `{"pipelines":[{"name":"their-pipeline","phase":"Suspended"}],"suspendedGeneration":11}`

func workflowEntry(status map[string]any, workflowKey string) map[string]any {
	GinkgoHelper()
	kratix, ok := status["kratix"].(map[string]any)
	Expect(ok).To(BeTrue(), "no status.kratix")
	workflows, ok := kratix["workflows"].(map[string]any)
	Expect(ok).To(BeTrue(), "no status.kratix.workflows")
	entry, ok := workflows[workflowKey].(map[string]any)
	Expect(ok).To(BeTrue(), "no status.kratix.workflows.%s", workflowKey)
	return entry
}

func workflowPipelines(status map[string]any, workflowKey string) []any {
	GinkgoHelper()
	pipelines, ok := workflowEntry(status, workflowKey)["pipelines"].([]any)
	Expect(ok).To(BeTrue(), "no status.kratix.workflows.%s.pipelines", workflowKey)
	return pipelines
}

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
				result := lib.MarkAsCompleted(status, configureKey, v1alpha1.WorkflowTypeResource)
				Expect(result).To(HaveKeyWithValue("message", "Resource requested"))
			})

			It("updates to 'Promise configured' if it is Pending and a Promise workflow", func() {
				status := map[string]any{
					"message": "Pending",
				}
				result := lib.MarkAsCompleted(status, configureKey, v1alpha1.WorkflowTypePromise)
				Expect(result).To(HaveKeyWithValue("message", "Promise configured"))
			})

			It("does not update if it is not 'Pending'", func() {
				status := map[string]any{
					"message": "Howdy",
				}
				result := lib.MarkAsCompleted(status, configureKey, v1alpha1.WorkflowTypeResource)
				Expect(result).To(HaveKeyWithValue("message", "Howdy"))

				result = lib.MarkAsCompleted(status, configureKey, v1alpha1.WorkflowTypePromise)
				Expect(result).To(HaveKeyWithValue("message", "Howdy"))
			})
		})

		Describe("The Conditions", func() {
			for _, workflowType := range []v1alpha1.Type{v1alpha1.WorkflowTypeResource, v1alpha1.WorkflowTypePromise} {
				It("sets the ConfigureWorkflowCompleted condition", func() {
					result := lib.MarkAsCompleted(map[string]any{}, configureKey, workflowType)
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
					}, configureKey, workflowType)
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
					}, configureKey, workflowType)
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

				for _, workflowKey := range []string{configureKey, deleteKey} {
					It("clears the suspended generation of the workflow it is given, leaving other workflows alone", func() {
						status, foreignJSON := statusWithWorkflows(map[string]any{
							workflowKey: map[string]any{
								"suspendedGeneration": int64(2),
							},
						})

						result := lib.MarkAsCompleted(status, workflowKey, workflowType)

						Expect(workflowEntry(result, workflowKey)).NotTo(HaveKey("suspendedGeneration"))
						Expect(foreignJSON()).To(Equal(untouchedForeignEntry))
					})
				}
			}
		})

	})

	Context("MarkPipelineAsSuspended", func() {
		for _, workflowKey := range []string{configureKey, deleteKey} {
			It("marks the pipeline as suspended under the workflow it is given", func() {
				status, foreignJSON := statusWithWorkflows(map[string]any{
					workflowKey: map[string]any{
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
				})
				status["message"] = "leave me alone"

				result, err := lib.MarkPipelineAsSuspended(status, workflowKey, "pipeline-b", "waiting for approval", "", 7)

				Expect(err).NotTo(HaveOccurred())
				pipelines := workflowPipelines(result, workflowKey)
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
				Expect(workflowEntry(result, workflowKey)).To(HaveKeyWithValue("suspendedGeneration", int64(7)))
				Expect(result).To(HaveKeyWithValue("message", "leave me alone"))
				Expect(foreignJSON()).To(Equal(untouchedForeignEntry))
			})
		}

		It("cleans up existing message when message is not provided anymore", func() {
			status, _ := statusWithWorkflows(map[string]any{
				configureKey: map[string]any{
					"pipelines": []any{
						map[string]any{
							"name":    "pipeline-a",
							"phase":   "Suspended",
							"message": "old message",
						},
					},
				},
			})

			result, err := lib.MarkPipelineAsSuspended(status, configureKey, "pipeline-a", "", "", 3)

			Expect(err).NotTo(HaveOccurred())
			Expect(workflowPipelines(result, configureKey)[0]).To(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-a"),
				HaveKeyWithValue("phase", "Suspended"),
				Not(HaveKey("message")),
			))
			Expect(workflowEntry(result, configureKey)).To(HaveKeyWithValue("suspendedGeneration", int64(3)))
		})

		It("fails when it cannot find the pipeline", func() {
			status, _ := statusWithWorkflows(map[string]any{
				configureKey: map[string]any{
					"pipelines": []any{
						map[string]any{
							"name":  "pipeline-a",
							"phase": "Running",
						},
					},
				},
			})

			_, err := lib.MarkPipelineAsSuspended(status, configureKey, "pipeline-b", "", "", 0)

			Expect(err).To(MatchError(ContainSubstring("\"pipeline-b\" not found in status.kratix.workflows.configure.pipelines")))
		})

		It("fails when the workflow it is given has no entry, rather than reading another workflow's", func() {
			status, foreignJSON := statusWithWorkflows(map[string]any{})

			_, err := lib.MarkPipelineAsSuspended(status, configureKey, "pipeline-a", "", "", 0)

			Expect(err).To(MatchError(ContainSubstring("missing status.kratix.workflows.configure")))
			Expect(foreignJSON()).To(Equal(untouchedForeignEntry))
		})

		When("retryAt is not set but the pipeline previously had retry related status fields", func() {
			It("clears nextRetryAt and attempts", func() {
				status, _ := statusWithWorkflows(map[string]any{
					configureKey: map[string]any{
						"pipelines": []any{
							map[string]any{
								"name":        "pipeline-a",
								"phase":       "Suspended",
								"nextRetryAt": "2026-03-25T14:22:00Z",
								"attempts":    int64(3),
							},
						},
					},
				})

				result, err := lib.MarkPipelineAsSuspended(status, configureKey, "pipeline-a", "waiting for a shooting star", "", 1)

				Expect(err).NotTo(HaveOccurred())
				Expect(workflowPipelines(result, configureKey)[0]).To(SatisfyAll(
					HaveKeyWithValue("phase", "Suspended"),
					HaveKeyWithValue("message", "waiting for a shooting star"),
					Not(HaveKey("nextRetryAt")),
					Not(HaveKey("attempts")),
				))
			})
		})

		When("retryAt is set", func() {
			It("sets the timestamp and increments the attempts counter", func() {
				status, _ := statusWithWorkflows(map[string]any{
					configureKey: map[string]any{
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
				})
				status["message"] = "leave me alone"

				expectedTimestamp := "2026-03-25T14:22:00Z"

				result, err := lib.MarkPipelineAsSuspended(status, configureKey, "pipeline-b", "waiting for approval", expectedTimestamp, 7)

				Expect(err).NotTo(HaveOccurred())
				pipelines := workflowPipelines(result, configureKey)
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
				Expect(workflowEntry(result, configureKey)).To(HaveKeyWithValue("suspendedGeneration", int64(7)))
				Expect(result).To(HaveKeyWithValue("message", "leave me alone"))
			})

			It("can increments the attempts counter when it wasn't set before", func() {
				status, _ := statusWithWorkflows(map[string]any{
					configureKey: map[string]any{
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
				})
				status["message"] = "leave me alone"

				expectedTimestamp := "2026-03-25T14:22:00Z"

				result, err := lib.MarkPipelineAsSuspended(status, configureKey, "pipeline-b", "waiting for approval", expectedTimestamp, 7)

				Expect(err).NotTo(HaveOccurred())
				pipelines := workflowPipelines(result, configureKey)
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
				Expect(workflowEntry(result, configureKey)).To(HaveKeyWithValue("suspendedGeneration", int64(7)))
				Expect(result).To(HaveKeyWithValue("message", "leave me alone"))
			})
		})
	})

	Describe("ClearPipelineSuspension", func() {
		for _, workflowKey := range []string{configureKey, deleteKey} {
			It("resets a suspended pipeline of the workflow it is given to running, leaving other workflows alone", func() {
				status, foreignJSON := statusWithWorkflows(map[string]any{
					workflowKey: map[string]any{
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
				})

				result, err := lib.ClearPipelineSuspension(status, workflowKey, "pipeline-b")

				Expect(err).NotTo(HaveOccurred())
				pipelines := workflowPipelines(result, workflowKey)
				Expect(pipelines[0]).To(SatisfyAll(
					HaveKeyWithValue("name", "pipeline-a"),
					HaveKeyWithValue("phase", "Succeeded"),
				))
				Expect(pipelines[1]).To(SatisfyAll(
					HaveKeyWithValue("name", "pipeline-b"),
					HaveKeyWithValue("phase", "Running"),
					Not(HaveKey("message")),
				))
				Expect(foreignJSON()).To(Equal(untouchedForeignEntry))
			})
		}

		It("resets a suspended pipeline to running and clears any retry status fields", func() {
			status, _ := statusWithWorkflows(map[string]any{
				configureKey: map[string]any{
					"pipelines": []any{
						map[string]any{
							"name":  "pipeline-a",
							"phase": "Succeeded",
						},
						map[string]any{
							"name":        "pipeline-b",
							"phase":       "Suspended",
							"message":     "waiting for approval",
							"attempts":    int64(18),
							"nextRetryAt": time.RFC3339,
						},
					},
				},
			})

			result, err := lib.ClearPipelineSuspension(status, configureKey, "pipeline-b")

			Expect(err).NotTo(HaveOccurred())
			pipelines := workflowPipelines(result, configureKey)
			Expect(pipelines[0]).To(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-a"),
				HaveKeyWithValue("phase", "Succeeded"),
			))
			Expect(pipelines[1]).To(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-b"),
				HaveKeyWithValue("phase", "Running"),
				Not(HaveKey("message")),
				Not(HaveKey("attempts")),
				Not(HaveKey("nextRetryAt")),
			))
		})

		It("fails when the pipeline does not exist", func() {
			status, _ := statusWithWorkflows(map[string]any{
				configureKey: map[string]any{
					"pipelines": []any{
						map[string]any{
							"name":  "pipeline-a",
							"phase": "Suspended",
						},
					},
				},
			})

			_, err := lib.ClearPipelineSuspension(status, configureKey, "pipeline-b")

			Expect(err).To(MatchError(ContainSubstring("\"pipeline-b\" not found in status.kratix.workflows.configure.pipelines")))
		})

		It("is a no-op when status.kratix is missing", func() {
			status := map[string]any{"message": "Pending"}
			result, err := lib.ClearPipelineSuspension(status, configureKey, "promise-configure")
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(status))
		})

		It("is a no-op when status.kratix.workflows is missing", func() {
			status := map[string]any{
				"kratix": map[string]any{"kind": "Jenkins"},
			}
			result, err := lib.ClearPipelineSuspension(status, configureKey, "promise-configure")
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(status))
		})

		It("is a no-op when the workflow it is given has no entry, rather than reading another workflow's", func() {
			status, foreignJSON := statusWithWorkflows(map[string]any{})
			result, err := lib.ClearPipelineSuspension(status, configureKey, "their-pipeline")
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(status))
			Expect(foreignJSON()).To(Equal(untouchedForeignEntry))
		})

		It("is a no-op when the workflow entry has no pipelines", func() {
			status, _ := statusWithWorkflows(map[string]any{
				configureKey: map[string]any{
					"suspendedGeneration": int64(3),
				},
			})
			result, err := lib.ClearPipelineSuspension(status, configureKey, "promise-configure")
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(status))
		})
	})
})
