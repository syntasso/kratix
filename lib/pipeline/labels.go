package pipeline

import "github.com/syntasso/kratix/api/v1alpha1"

type pipelineLabels map[string]string
type action string

const (
	KratixResourceHashLabel = "kratix-resource-hash"
)

func newPipelineLabels() pipelineLabels {
	return make(map[string]string)
}

func LabelsForAllResourceWorkflows(rrID, promiseID string) map[string]string {
	return ResourceLabels(rrID, promiseID).
		WithWorkflow(v1alpha1.WorkflowTypeResource, "")
}

func LabelsForAllPromiseWorkflows(promiseID string) map[string]string {
	return PromiseLabels(promiseID).
		WithWorkflow(v1alpha1.WorkflowTypePromise, "")
}

func LabelsForDeleteResource(rrID, promiseID string, requestSHA ...string) map[string]string {
	labels := ResourceLabels(rrID, promiseID).WithWorkflow(v1alpha1.WorkflowTypeResource, v1alpha1.WorkflowActionDelete)
	if len(requestSHA) > 0 {
		return labels.WithRequestSHA(requestSHA[0])
	}
	return labels
}

func LabelsForConfigureResource(rrID, promiseID string, requestSHA ...string) map[string]string {
	labels := ResourceLabels(rrID, promiseID).WithWorkflow(v1alpha1.WorkflowTypeResource, v1alpha1.WorkflowActionConfigure)
	if len(requestSHA) > 0 {
		return labels.WithRequestSHA(requestSHA[0])
	}
	return labels
}

func LabelsForDeletePromise(promiseID string, requestSHA ...string) map[string]string {
	labels := PromiseLabels(promiseID).WithWorkflow(v1alpha1.WorkflowTypePromise, v1alpha1.WorkflowActionDelete)
	if len(requestSHA) > 0 {
		return labels.WithRequestSHA(requestSHA[0])
	}
	return labels
}

func LabelsForConfigurePromise(promiseID string, requestSHA ...string) map[string]string {
	labels := PromiseLabels(promiseID).WithWorkflow(v1alpha1.WorkflowTypePromise, v1alpha1.WorkflowActionConfigure)
	if len(requestSHA) > 0 {
		return labels.WithRequestSHA(requestSHA[0])
	}
	return labels
}

func ResourceLabels(rrID, promiseID string) pipelineLabels {
	return PromiseLabels(promiseID).WithResourceRequestID(rrID)
}

func PromiseLabels(promiseID string) pipelineLabels {
	return newPipelineLabels().WithPromiseID(promiseID)
}

func (p pipelineLabels) WithPromiseID(promiseID string) pipelineLabels {
	p["kratix-promise-id"] = promiseID
	return p
}

func (p pipelineLabels) WithResourceRequestID(resourceRequestID string) pipelineLabels {
	p["kratix-promise-resource-request-id"] = resourceRequestID
	return p
}

func (p pipelineLabels) WithWorkflow(workflowType v1alpha1.Type, workflowAction v1alpha1.Action) pipelineLabels {
	p["kratix-workflow-kind"] = "pipeline.platform.kratix.io"
	p["kratix-workflow-promise-version"] = "v1alpha1"
	p["kratix-workflow-type"] = string(workflowType)
	if workflowAction != "" {
		p["kratix-workflow-action"] = string(workflowAction)
	}
	return p
}

func (p pipelineLabels) WithRequestSHA(requestSHA string) pipelineLabels {
	p[KratixResourceHashLabel] = requestSHA
	return p
}
