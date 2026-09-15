package workflow_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/workflow"
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

var _ = Describe("DecideNextAction", func() {
	var opts workflow.Opts
	var progress workflow.Progress

	BeforeEach(func() {
		opts = workflow.Opts{
			Resources: []v1alpha1.PipelineJobResources{
				newPipelineResources("pipeline-1", "hash-1", v1alpha1.WorkflowActionConfigure),
				newPipelineResources("pipeline-2", "hash-2", v1alpha1.WorkflowActionConfigure),
			},
		}
		// Status that matches both pipelines, with neither started yet.
		progress = workflow.Progress{
			Pipelines: []v1alpha1.WorkflowPipelineStatus{
				{Name: "pipeline-1", Phase: v1alpha1.WorkflowPhasePending, Hash: "hash-1"},
				{Name: "pipeline-2", Phase: v1alpha1.WorkflowPhasePending, Hash: "hash-2"},
			},
		}
	})

	When("a Job is still running", func() {
		BeforeEach(func() {
			progress.Jobs = []batchv1.Job{newRunningJob("some-other-job")}
		})

		It("waits for that Job to finish", func() {
			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.WaitForRunningJob))
			Expect(decision.JobToSuspend).To(BeNil())
		})

		It("suspends that Job when a manual reconciliation is requested", func() {
			progress.ManualReconcile = true

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.SuspendRunningJob))
			Expect(decision.JobToSuspend).NotTo(BeNil())
			Expect(decision.JobToSuspend.Name).To(Equal("some-other-job"))
		})

		It("waits even when every pipeline has already succeeded", func() {
			progress.Pipelines[0].Phase = v1alpha1.WorkflowPhaseSucceeded
			progress.Pipelines[1].Phase = v1alpha1.WorkflowPhaseSucceeded

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.WaitForRunningJob))
		})
	})

	When("the pipelines have changed since status was written", func() {
		It("starts again when there is no status at all", func() {
			progress.Pipelines = nil

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StartPipeline))
			Expect(decision.Restart).To(BeTrue())
			Expect(decision.Pipeline.Name).To(Equal("pipeline-1"))
		})

		It("starts again when a pipeline has been renamed", func() {
			progress.Pipelines[1].Name = "renamed-pipeline"

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StartPipeline))
			Expect(decision.Restart).To(BeTrue())
		})

		It("starts again when a pipeline that has started has a stale hash", func() {
			progress.Pipelines[0].Phase = v1alpha1.WorkflowPhaseRunning
			progress.Pipelines[0].Hash = "an-old-hash"

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StartPipeline))
			Expect(decision.Restart).To(BeTrue())
		})

		It("does not start again when a pipeline that is still pending has a stale hash", func() {
			progress.Pipelines[0].Hash = "an-old-hash"

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Restart).To(BeFalse())
		})

		It("starts again when a status entry could not be read", func() {
			progress.MalformedStatus = true

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StartPipeline))
			Expect(decision.Restart).To(BeTrue())
		})
	})

	When("a restart has been asked for by label", func() {
		It("starts again from the first pipeline", func() {
			progress.RunFromStart = true
			progress.Pipelines[0].Phase = v1alpha1.WorkflowPhaseSucceeded

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StartPipeline))
			Expect(decision.Restart).To(BeTrue())
			Expect(decision.Pipeline.Name).To(Equal("pipeline-1"))
		})

		It("starts again even when the workflow is paused", func() {
			progress.RunFromStart = true
			progress.Paused = true

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StartPipeline))
			Expect(decision.Restart).To(BeTrue())
		})
	})

	When("nothing is running and status is trusted", func() {
		It("starts the first pipeline that has not finished", func() {
			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StartPipeline))
			Expect(decision.Restart).To(BeFalse())
			Expect(decision.Pipeline.Name).To(Equal("pipeline-1"))
		})

		It("moves on to the next pipeline once the first has succeeded", func() {
			progress.Pipelines[0].Phase = v1alpha1.WorkflowPhaseSucceeded

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StartPipeline))
			Expect(decision.Pipeline.Name).To(Equal("pipeline-2"))
		})

		It("finishes the workflow once every pipeline has succeeded", func() {
			progress.Pipelines[0].Phase = v1alpha1.WorkflowPhaseSucceeded
			progress.Pipelines[1].Phase = v1alpha1.WorkflowPhaseSucceeded

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.FinishWorkflow))
			Expect(decision.Pipeline).To(BeNil())
		})

		It("stays paused when the workflow is suspended", func() {
			progress.Paused = true

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StayPaused))
		})

		It("finishes the workflow even when it is paused, if every pipeline has succeeded", func() {
			progress.Paused = true
			progress.Pipelines[0].Phase = v1alpha1.WorkflowPhaseSucceeded
			progress.Pipelines[1].Phase = v1alpha1.WorkflowPhaseSucceeded

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.FinishWorkflow))
		})
	})

	When("the current pipeline says it is running", func() {
		BeforeEach(func() {
			progress.Pipelines[0].Phase = v1alpha1.WorkflowPhaseRunning
			progress.Pipelines[0].Job = "pipeline-1-job"
		})

		It("records the outcome of the Job named in status, which has now finished", func() {
			progress.Jobs = []batchv1.Job{newFinishedJob("pipeline-1-job")}

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.RecordJobOutcome))
			Expect(decision.JobToRecord).NotTo(BeNil())
			Expect(decision.JobToRecord.Name).To(Equal("pipeline-1-job"))
			Expect(decision.Pipeline.Name).To(Equal("pipeline-1"))
		})

		It("starts the pipeline again when the Job named in status has gone", func() {
			progress.Jobs = nil

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StartPipeline))
			Expect(decision.Restart).To(BeFalse())
			Expect(decision.Pipeline.Name).To(Equal("pipeline-1"))
		})

		It("ignores Jobs belonging to anything other than the current pipeline", func() {
			progress.Jobs = []batchv1.Job{newFinishedJob("a-job-from-somewhere-else")}

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.StartPipeline))
		})
	})

	When("the current pipeline has already failed", func() {
		BeforeEach(func() {
			progress.Pipelines[0].Phase = v1alpha1.WorkflowPhaseFailed
		})

		It("reports the failure rather than starting anything", func() {
			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.FailCurrentPipeline))
			Expect(decision.Pipeline.Name).To(Equal("pipeline-1"))
		})

		It("reports the failure for a delete pipeline too", func() {
			opts.Resources = []v1alpha1.PipelineJobResources{
				newPipelineResources("pipeline-1", "hash-1", v1alpha1.WorkflowActionDelete),
			}
			progress.Pipelines = progress.Pipelines[:1]

			decision := workflow.DecideNextAction(opts, progress)

			Expect(decision.Action).To(Equal(workflow.FailCurrentPipeline))
			Expect(decision.Pipeline.WorkflowAction).To(Equal(v1alpha1.WorkflowActionDelete))
		})
	})
})
