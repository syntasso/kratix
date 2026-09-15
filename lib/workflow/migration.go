package workflow

import (
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// MigrateStatus moves the old pipeline list into its workflow key and requeues.
func MigrateStatus(opts Opts) (bool, error) {
	if opts.workflowType != "promise" && opts.workflowType != "resource" {
		return false, nil
	}
	parent := opts.parentObject
	pipelines, found, err := unstructured.NestedSlice(parent.Object, "status", "kratix", "workflows", "pipelines")
	if err != nil || !found {
		return false, err
	}
	jobs, err := getJobsWithLabels(opts, labelsForJobs(opts), opts.namespace)
	if err != nil {
		return false, err
	}
	resourceutil.SortJobsByCreationDateTime(jobs, false)
	key := "configure"
	if len(jobs) > 0 {
		key = jobs[0].Labels[v1alpha1.WorkflowActionLabel]
	} else if !parent.GetDeletionTimestamp().IsZero() {
		key = "delete"
	}
	for _, entry := range pipelines {
		pipeline, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		for _, job := range jobs {
			if job.Labels[v1alpha1.WorkflowActionLabel] == key && pipeline["name"] == job.Labels[v1alpha1.PipelineNameLabel] {
				pipeline["hash"] = job.Labels[v1alpha1.KratixResourceHashLabel]
				break
			}
		}
	}
	if err := unstructured.SetNestedSlice(parent.Object, pipelines, "status", "kratix", "workflows", key, "pipelines"); err != nil {
		return false, err
	}
	unstructured.RemoveNestedField(parent.Object, "status", "kratix", "workflows", "pipelines")
	for _, field := range []string{"suspendedGeneration", "lastSuccessfulConfigureWorkflowTime"} {
		value, found, _ := unstructured.NestedFieldNoCopy(parent.Object, "status", "kratix", "workflows", field)
		if found {
			if err := unstructured.SetNestedField(parent.Object, value, "status", "kratix", "workflows", key, field); err != nil {
				return false, err
			}
			unstructured.RemoveNestedField(parent.Object, "status", "kratix", "workflows", field)
		}
	}
	return true, opts.client.Status().Update(opts.ctx, parent)
}
