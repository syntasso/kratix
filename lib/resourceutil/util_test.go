package resourceutil_test

import (
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

	Describe("RetryAfterRemaining", func() {
		var lastSuccessful time.Time

		BeforeEach(func() {
			lastSuccessful = time.Now().Add(-time.Minute)
			resourceutil.SetStatus(rr, logger, "lastSuccessfulConfigureWorkflowTime", lastSuccessful.Format(time.RFC3339))
		})

		It("returns false when retryAfter is not set", func() {
			remaining, configured := resourceutil.RetryAfterRemaining(rr, logger)
			Expect(configured).To(BeFalse())
			Expect(remaining).To(Equal(time.Duration(0)))
		})

		It("calculates the remaining duration when retryAfter is configured", func() {
			resourceutil.SetStatus(rr, logger, "retryAfter", "2h")
			remaining, configured := resourceutil.RetryAfterRemaining(rr, logger)

			Expect(configured).To(BeTrue())
			Expect(remaining).To(BeNumerically("~", time.Hour+59*time.Minute, time.Minute))
		})

		It("uses the configure workflow condition when no last successful time exists", func() {
			rr.Object["status"] = map[string]interface{}{"retryAfter": "10m"}
			conditionTime := time.Now().Add(-5 * time.Minute)
			resourceutil.SetCondition(rr, &clusterv1.Condition{
				Type:               resourceutil.ConfigureWorkflowCompletedCondition,
				Status:             v1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(conditionTime),
			})

			remaining, configured := resourceutil.RetryAfterRemaining(rr, logger)
			Expect(configured).To(BeTrue())
			Expect(remaining).To(BeNumerically("~", 5*time.Minute, time.Minute))
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
