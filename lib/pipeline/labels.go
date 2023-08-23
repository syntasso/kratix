package pipeline

type pipelineLabels map[string]string

const (
	configurePipelineType = "configure"
	deletePipelineType    = "delete"
)

func newPipelineLabels() pipelineLabels {
	return make(map[string]string)
}

func DeletePipelineLabels(rrID, promiseID string) map[string]string {
	return Labels(rrID, promiseID).
		WithPipelineType(deletePipelineType)
}

func ConfigurePipelineLabels(rrID, promiseID string) map[string]string {
	labels := Labels(rrID, promiseID).WithPipelineType(configurePipelineType)
	return labels
}

func Labels(rrID, promiseID string) pipelineLabels {
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
