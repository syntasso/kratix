package resourceutil_test

import (
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
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
		originalHash, err = hash.ComputeHash(rr)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("PipelineExists", func() {
		It("returns false if there are no pipeline jobs", func() {
			Expect(resourceutil.PipelineExists(logger, nil, nil)).To(BeFalse())
			Expect(resourceutil.PipelineExists(logger, nil, []batchv1.Job{})).To(BeFalse())
		})

		It("returns true if there's a job matching the request spec hash", func() {
			jobs := []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix-resource-hash": originalHash,
						},
					},
					Status: completedStatus,
				},
			}

			Expect(resourceutil.PipelineExists(logger, rr, jobs)).To(BeTrue())
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
							"kratix-resource-hash": originalHash,
						},
					},
					Status: completedStatus,
				},
			}

			Expect(resourceutil.PipelineExists(logger, rr, jobs)).To(BeFalse())
		})

		It("only compares hashes of the most recent job", func() {
			resourceutil.MarkPipelineAsCompleted(logger, rr)
			jobs := []batchv1.Job{
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Hour)),
						Labels: map[string]string{
							"kratix-resource-hash": "some-old-hash",
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
						Labels: map[string]string{
							"kratix-resource-hash": originalHash,
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
						Labels: map[string]string{
							"kratix-resource-hash": "some-older-hash",
						},
					},
					Status: completedStatus,
				},
			}

			Expect(resourceutil.PipelineExists(logger, rr, jobs)).To(BeTrue())

			jobs = append(jobs, batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					CreationTimestamp: metav1.Now(),
					Labels: map[string]string{
						"kratix-resource-hash": "some-newer-hash",
					},
				},
			})

			Expect(resourceutil.PipelineExists(logger, rr, jobs)).To(BeFalse())
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
							"kratix-resource-hash": originalHash,
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix-resource-hash": originalHash,
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
							"kratix-resource-hash": originalHash,
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
							"kratix-resource-hash": originalHash,
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix-resource-hash": originalHash,
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
							"kratix-resource-hash": originalHash,
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix-resource-hash": originalHash,
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
							"kratix-resource-hash": originalHash,
						},
					},
					Status: completedStatus,
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						CreationTimestamp: metav1.Now(),
						Labels: map[string]string{
							"kratix-resource-hash": originalHash,
						},
					},
					Status: batchv1.JobStatus{},
				},
			}
			Expect(resourceutil.IsThereAPipelineRunning(logger, jobs)).To(BeTrue())
		})
	})
})
