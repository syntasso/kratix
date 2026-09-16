package workflow

import (
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// preKeyedStatusFields were written straight under status.kratix.workflows before
// the status was keyed by workflow action.
var preKeyedStatusFields = []string{"pipelines", "suspendedGeneration", "lastSuccessfulConfigureWorkflowTime"}

// MigrateStatus removes the pre-keyed workflow status, rebuilds it from the Jobs
// still on the cluster, and requeues. The old status cannot be read, because the
// apiserver prunes it to empty objects against the keyed schema. A pipeline with no
// Job left is recorded as Pending, so it runs again.
func MigrateStatus(opts Opts) (bool, error) {
	if opts.workflowType != "promise" && opts.workflowType != "resource" {
		return false, nil
	}

	parent := opts.parentObject
	removed := false
	for _, field := range preKeyedStatusFields {
		if _, found, _ := unstructured.NestedFieldNoCopy(parent.Object, "status", "kratix", "workflows", field); found {
			unstructured.RemoveNestedField(parent.Object, "status", "kratix", "workflows", field)
			removed = true
		}
	}
	if !removed {
		return false, nil
	}

	if len(opts.Resources) == 0 {
		return true, opts.client.Status().Update(opts.ctx, parent)
	}

	jobs, err := getJobsWithLabels(opts, labelsForJobs(opts), opts.namespace)
	if err != nil {
		return false, err
	}

	pipelines := pipelineStatusFromJobs(opts, jobs)
	if err := unstructured.SetNestedSlice(parent.Object, pipelines,
		"status", "kratix", "workflows", opts.workflowKey(), "pipelines"); err != nil {
		return false, err
	}
	return true, opts.client.Status().Update(opts.ctx, parent)
}

// pipelineStatusFromJobs returns an entry per pipeline, in the order they run.
func pipelineStatusFromJobs(opts Opts, jobs []batchv1.Job) []any {
	resourceutil.SortJobsByCreationDateTime(jobs, false)
	action := string(opts.Resources[0].WorkflowAction)

	pipelines := make([]any, 0, len(opts.Resources))
	for _, pipeline := range opts.Resources {
		job := mostRecentJobForPipeline(jobs, action, pipeline.Name)
		if job == nil {
			pipelines = append(pipelines, map[string]any{
				"name":  pipeline.Name,
				"phase": v1alpha1.WorkflowPhasePending,
			})
			continue
		}
		pipelines = append(pipelines, map[string]any{
			"name":  pipeline.Name,
			"hash":  job.Labels[v1alpha1.KratixResourceHashLabel],
			"job":   job.Name,
			"phase": phaseForJob(job),
		})
	}
	return pipelines
}

// mostRecentJobForPipeline expects jobs to be sorted newest first.
func mostRecentJobForPipeline(jobs []batchv1.Job, action, pipelineName string) *batchv1.Job {
	for i := range jobs {
		jobLabels := jobs[i].Labels
		if jobLabels[v1alpha1.WorkflowActionLabel] == action && jobLabels[v1alpha1.PipelineNameLabel] == pipelineName {
			return &jobs[i]
		}
	}
	return nil
}

func phaseForJob(job *batchv1.Job) string {
	if isFailed(job) {
		return v1alpha1.WorkflowPhaseFailed
	}
	if isRunning(job) {
		return v1alpha1.WorkflowPhaseRunning
	}
	return v1alpha1.WorkflowPhaseSucceeded
}
