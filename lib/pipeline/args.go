package pipeline

type PipelineArgs struct {
	names map[string]string
}

func NewPipelineArgs(promiseIdentifier, resourceRequestIdentifier, pName, objectName, namespace string) PipelineArgs {
	pipelineID := promiseIdentifier + "-promise-pipeline"
	if resourceRequestIdentifier != "" {
		pipelineID = promiseIdentifier + "-resource-pipeline"
	}

	names := map[string]string{
		"configure-pipeline-name": pipelineName(promiseIdentifier, resourceRequestIdentifier, objectName, pName),
		"delete-pipeline-name":    pipelineName(promiseIdentifier, resourceRequestIdentifier, objectName, pName),
		"promise-id":              promiseIdentifier,
		"service-account":         pipelineID,
		"role":                    pipelineID,
		"role-binding":            pipelineID,
		"config-map":              "destination-selectors-" + promiseIdentifier,
		"resource-request-id":     resourceRequestIdentifier,
		"namespace":               namespace,
		"pipeline-name":           pName,
		"name":                    objectName,
	}

	return PipelineArgs{
		names: names,
	}
}

func (p PipelineArgs) ConfigurePipelineJobLabels(objHash string) pipelineLabels {
	resourceRequestID := p.names["resource-request-id"]
	if resourceRequestID == "" {
		return LabelsForConfigurePromise(p.PromiseID(), p.PipelineName(), objHash)
	}
	return LabelsForConfigureResource(resourceRequestID, p.Name(), p.PromiseID(), p.PipelineName(), objHash)
}

func (p PipelineArgs) DeletePipelineJobLabels() pipelineLabels {
	resourceRequestID := p.names["resource-request-id"]
	if resourceRequestID == "" {
		return LabelsForDeletePromise(p.PromiseID(), p.PipelineName())
	}
	return LabelsForDeleteResource(resourceRequestID, p.Name(), p.PromiseID(), p.PipelineName())
}

func (p PipelineArgs) ConfigMapName() string {
	return p.names["config-map"]
}

func (p PipelineArgs) ServiceAccountName() string {
	return p.names["service-account"]
}

func (p PipelineArgs) RoleName() string {
	return p.names["role"]
}

func (p PipelineArgs) Name() string {
	return p.names["name"]
}

func (p PipelineArgs) RoleBindingName() string {
	return p.names["role-binding"]
}

func (p PipelineArgs) Namespace() string {
	return p.names["namespace"]
}

func (p PipelineArgs) PromiseID() string {
	return p.names["promise-id"]
}

func (p PipelineArgs) ConfigurePipelineName() string {
	return p.names["configure-pipeline-name"]
}

func (p PipelineArgs) DeletePipelineName() string {
	return p.names["delete-pipeline-name"]
}

func (p PipelineArgs) PipelineName() string {
	return p.names["pipeline-name"]
}

func (p PipelineArgs) Labels() pipelineLabels {
	return newPipelineLabels().WithPromiseID(p.PromiseID())
}
