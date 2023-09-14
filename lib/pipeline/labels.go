package pipeline

type pipelineLabels map[string]string

const (
	configureAction         = "configure"
	deleteAction            = "delete"
	resourceType            = "resource"
	promiseType             = "promise"
	KratixResourceHashLabel = "kratix-resource-hash"
)

func newPipelineLabels() pipelineLabels {
	return make(map[string]string)
}

func LabelsForAllResourceWorkflows(rrID, promiseID string) map[string]string {
	return ResourceLabels(rrID, promiseID).
		WithWorkflow(resourceType, "")
}

func LabelsForDeleteResource(rrID, promiseID string, requestSHA ...string) map[string]string {
	labels := ResourceLabels(rrID, promiseID).WithWorkflow(resourceType, deleteAction)
	if len(requestSHA) > 0 {
		return labels.WithRequestSHA(requestSHA[0])
	}
	return labels
}

func LabelsForConfigureResource(rrID, promiseID string, requestSHA ...string) map[string]string {
	labels := ResourceLabels(rrID, promiseID).WithWorkflow(resourceType, configureAction)
	if len(requestSHA) > 0 {
		return labels.WithRequestSHA(requestSHA[0])
	}
	return labels
}

func LabelsForConfigurePromise(promiseID string, requestSHA ...string) map[string]string {
	labels := PromiseLabels(promiseID).WithWorkflow(promiseType, configureAction)
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

func (p pipelineLabels) WithWorkflow(workflowType, workflowAction string) pipelineLabels {
	p["kratix-workflow-kind"] = "pipeline.platform.kratix.io"
	p["kratix-workflow-promise-version"] = "v1alpha1"
	p["kratix-workflow-type"] = workflowType
	if workflowAction != "" {
		p["kratix-workflow-action"] = workflowAction
	}
	return p
}

func (p pipelineLabels) WithRequestSHA(requestSHA string) pipelineLabels {
	p[KratixResourceHashLabel] = requestSHA
	return p
}
