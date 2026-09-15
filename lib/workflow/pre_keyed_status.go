package workflow

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// preKeyedStatusFields are the workflow status fields that older Kratix versions
// wrote directly under status.kratix.workflows instead of under a workflow key.
var preKeyedStatusFields = []string{"pipelines", "suspendedGeneration", "lastSuccessfulConfigureWorkflowTime"}

// RemovePreKeyedStatus deletes pre-keyed workflow status and requeues. The keyed
// CRD schema types workflows as a map of objects, so the apiserver prunes the old
// pipeline entries to empty objects on read — there is nothing left to migrate —
// and a leftover flat list makes later status writes fail validation on clusters
// without validation ratcheting. The workflows run again from the start.
func RemovePreKeyedStatus(opts Opts) (bool, error) {
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
	return true, opts.client.Status().Update(opts.ctx, parent)
}
