package pipeline

type pipelineLabels map[string]string

func newPipelineLabels() pipelineLabels {
	return make(map[string]string)
}

func Labels(promiseID, rrID string) pipelineLabels {
	return newPipelineLabels().WithPromiseID(promiseID).WithResourceRequestID(rrID)
}

func (p pipelineLabels) WithPromiseID(promiseID string) pipelineLabels {
	p["kratix-promise-id"] = promiseID
	return p
}

func (p pipelineLabels) WithResourceRequestID(resourceRequestID string) pipelineLabels {
	p["kratix-promise-resource-request-id"] = resourceRequestID
	return p
}

func (p pipelineLabels) WithPipelineType(pipelineType string) pipelineLabels {
	p["kratix-pipeline-type"] = pipelineType
	return p
}
