package pipeline

import (
	"github.com/syntasso/kratix/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const kratixActionDelete = "delete"

func NewDeleteResource(rr *unstructured.Unstructured, pipeline v1alpha1.Pipeline, resourceRequestIdentifier, promiseIdentifier, crdPlural string) []client.Object {
	return NewDelete(rr, pipeline, resourceRequestIdentifier, promiseIdentifier, crdPlural)
}

func NewDeletePromise(promise *unstructured.Unstructured, pipeline v1alpha1.Pipeline) []client.Object {
	return NewDelete(promise, pipeline, "", promise.GetName(), v1alpha1.PromisePlural)
}

func NewDelete(obj *unstructured.Unstructured, pipeline v1alpha1.Pipeline, resourceRequestIdentifier, promiseIdentifier, objPlural string) []client.Object {
	isPromise := resourceRequestIdentifier == ""
	namespace := obj.GetNamespace()
	if isPromise {
		namespace = v1alpha1.SystemNamespace
	}

	args := NewPipelineArgs(promiseIdentifier, resourceRequestIdentifier, pipeline.Name, obj.GetName(), namespace)

	containers, pipelineVolumes := generateDeletePipelineContainersAndVolumes(obj, isPromise, pipeline)

	imagePullSecrets := pipeline.Spec.ImagePullSecrets

	resources := []client.Object{
		serviceAccount(args),
		role(obj, objPlural, args),
		roleBinding(args),
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      args.DeletePipelineName(),
				Namespace: args.Namespace(),
				Labels:    args.DeletePipelineJobLabels(),
			},
			Spec: batchv1.JobSpec{
				Template: v1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: args.DeletePipelineJobLabels(),
					},
					Spec: v1.PodSpec{
						RestartPolicy:      v1.RestartPolicyOnFailure,
						ServiceAccountName: args.ServiceAccountName(),
						Containers:         []v1.Container{containers[len(containers)-1]},
						InitContainers:     containers[0 : len(containers)-1],
						Volumes:            pipelineVolumes,
						ImagePullSecrets:   imagePullSecrets,
					},
				},
			},
		},
	}

	return resources
}

func generateDeletePipelineContainersAndVolumes(obj *unstructured.Unstructured, isPromise bool, pipeline v1alpha1.Pipeline) ([]v1.Container, []v1.Volume) {
	kratixEnvVars := []v1.EnvVar{
		{
			Name:  kratixActionEnvVar,
			Value: kratixActionDelete,
		},
	}

	workflowType := v1alpha1.WorkflowTypeResource
	if isPromise {
		workflowType = v1alpha1.WorkflowTypePromise
	}

	return generateContainersAndVolumes(obj, workflowType, pipeline, kratixEnvVars)
}
