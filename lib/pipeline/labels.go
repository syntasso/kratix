package pipeline

import (
	"github.com/syntasso/kratix/api/v1alpha1"
)

type pipelineLabels map[string]string
type action string

func newPipelineLabels() pipelineLabels {
	return make(map[string]string)
}

func LabelsForAllResourceWorkflows(rrID, promiseID string) map[string]string {
	return ResourceLabels(rrID, promiseID).
		WithWorkflow(v1alpha1.WorkflowTypeResource, "", "")
}

func LabelsForAllPromiseWorkflows(promiseID string) map[string]string {
	return PromiseLabels(promiseID).
		WithWorkflow(v1alpha1.WorkflowTypePromise, "", "")
}

func LabelsForDeleteResource(rrID, promiseID, pipelineName string, requestSHA ...string) map[string]string {
	labels := ResourceLabels(rrID, promiseID).WithWorkflow(v1alpha1.WorkflowTypeResource, v1alpha1.WorkflowActionDelete, pipelineName)
	if len(requestSHA) > 0 {
		return labels.WithRequestSHA(requestSHA[0])
	}
	return labels
}

func LabelsForConfigureResource(rrID, promiseID, pipelineName string, requestSHA ...string) map[string]string {
	labels := ResourceLabels(rrID, promiseID).WithWorkflow(v1alpha1.WorkflowTypeResource, v1alpha1.WorkflowActionConfigure, pipelineName)
	if len(requestSHA) > 0 {
		return labels.WithRequestSHA(requestSHA[0])
	}
	return labels
}

func LabelsForDeletePromise(promiseID, pipelineName string, requestSHA ...string) map[string]string {
	labels := PromiseLabels(promiseID).WithWorkflow(v1alpha1.WorkflowTypePromise, v1alpha1.WorkflowActionDelete, pipelineName)
	if len(requestSHA) > 0 {
		return labels.WithRequestSHA(requestSHA[0])
	}
	return labels
}

func LabelsForConfigurePromise(promiseID, pipelineName string, requestSHA ...string) map[string]string {
	labels := PromiseLabels(promiseID).WithWorkflow(v1alpha1.WorkflowTypePromise, v1alpha1.WorkflowActionConfigure, pipelineName)
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
	p[v1alpha1.PromiseNameLabel] = promiseID
	return p
}

func (p pipelineLabels) WithResourceRequestID(resourceRequestID string) pipelineLabels {
	p["kratix-promise-resource-request-id"] = resourceRequestID
	return p
}

func (p pipelineLabels) WithWorkflow(workflowType v1alpha1.Type, workflowAction v1alpha1.Action, pipelineName string) pipelineLabels {
	p["kratix-workflow-kind"] = "pipeline.platform.kratix.io"
	p["kratix-workflow-promise-version"] = "v1alpha1"
	p["kratix-workflow-type"] = string(workflowType)
	if workflowAction != "" {
		p["kratix-workflow-action"] = string(workflowAction)
	}
	if pipelineName != "" {
		p["kratix-workflow-pipeline-name"] = pipelineName
	}
	return p
}

func (p pipelineLabels) WithRequestSHA(requestSHA string) pipelineLabels {
	p[v1alpha1.KratixResourceHashLabel] = requestSHA
	return p
}
