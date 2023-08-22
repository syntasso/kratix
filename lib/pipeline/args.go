package pipeline

type PipelineArgs struct {
	names map[string]string
}

func NewPipelineArgs(promiseIdentifier, resourceRequestIdentifier, namespace string) PipelineArgs {
	names := map[string]string{
		"configure-pipeline-name": pipelineName("configure", promiseIdentifier),
		"delete-pipeline-name":    pipelineName("delete", promiseIdentifier),
		"promise-id":              promiseIdentifier,
		"resource-request-id":     resourceRequestIdentifier,
		"service-account":         promiseIdentifier + "-promise-pipeline",
		"role":                    promiseIdentifier + "-promise-pipeline",
		"role-binding":            promiseIdentifier + "-promise-pipeline",
		"config-map":              "destination-selectors-" + promiseIdentifier,
		"namespace":               namespace,
	}
	return PipelineArgs{
		names: names,
	}
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

func (p PipelineArgs) ResourceRequestID() string {
	return p.names["resource-request-id"]
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

func (p PipelineArgs) ConfigurePipelinePodLabels(rrSHA string) pipelineLabels {
	return ConfigurePipelineLabels(p.ResourceRequestID(), p.PromiseID(), rrSHA)
}

func (p PipelineArgs) DeletePipelinePodLabels() pipelineLabels {
	return DeletePipelineLabels(p.ResourceRequestID(), p.PromiseID())
}
