package resourceutil_test

import (
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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
			err := resourceutil.SetKratixWorkflowsStatus(rr, configureKey, "lastSuccessfulTime", "2026-10-14T16:16:00Z")
			Expect(err).NotTo(HaveOccurred())
			Expect(resourceutil.GetKratixWorkflowsStatus(rr, configureKey, "lastSuccessfulTime")).
				To(Equal("2026-10-14T16:16:00Z"))
		})

		It("stores each workflow's fields under its own key", func() {
			Expect(resourceutil.SetKratixWorkflowsStatus(rr, configureKey, "lastSuccessfulTime", "2026-10-14T16:16:00Z")).To(Succeed())
			Expect(resourceutil.SetKratixWorkflowsStatus(rr, deleteKey, "lastSuccessfulTime", "2026-11-15T09:00:00Z")).To(Succeed())

			Expect(resourceutil.GetKratixWorkflowsStatus(rr, configureKey, "lastSuccessfulTime")).
				To(Equal("2026-10-14T16:16:00Z"))
			Expect(resourceutil.GetKratixWorkflowsStatus(rr, deleteKey, "lastSuccessfulTime")).
				To(Equal("2026-11-15T09:00:00Z"))
		})

		Context("GetKratixWorkflowsStatus", func() {
			It("returns empty string for missing keys", func() {
				Expect(resourceutil.GetKratixWorkflowsStatus(rr, configureKey, "lastSuccessfulTime")).To(BeEmpty())
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
							configureKey: map[string]interface{}{
								"pipelines": []interface{}{
									map[string]interface{}{
										"name":  "first-pipeline",
										"phase": v1alpha1.WorkflowPhasePending,
									},
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
							v1alpha1.PipelineNameLabel:       "first-pipeline",
							v1alpha1.KratixResourceHashLabel: "hash-of-the-run",
						},
					},
				}
			})

			It("marks the current pipeline as succeeded for a resource request", func() {
				err := resourceutil.MarkCurrentPipelineAsSucceeded(rr, configureKey, logger, job)
				Expect(err).NotTo(HaveOccurred())

				workflows := pipelinesUnderKey(rr, configureKey)
				Expect(workflows).To(HaveLen(1))

				pipeline := workflows[0].(map[string]interface{})
				Expect(pipeline["phase"]).To(Equal(v1alpha1.WorkflowPhaseSucceeded))
				Expect(pipeline["lastTransitionTime"]).NotTo(BeNil())
			})

			It("records the hash the job ran with on the pipeline it transitions", func() {
				Expect(resourceutil.MarkCurrentPipelineAsSucceeded(rr, configureKey, logger, job)).To(Succeed())

				pipeline := pipelinesUnderKey(rr, configureKey)[0].(map[string]interface{})
				Expect(pipeline).To(HaveKeyWithValue("hash", "hash-of-the-run"))
			})

			It("marks the current pipeline with an explicit phase for a resource request", func() {
				err := resourceutil.MarkCurrentPipelineAs(v1alpha1.WorkflowPhaseFailed, rr, configureKey, logger, job)
				Expect(err).NotTo(HaveOccurred())

				workflows := pipelinesUnderKey(rr, configureKey)
				Expect(workflows).To(HaveLen(1))

				pipeline := workflows[0].(map[string]interface{})
				Expect(pipeline["phase"]).To(Equal(v1alpha1.WorkflowPhaseFailed))
				Expect(pipeline["lastTransitionTime"]).NotTo(BeNil())
			})

			It("resets resource request pipelines to pending", func() {
				rr.Object["status"] = map[string]any{
					"kratix": map[string]any{
						"workflows": map[string]any{
							configureKey: map[string]any{
								"suspendedGeneration": int64(2),
							},
						},
					},
				}

				err := resourceutil.ResetPipelineStatusToPending(rr, configureKey, pipelines)
				Expect(err).NotTo(HaveOccurred())

				workflows := pipelinesUnderKey(rr, configureKey)
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
				_, found, err := unstructured.NestedInt64(rr.Object, "status", "kratix", "workflows", configureKey, "suspendedGeneration")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeFalse())
			})

			It("resets only the pipelines of the workflow it is given", func() {
				Expect(resourceutil.ResetPipelineStatusToPending(rr, deleteKey, pipelines)).To(Succeed())

				Expect(pipelinesUnderKey(rr, deleteKey)).To(HaveLen(2))
				Expect(pipelinesUnderKey(rr, configureKey)).To(ConsistOf(
					HaveKeyWithValue("name", "first-pipeline"),
				))
			})

			It("finds the index of a pipeline with the requested phase", func() {
				rr.Object["status"] = map[string]any{
					"kratix": map[string]any{
						"workflows": map[string]any{
							configureKey: map[string]any{
								"pipelines": []any{
									map[string]any{"name": "first-pipeline", "phase": v1alpha1.WorkflowPhaseSucceeded},
									map[string]any{"name": "second-pipeline", "phase": "Suspended"},
								},
							},
						},
					},
				}

				index, err := resourceutil.GetSuspendedPipelineIndex(rr, configureKey)
				Expect(err).NotTo(HaveOccurred())
				Expect(index).To(Equal(1))
			})

		})

	})

	Describe("MarkDeleteWorkflowSuspended", func() {
		var obj *unstructured.Unstructured

		BeforeEach(func() {
			obj = &unstructured.Unstructured{}
			obj.SetName("test-resource")
		})

		It("sets the DeleteWorkflowCompleted condition to False with DeleteWorkflowSuspended reason", func() {
			resourceutil.MarkDeleteWorkflowSuspended(logger, obj)

			condition := resourceutil.GetCondition(obj, resourceutil.DeleteWorkflowCompletedCondition)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(v1.ConditionFalse))
			Expect(condition.Reason).To(Equal(resourceutil.DeleteWorkflowSuspendedReason))
			Expect(condition.Message).NotTo(BeEmpty())
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
			err := resourceutil.SetKratixWorkflowsInt64Status(rr, configureKey, "suspendedGeneration", 7)
			Expect(err).NotTo(HaveOccurred())

			Expect(resourceutil.GetKratixWorkflowsInt64Status(rr, configureKey, "suspendedGeneration")).To(Equal(int64(7)))
			Expect(resourceutil.GetKratixWorkflowsInt64Status(rr, deleteKey, "suspendedGeneration")).To(BeZero())
		})
	})
})

const (
	configureKey = string(v1alpha1.WorkflowActionConfigure)
	deleteKey    = string(v1alpha1.WorkflowActionDelete)
)

// pipelinesUnderKey fails rather than returning an empty slice when the key
// holds no pipelines, so an assertion that a key was left alone cannot pass by
// the whole workflow having disappeared.
func pipelinesUnderKey(obj *unstructured.Unstructured, key string) []any {
	pipelines, found, err := unstructured.NestedSlice(obj.Object, "status", "kratix", "workflows", key, "pipelines")
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, found).To(BeTrue(), "no pipelines stored under workflow key %q", key)
	return pipelines
}
