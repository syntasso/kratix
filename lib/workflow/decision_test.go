package workflow

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// newPipelineResources builds the least a pipeline needs for a decision: a name, an
// action, and a Job carrying the hash that status is compared against.
func newPipelineResources(name, hash string, action v1alpha1.Action) v1alpha1.PipelineJobResources {
	return v1alpha1.PipelineJobResources{
		Name:           name,
		WorkflowAction: action,
		Job: &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name + "-job",
				Labels: map[string]string{v1alpha1.KratixResourceHashLabel: hash},
			},
		},
	}
}

func newRunningJob(name string) batchv1.Job {
	return batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     batchv1.JobStatus{Active: 1},
	}
}

func newFinishedJob(name string) batchv1.Job {
	return batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: v1.ConditionTrue},
			},
		},
	}
}

var _ = Describe("decideNextAction", func() {
	var opts Opts
	var progress workflowProgress

	BeforeEach(func() {
		opts = Opts{
			Resources: []v1alpha1.PipelineJobResources{
				newPipelineResources("pipeline-1", "hash-1", v1alpha1.WorkflowActionConfigure),
				newPipelineResources("pipeline-2", "hash-2", v1alpha1.WorkflowActionConfigure),
			},
		}
		// Status that matches both pipelines, with neither started yet.
		progress = workflowProgress{
			pipelines: []pipelineStatus{
				{name: "pipeline-1", phase: v1alpha1.WorkflowPhasePending, hash: "hash-1"},
				{name: "pipeline-2", phase: v1alpha1.WorkflowPhasePending, hash: "hash-2"},
			},
		}
	})

	When("a Job is still running", func() {
		BeforeEach(func() {
			progress.jobs = []batchv1.Job{newRunningJob("some-other-job")}
		})

		It("waits for that Job to finish", func() {
			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(waitForRunningJob))
			Expect(decision.jobToSuspend).To(BeNil())
		})

		It("suspends that Job when a manual reconciliation is requested", func() {
			progress.manualReconcile = true

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(suspendRunningJob))
			Expect(decision.jobToSuspend).NotTo(BeNil())
			Expect(decision.jobToSuspend.Name).To(Equal("some-other-job"))
		})

		It("waits even when every pipeline has already succeeded", func() {
			progress.pipelines[0].phase = v1alpha1.WorkflowPhaseSucceeded
			progress.pipelines[1].phase = v1alpha1.WorkflowPhaseSucceeded

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(waitForRunningJob))
		})
	})

	When("status does not describe the pipelines we were given", func() {
		It("starts again when there is no status at all", func() {
			progress.pipelines = nil

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(startPipeline))
			Expect(decision.restart).To(BeTrue())
			Expect(decision.pipeline.Name).To(Equal("pipeline-1"))
		})

		It("starts again when a pipeline has been renamed", func() {
			progress.pipelines[1].name = "renamed-pipeline"

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(startPipeline))
			Expect(decision.restart).To(BeTrue())
		})

		It("starts again when a pipeline that has started has a stale hash", func() {
			progress.pipelines[0].phase = v1alpha1.WorkflowPhaseRunning
			progress.pipelines[0].hash = "an-old-hash"

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(startPipeline))
			Expect(decision.restart).To(BeTrue())
		})

		It("does not start again when a pipeline that is still pending has a stale hash", func() {
			progress.pipelines[0].hash = "an-old-hash"

			decision := decideNextAction(opts, progress)

			Expect(decision.restart).To(BeFalse())
		})

		It("starts again when a status entry could not be read", func() {
			progress.malformedStatus = true

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(startPipeline))
			Expect(decision.restart).To(BeTrue())
		})
	})

	When("a restart has been asked for by label", func() {
		It("starts again from the first pipeline", func() {
			progress.runFromStart = true
			progress.pipelines[0].phase = v1alpha1.WorkflowPhaseSucceeded

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(startPipeline))
			Expect(decision.restart).To(BeTrue())
			Expect(decision.pipeline.Name).To(Equal("pipeline-1"))
		})

		It("starts again even when the workflow is paused", func() {
			progress.runFromStart = true
			progress.paused = true

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(startPipeline))
			Expect(decision.restart).To(BeTrue())
		})
	})

	When("nothing is running and status is trusted", func() {
		It("starts the first pipeline that has not finished", func() {
			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(startPipeline))
			Expect(decision.restart).To(BeFalse())
			Expect(decision.pipeline.Name).To(Equal("pipeline-1"))
		})

		It("moves on to the next pipeline once the first has succeeded", func() {
			progress.pipelines[0].phase = v1alpha1.WorkflowPhaseSucceeded

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(startPipeline))
			Expect(decision.pipeline.Name).To(Equal("pipeline-2"))
		})

		It("finishes the workflow once every pipeline has succeeded", func() {
			progress.pipelines[0].phase = v1alpha1.WorkflowPhaseSucceeded
			progress.pipelines[1].phase = v1alpha1.WorkflowPhaseSucceeded

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(finishWorkflow))
			Expect(decision.pipeline).To(BeNil())
		})

		It("stays paused when the workflow is suspended", func() {
			progress.paused = true

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(stayPaused))
		})

		It("finishes the workflow even when it is paused, if every pipeline has succeeded", func() {
			progress.paused = true
			progress.pipelines[0].phase = v1alpha1.WorkflowPhaseSucceeded
			progress.pipelines[1].phase = v1alpha1.WorkflowPhaseSucceeded

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(finishWorkflow))
		})
	})

	When("the current pipeline says it is running", func() {
		BeforeEach(func() {
			progress.pipelines[0].phase = v1alpha1.WorkflowPhaseRunning
			progress.pipelines[0].job = "pipeline-1-job"
		})

		It("records the Job named in status, which has now finished", func() {
			progress.jobs = []batchv1.Job{newFinishedJob("pipeline-1-job")}

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(recordFinishedJob))
			Expect(decision.jobToRecord).NotTo(BeNil())
			Expect(decision.jobToRecord.Name).To(Equal("pipeline-1-job"))
			Expect(decision.pipeline.Name).To(Equal("pipeline-1"))
		})

		It("starts the pipeline again when the Job named in status has gone", func() {
			progress.jobs = nil

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(startPipeline))
			Expect(decision.restart).To(BeFalse())
			Expect(decision.pipeline.Name).To(Equal("pipeline-1"))
		})

		It("ignores Jobs belonging to anything other than the current pipeline", func() {
			progress.jobs = []batchv1.Job{newFinishedJob("a-job-from-somewhere-else")}

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(startPipeline))
		})
	})

	When("the current pipeline has already failed", func() {
		BeforeEach(func() {
			progress.pipelines[0].phase = v1alpha1.WorkflowPhaseFailed
		})

		It("reports the failure rather than starting anything", func() {
			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(failCurrentPipeline))
			Expect(decision.pipeline.Name).To(Equal("pipeline-1"))
		})

		It("reports the failure for a delete pipeline too", func() {
			opts.Resources = []v1alpha1.PipelineJobResources{
				newPipelineResources("pipeline-1", "hash-1", v1alpha1.WorkflowActionDelete),
			}
			progress.pipelines = progress.pipelines[:1]

			decision := decideNextAction(opts, progress)

			Expect(decision.action).To(Equal(failCurrentPipeline))
			Expect(decision.pipeline.WorkflowAction).To(Equal(v1alpha1.WorkflowActionDelete))
		})
	})
})
