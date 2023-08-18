package pipeline

type pipelineLabels map[string]string

const (
	configurePipelineType   = "configure"
	deletePipelineType      = "delete"
	KratixResourceHashLabel = "kratix-resource-hash"
)

func newPipelineLabels() pipelineLabels {
	return make(map[string]string)
}

func DeletePipelineLabels(rrID, promiseID string) map[string]string {
	return Labels(rrID, promiseID).
		WithPipelineType(deletePipelineType)
}

func ConfigurePipelineLabels(rrID, promiseID string, requestSHA ...string) map[string]string {
	return Labels(rrID, promiseID).
		WithPipelineType(configurePipelineType).
		WithRequestSHA(requestSHA)
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

func (p pipelineLabels) WithRequestSHA(requestSHA []string) pipelineLabels {
	if len(requestSHA) == 0 {
		return p
	}
	p[KratixResourceHashLabel] = requestSHA[0]
	return p
}
