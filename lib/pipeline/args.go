package pipeline

type PipelineArgs struct {
	names map[string]string
}

func NewPipelineArgs(promiseIdentifier, resourceRequestIdentifier, namespace string) PipelineArgs {
	pipelineID := promiseIdentifier + "-promise-pipeline"
	if resourceRequestIdentifier != "" {
		pipelineID = promiseIdentifier + "-resource-pipeline"
	}

	names := map[string]string{
		"configure-pipeline-name": pipelineName("configure", promiseIdentifier),
		"delete-pipeline-name":    pipelineName("delete", promiseIdentifier),
		"promise-id":              promiseIdentifier,
		"service-account":         pipelineID,
		"role":                    pipelineID,
		"role-binding":            pipelineID,
		"config-map":              "destination-selectors-" + promiseIdentifier,
		"resource-request-id":     resourceRequestIdentifier,
		"namespace":               namespace,
	}

	return PipelineArgs{
		names: names,
	}
}

func (p PipelineArgs) ConfigurePipelinePodLabels(objHash string) pipelineLabels {
	resourceRequestID := p.names["resource-request-id"]
	if resourceRequestID == "" {
		return LabelsForConfigurePromise(p.PromiseID(), objHash)
	}
	return LabelsForConfigureResource(resourceRequestID, p.PromiseID(), objHash)
}

func (p PipelineArgs) DeletePipelinePodLabels() pipelineLabels {
	resourceRequestID := p.names["resource-request-id"]
	if resourceRequestID == "" {
		return LabelsForDeletePromise(p.PromiseID())
	}
	return LabelsForDeleteResource(resourceRequestID, p.PromiseID())
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

func (p PipelineArgs) Labels() pipelineLabels {
	return newPipelineLabels().WithPromiseID(p.PromiseID())
}
