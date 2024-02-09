package pipeline

import (
	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const kratixActionDelete = "delete"

func NewDeleteResource(rr *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline, resourceRequestIdentifier, promiseIdentifier, crdPlural string) []client.Object {
	return newDelete(rr, pipelines, resourceRequestIdentifier, promiseIdentifier, crdPlural)
}

func NewDeletePromise(promise *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline) []client.Object {
	return newDelete(promise, pipelines, "", promise.GetName(), platformv1alpha1.PromisePlural)
}

func newDelete(obj *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline, resourceRequestIdentifier, promiseIdentifier, objPlural string) []client.Object {
	isPromise := resourceRequestIdentifier == ""
	namespace := obj.GetNamespace()
	if isPromise {
		namespace = v1alpha1.SystemNamespace
	}

	args := NewPipelineArgs(promiseIdentifier, resourceRequestIdentifier, namespace)

	containers, pipelineVolumes := deletePipelineContainers(obj, isPromise, pipelines)

	resources := []client.Object{
		serviceAccount(args),
		role(obj, objPlural, args),
		roleBinding(args),
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      args.DeletePipelineName(),
				Namespace: args.Namespace(),
				Labels:    args.DeletePipelinePodLabels(),
			},
			Spec: batchv1.JobSpec{
				Template: v1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: args.DeletePipelinePodLabels(),
					},
					Spec: v1.PodSpec{
						RestartPolicy:      v1.RestartPolicyOnFailure,
						ServiceAccountName: args.ServiceAccountName(),
						Containers:         []v1.Container{containers[len(containers)-1]},
						InitContainers:     containers[0 : len(containers)-1],
						Volumes:            pipelineVolumes,
					},
				},
			},
		},
	}

	return resources
}

func deletePipelineContainers(obj *unstructured.Unstructured, isPromise bool, pipelines []platformv1alpha1.Pipeline) ([]v1.Container, []v1.Volume) {
	volumes, volumeMounts := pipelineVolumes()

	//TODO: Does this get called for promises too? If so, change the parameter name and dynamically set input below
	workflowType := platformv1alpha1.WorkflowTypeResource
	if isPromise {
		workflowType = platformv1alpha1.WorkflowTypePromise
	}

	readerContainer := readerContainer(obj, workflowType, "shared-input")
	containers := []v1.Container{
		readerContainer,
	}

	if len(pipelines) > 0 {
		//TODO: We only support 1 workflow for now
		for _, c := range pipelines[0].Spec.Containers {
			containers = append(containers, v1.Container{
				Name:         c.Name,
				Image:        c.Image,
				VolumeMounts: volumeMounts,
				Env: []v1.EnvVar{
					{
						Name:  kratixActionEnvVar,
						Value: kratixActionDelete,
					},
				},
			})
		}
	}

	return containers, volumes
}
