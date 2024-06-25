package pipelineutil

import (
	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type PipelineJobResources interface {
	GetJob() *batchv1.Job
	GetRequiredResources() []client.Object
	GetName() string
}

type pipelineObjects struct {
	name                 string
	job                  *batchv1.Job
	jobRequiredResources []client.Object
}

func NewPipelineObjects(name string, job *batchv1.Job, jobRequiredResources []client.Object) PipelineJobResources {
	return &pipelineObjects{
		name:                 name,
		job:                  job,
		jobRequiredResources: jobRequiredResources,
	}
}

func (o *pipelineObjects) GetName() string {
	return o.name
}

func (o *pipelineObjects) GetJob() *batchv1.Job {
	return o.job
}

func (o *pipelineObjects) GetRequiredResources() []client.Object {
	return o.jobRequiredResources
}
