package resourceutil_test

import (
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/resourceutil"
)

var _ = Describe("Conditions", func() {
	var (
		logger          = logr.Discard()
		rr              *unstructured.Unstructured
		originalHash    string
		completedStatus batchv1.JobStatus
	)

	BeforeEach(func() {
		rr = &unstructured.Unstructured{
			Object: map[string]interface{}{
				"spec": map[string]interface{}{
					"foo": "bar",
				},
			},
		}

		completedStatus = batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{
					Type:   batchv1.JobComplete,
					Status: v1.ConditionTrue,
				},
			},
		}

		var err error
		originalHash, err = hash.ComputeHashForResource(rr)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("PipelineExists", func() {
		It("returns false if there are no pipeline jobs", func() {
			Expect(resourceutil.PipelineWithDesiredSpecExists(logger, nil, nil)).To(BeNil())
			Expect(resourceutil.PipelineWithDesiredSpecExists(logger, nil, []batchv1.Job{})).To(BeNil())
		})

		It("returns true if there's a job matching the request spec hash", func() {
			jobs := []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
						Name: "expected",
					},
					Status: completedStatus,
				},
			}

			returnedJob, err := resourceutil.PipelineWithDesiredSpecExists(logger, rr, jobs)
			Expect(err).NotTo(HaveOccurred())
			Expect(returnedJob).NotTo(BeNil())
			Expect(returnedJob.GetName()).To(Equal("expected"))
		})

		It("returns false if there's no job matching the request spec hash", func() {
			rr.Object["spec"] = map[string]interface{}{
				"foo": "another-value",
			}

			jobs := []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
					},
					Status: completedStatus,
				},
			}

			Expect(resourceutil.PipelineWithDesiredSpecExists(logger, rr, jobs)).To(BeNil())
		})

		It("only compares hashes of the most recent job", func() {
			markWorkflowAsCompleted(rr)
			jobs := []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Hour)),
						Labels: map[string]string{
							"kratix.io/hash": "some-old-hash",
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
						Name: "expected",
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
						Labels: map[string]string{
							"kratix.io/hash": "some-older-hash",
						},
					},
					Status: completedStatus,
				},
			}

			returnedJob, err := resourceutil.PipelineWithDesiredSpecExists(logger, rr, jobs)
			Expect(err).NotTo(HaveOccurred())
			Expect(returnedJob).NotTo(BeNil())
			Expect(returnedJob.GetName()).To(Equal("expected"))

			jobs = append(jobs, batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
					Labels: map[string]string{
						"kratix.io/hash": "some-newer-hash",
					},
				},
			})

			Expect(resourceutil.PipelineWithDesiredSpecExists(logger, rr, jobs)).To(BeNil())
		})
	})

	Describe("IsThereAPipelineRunning", func() {
		It("returns false if there are no jobs", func() {
			Expect(resourceutil.IsThereAPipelineRunning(logger, nil)).To(BeFalse())
		})

		It("returns false if all jobs are Complete, Suspedend or Failed True", func() {
			jobs := []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
					},
					Status: batchv1.JobStatus{
						Conditions: []batchv1.JobCondition{
							{
								Type:   batchv1.JobFailed,
								Status: v1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
					},
					Status: batchv1.JobStatus{
						Conditions: []batchv1.JobCondition{
							{
								Type:   batchv1.JobSuspended,
								Status: v1.ConditionTrue,
							},
						},
					},
				},
			}
			Expect(resourceutil.IsThereAPipelineRunning(logger, jobs)).To(BeFalse())
		})

		It("returns false if all jobs are completed", func() {
			jobs := []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
					},
					Status: completedStatus,
				},
			}
			Expect(resourceutil.IsThereAPipelineRunning(logger, jobs)).To(BeFalse())
		})

		It("returns true if there's a job with the JobCompleted: False condition", func() {
			jobs := []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
					},
					Status: batchv1.JobStatus{
						Conditions: []batchv1.JobCondition{
							{
								Type:   batchv1.JobComplete,
								Status: v1.ConditionFalse,
							},
						},
					},
				},
			}
			Expect(resourceutil.IsThereAPipelineRunning(logger, jobs)).To(BeTrue())
		})

		It("returns true if any jobs have no conditions", func() {
			jobs := []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
					},
					Status: batchv1.JobStatus{},
				},
			}
			Expect(resourceutil.IsThereAPipelineRunning(logger, jobs)).To(BeTrue())
		})
	})

	Describe("PipelinesToSuspend", func() {
		It("returns empty if there are no jobs", func() {
			Expect(resourceutil.SuspendablePipelines(logger, nil)).To(BeEmpty())
		})

		It("returns any jobs that aren't suspended and have no active pods", func() {
			trueBool := true
			jobs := []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
						Name: "unactive-but-suspended",
					},
					Status: batchv1.JobStatus{
						Active: 0,
					},
					Spec: batchv1.JobSpec{
						Suspend: &trueBool,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix.io/hash": originalHash,
						},
						Name: "unactive",
					},
					Status: batchv1.JobStatus{
						Conditions: []batchv1.JobCondition{
							{
								Type:   batchv1.JobFailed,
								Status: v1.ConditionTrue,
							},
						},
						Active: 0,
					},
				},
			}
			Expect(resourceutil.SuspendablePipelines(logger, jobs)).To(HaveLen(1))
			Expect(resourceutil.SuspendablePipelines(logger, jobs)[0].GetName()).To(Equal("unactive"))
		})
	})

	Describe("SetStatus", func() {
		var rr *unstructured.Unstructured

		When("there is an existing status", func() {
			BeforeEach(func() {
				rr = &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": map[string]interface{}{
							"foo": "bar",
						},
					},
				}
			})

			It("sets the status of a resource", func() {
				resourceutil.SetStatus(rr, logger, "test", "val", "key1", int64(1))
				Expect(rr.Object).To(HaveKey("status"))
				Expect(rr.Object["status"]).To(HaveKeyWithValue("foo", "bar"))
				Expect(rr.Object["status"]).To(HaveKeyWithValue("test", "val"))
				Expect(rr.Object["status"]).To(HaveKeyWithValue("key1", int64(1)))
			})

			When("a non-string key is provided", func() {
				It("does not set that key/value pair", func() {
					resourceutil.SetStatus(rr, logger, 1, "val")
					Expect(rr.Object).To(HaveKey("status"))
					Expect(rr.Object["status"]).To(Equal(map[string]interface{}{"foo": "bar"}))
				})
			})

			When("an odd number of arguments is provided", func() {
				It("does not set any new key/value pairs", func() {
					resourceutil.SetStatus(rr, logger, "key1", "value1", "key2")
					Expect(rr.Object).To(HaveKey("status"))
					Expect(rr.Object["status"]).To(Equal(map[string]interface{}{"foo": "bar"}))
				})
			})
		})

		When("there is no existing status", func() {
			BeforeEach(func() {
				rr = &unstructured.Unstructured{
					Object: map[string]interface{}{},
				}
			})

			It("sets the status of a resource", func() {
				resourceutil.SetStatus(rr, logger, "test", "val", "key1", int64(1))
				Expect(rr.Object).To(HaveKey("status"))
				Expect(rr.Object["status"]).To(HaveKeyWithValue("test", "val"))
				Expect(rr.Object["status"]).To(HaveKeyWithValue("key1", int64(1)))
			})

			When("there are no valid status key/value pairs", func() {
				It("does not set status", func() {
					resourceutil.SetStatus(rr, logger, 1, "val")
					Expect(rr.Object).NotTo(HaveKey("status"))
				})
			})
		})
	})

	Describe("Kratix workflow status", func() {
		var rr *unstructured.Unstructured

		BeforeEach(func() {
			rr = &unstructured.Unstructured{
				Object: map[string]interface{}{},
			}
		})

		It("can set and get status.kratix.workflows fields correctly", func() {
			err := resourceutil.SetKratixWorkflowsStatus(rr, "lastSuccessfulConfigureWorkflowTime", "2026-10-14T16:16:00Z")
			Expect(err).NotTo(HaveOccurred())
			Expect(resourceutil.GetKratixWorkflowsStatus(rr, "lastSuccessfulConfigureWorkflowTime")).
				To(Equal("2026-10-14T16:16:00Z"))
		})

		Context("GetKratixWorkflowsStatus", func() {
			It("returns empty string for missing keys", func() {
				Expect(resourceutil.GetKratixWorkflowsStatus(rr, "lastSuccessfulConfigureWorkflowTime")).To(BeEmpty())
			})
		})

		Describe("pipeline execution status", func() {
			var job *batchv1.Job
			var pipelines []v1alpha1.PipelineJobResources

			BeforeEach(func() {
				rr.SetAPIVersion("test.kratix.io/v1alpha1")
				rr.SetKind("Redis")
				rr.Object["status"] = map[string]interface{}{
					"kratix": map[string]interface{}{
						"workflows": map[string]interface{}{
							"pipelines": []interface{}{
								map[string]interface{}{
									"name":  "first-pipeline",
									"phase": v1alpha1.WorkflowPhasePending,
								},
							},
						},
					},
				}

				pipelines = []v1alpha1.PipelineJobResources{
					{Name: "first-pipeline"},
					{Name: "second-pipeline"},
				}

				job = &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name: "job-1",
						Labels: map[string]string{
							v1alpha1.PipelineNameLabel: "first-pipeline",
						},
					},
				}
			})

			It("marks the current pipeline as succeeded for a resource request", func() {
				err := resourceutil.MarkCurrentPipelineAsSucceeded(rr, logger, job)
				Expect(err).NotTo(HaveOccurred())

				workflows, found, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "pipelines")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(workflows).To(HaveLen(1))

				pipeline := workflows[0].(map[string]interface{})
				Expect(pipeline["phase"]).To(Equal(v1alpha1.WorkflowPhaseSucceeded))
				Expect(pipeline["lastTransitionTime"]).NotTo(BeNil())
			})

			It("marks the current pipeline with an explicit phase for a resource request", func() {
				err := resourceutil.MarkCurrentPipelineAs(v1alpha1.WorkflowPhaseFailed, rr, logger, job)
				Expect(err).NotTo(HaveOccurred())

				workflows, found, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "pipelines")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(workflows).To(HaveLen(1))

				pipeline := workflows[0].(map[string]interface{})
				Expect(pipeline["phase"]).To(Equal(v1alpha1.WorkflowPhaseFailed))
				Expect(pipeline["lastTransitionTime"]).NotTo(BeNil())
			})

			It("clears the suspended message when the pipeline moves to a new phase", func() {
				rr.Object["status"] = map[string]any{
					"kratix": map[string]any{
						"workflows": map[string]any{
							"pipelines": []any{
								map[string]any{
									"name":    "first-pipeline",
									"phase":   v1alpha1.WorkflowPhaseSuspended,
									"message": "waiting",
								},
							},
						},
					},
				}

				err := resourceutil.MarkCurrentPipelineAs(v1alpha1.WorkflowPhaseRunning, rr, logger, job)
				Expect(err).NotTo(HaveOccurred())

				workflows, found, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "pipelines")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				pipeline := workflows[0].(map[string]interface{})
				Expect(pipeline["phase"]).To(Equal(v1alpha1.WorkflowPhaseRunning))
				Expect(pipeline).NotTo(HaveKey("message"))
			})

			It("resets resource request pipelines to pending", func() {
				rr.Object["status"] = map[string]any{
					"kratix": map[string]any{
						"workflows": map[string]any{
							"suspendedGeneration": int64(2),
						},
					},
				}

				err := resourceutil.ResetPipelineStatusToPending(rr, pipelines)
				Expect(err).NotTo(HaveOccurred())

				workflows, found, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "pipelines")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(workflows).To(HaveLen(2))
				Expect(workflows[0]).To(SatisfyAll(
					HaveKeyWithValue("name", "first-pipeline"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
					HaveKeyWithValue("lastTransitionTime", Not(BeNil())),
				))
				Expect(workflows[1]).To(SatisfyAll(
					HaveKeyWithValue("name", "second-pipeline"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
					HaveKeyWithValue("lastTransitionTime", Not(BeNil())),
				))
				_, found, err = unstructured.NestedInt64(rr.Object, "status", "kratix", "workflows", "suspendedGeneration")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})

			It("finds the index of a pipeline with the requested phase", func() {
				rr.Object["status"] = map[string]any{
					"kratix": map[string]any{
						"workflows": map[string]any{
							"pipelines": []any{
								map[string]any{"name": "first-pipeline", "phase": v1alpha1.WorkflowPhaseSucceeded},
								map[string]any{"name": "second-pipeline", "phase": "Suspended"},
							},
						},
					},
				}

				index, err := resourceutil.GetPipelineIndexWithPhase(rr, "Suspended")
				Expect(err).NotTo(HaveOccurred())
				Expect(index).To(Equal(1))
			})

			It("resets a single pipeline at an index back to pending", func() {
				rr.Object["status"] = map[string]any{
					"kratix": map[string]any{
						"workflows": map[string]any{
							"pipelines": []any{
								map[string]any{"name": "first-pipeline", "phase": v1alpha1.WorkflowPhaseSucceeded},
								map[string]any{"name": "second-pipeline", "phase": "Suspended", "message": "waiting"},
							},
						},
					},
				}

				changed, err := resourceutil.ResetPipelineStatusAtIndexToPending(rr, 1)
				Expect(err).NotTo(HaveOccurred())
				Expect(changed).To(BeTrue())

				workflows, found, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "pipelines")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(workflows[0]).To(SatisfyAll(
					HaveKeyWithValue("name", "first-pipeline"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseSucceeded),
				))
				Expect(workflows[1]).To(SatisfyAll(
					HaveKeyWithValue("name", "second-pipeline"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
					HaveKeyWithValue("lastTransitionTime", Not(BeNil())),
					Not(HaveKey("message")),
				))
			})
		})

	})

	Describe("GetObservedGeneration", func() {
		var rr *unstructured.Unstructured

		When("status is nil", func() {
			BeforeEach(func() {
				rr = &unstructured.Unstructured{
					Object: map[string]interface{}{},
				}
			})

			It("returns 0", func() {
				Expect(resourceutil.GetObservedGeneration(rr)).To(Equal(int64(0)))
			})
		})

		When("status.observedGeneration is nil", func() {
			BeforeEach(func() {
				rr = &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": map[string]interface{}{},
					},
				}
			})

			It("returns 0", func() {
				Expect(resourceutil.GetObservedGeneration(rr)).To(Equal(int64(0)))
			})
		})

		When("status.observedGeneration is set", func() {
			BeforeEach(func() {
				rr = &unstructured.Unstructured{
					Object: map[string]interface{}{
						"status": map[string]interface{}{
							"observedGeneration": int64(1),
						},
					},
				}
			})

			It("returns the observedGeneration", func() {
				Expect(resourceutil.GetObservedGeneration(rr)).To(Equal(int64(1)))
			})
		})
	})

	Describe("Kratix workflows status", func() {
		var rr *unstructured.Unstructured

		BeforeEach(func() {
			rr = &unstructured.Unstructured{Object: map[string]any{}}
		})

		It("can set and get int64 fields under status.kratix.workflows", func() {
			err := resourceutil.SetKratixWorkflowsInt64Status(rr, "suspendedGeneration", 7)
			Expect(err).NotTo(HaveOccurred())

			Expect(resourceutil.GetKratixWorkflowsInt64Status(rr, "suspendedGeneration")).To(Equal(int64(7)))
		})
	})
})

func markWorkflowAsCompleted(obj *unstructured.Unstructured) {
	resourceutil.SetCondition(obj, &clusterv1.Condition{
		Type:               resourceutil.ConfigureWorkflowCompletedCondition,
		Status:             v1.ConditionTrue,
		Message:            "Pipelines completed",
		Reason:             "PipelinesExecutedSuccessfully",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}
