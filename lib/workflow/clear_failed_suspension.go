package workflow

import (
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ClearFailedSuspension removes the workflow-suspended label, and returns true, when
// the Job that added it failed before writing the Suspended phase. Left in place, the
// label stops the controllers reading that Job, so the failure is never reported.
func ClearFailedSuspension(opts Opts) (bool, error) {
	if !hasWorkflowSuspendedLabel(opts.parentObject) || len(opts.Resources) == 0 {
		return false, nil
	}

	entries, _, err := unstructured.NestedSlice(opts.parentObject.Object,
		"status", "kratix", "workflows", opts.workflowKey(), "pipelines")
	if err != nil {
		return false, err
	}
	var pipelines []v1alpha1.WorkflowPipelineStatus
	for _, entry := range entries {
		if pipeline, err := pipelineStatusFromUnstructured(entry); err == nil {
			pipelines = append(pipelines, pipeline)
		}
	}
	index, found := firstUnfinishedPipeline(pipelines)
	if !found || pipelines[index].Phase != v1alpha1.WorkflowPhaseRunning {
		return false, nil
	}

	jobs, err := getWorkflowJobs(opts)
	if err != nil {
		return false, err
	}
	if !isFailed(findJobByName(jobs, pipelines[index].Job)) {
		return false, nil
	}

	labels := opts.parentObject.GetLabels()
	delete(labels, v1alpha1.WorkflowSuspendedLabel)
	opts.parentObject.SetLabels(labels)
	if err := opts.client.Update(opts.ctx, opts.parentObject); err != nil {
		return false, err
	}
	logging.Warn(opts.logger, "the pipeline Job that suspended this workflow failed before recording the suspension; "+
		"removed the workflow-suspended label so the failure is recorded", "job", pipelines[index].Job)
	return true, nil
}
