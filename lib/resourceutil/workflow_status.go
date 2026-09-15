package resourceutil

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// A workflow records what its pipelines have done under
// status.kratix.workflows.<workflow>, where <workflow> is "configure",
// "delete", or a key chosen by a controller that runs its own workflows.

func workflowStatusPath(workflow string, fields ...string) []string {
	return append([]string{"status", "kratix", "workflows", workflow}, fields...)
}

// GetPipelineStatuses returns each pipeline in the order it runs, and nil when
// the workflow has recorded nothing yet.
func GetPipelineStatuses(obj *unstructured.Unstructured, workflow string) ([]v1alpha1.WorkflowPipelineStatus, error) {
	path := workflowStatusPath(workflow, "pipelines")
	recorded, found, err := unstructured.NestedSlice(obj.Object, path...)
	if err != nil || !found {
		return nil, err
	}

	pipelines := make([]v1alpha1.WorkflowPipelineStatus, 0, len(recorded))
	for _, entry := range recorded {
		fields, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s of %s is not a list of pipelines", strings.Join(path, "."), obj.GetName())
		}
		var pipeline v1alpha1.WorkflowPipelineStatus
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(fields, &pipeline); err != nil {
			return nil, err
		}
		pipelines = append(pipelines, pipeline)
	}
	return pipelines, nil
}

func SetPipelineStatuses(obj *unstructured.Unstructured, workflow string, pipelines []v1alpha1.WorkflowPipelineStatus) error {
	// An object written with "status: null" has a status key holding nil, and
	// nothing can be written underneath it.
	if obj.Object["status"] == nil {
		obj.Object["status"] = map[string]any{}
	}

	entries, err := pipelineStatusEntries(pipelines)
	if err != nil {
		return err
	}
	return unstructured.SetNestedSlice(obj.Object, entries, workflowStatusPath(workflow, "pipelines")...)
}

func pipelineStatusEntries(pipelines []v1alpha1.WorkflowPipelineStatus) ([]any, error) {
	entries := make([]any, 0, len(pipelines))
	for i := range pipelines {
		fields, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pipelines[i])
		if err != nil {
			return nil, err
		}
		// A zero metav1.Time converts to nil, and the API server rejects a null
		// against the string schema of lastTransitionTime.
		for key, value := range fields {
			if value == nil {
				delete(fields, key)
			}
		}
		entries = append(entries, fields)
	}
	return entries, nil
}

// PipelineStatusesPatch returns a patch that sets only this workflow's pipeline
// statuses. A pipeline writes to the object's status from inside its Job, so
// writing the whole status back can put an earlier value in place of what the
// pipeline wrote.
func PipelineStatusesPatch(workflow string, pipelines []v1alpha1.WorkflowPipelineStatus) ([]byte, error) {
	entries, err := pipelineStatusEntries(pipelines)
	if err != nil {
		return nil, err
	}

	status := map[string]any{}
	if err := unstructured.SetNestedSlice(status, entries, workflowStatusPath(workflow, "pipelines")...); err != nil {
		return nil, err
	}
	return json.Marshal(status)
}

// GetSuspendedGeneration returns the generation the object was at when the
// workflow was suspended, or 0 when it is not suspended.
func GetSuspendedGeneration(obj *unstructured.Unstructured, workflow string) int64 {
	generation, found, err := unstructured.NestedInt64(obj.Object, workflowStatusPath(workflow, "suspendedGeneration")...)
	if err != nil || !found {
		return 0
	}
	return generation
}

func ClearSuspendedGeneration(obj *unstructured.Unstructured, workflow string) {
	unstructured.RemoveNestedField(obj.Object, workflowStatusPath(workflow, "suspendedGeneration")...)
}
