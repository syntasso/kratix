package pipeline

import (
	"strconv"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const kratixDeleteOperation = "delete"

func NewDeletePipeline(rr *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline, resourceRequestIdentifier, promiseIdentifier string) v1.Pod {
	containers, pipelineVolumes := deletePipelineContainers(rr, pipelines, resourceRequestIdentifier)

	pod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deletePipelineName(promiseIdentifier),
			Namespace: "default",
			Labels:    DeletePipelineLabels(resourceRequestIdentifier, promiseIdentifier),
		},
		Spec: v1.PodSpec{
			RestartPolicy:      v1.RestartPolicyOnFailure,
			ServiceAccountName: promiseIdentifier + "-promise-pipeline",
			Containers:         []v1.Container{containers[len(containers)-1]},
			InitContainers:     containers[0 : len(containers)-1],
			Volumes:            pipelineVolumes,
		},
	}

	return pod
}

func deletePipelineContainers(rr *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline, rrID string) ([]v1.Container, []v1.Volume) {
	readerContainer, readerVolume := readerContainerAndVolume(rr)
	containers := []v1.Container{
		readerContainer,
	}
	volumes := []v1.Volume{
		readerVolume, // vol0
	}

	if len(pipelines) > 0 {
		//TODO: We only support 1 workflow for now
		for i, c := range pipelines[0].Spec.Containers {
			volumes = append(volumes, v1.Volume{
				Name:         "vol" + strconv.Itoa(i+1),
				VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}},
			})

			containers = append(containers, v1.Container{
				Name:  c.Name,
				Image: c.Image,
				VolumeMounts: []v1.VolumeMount{
					{Name: "vol" + strconv.Itoa(i), MountPath: "/input"},
					{Name: "vol" + strconv.Itoa(i+1), MountPath: "/output"},
				},
				Env: []v1.EnvVar{
					{
						Name:  kratixOperationEnvVar,
						Value: kratixDeleteOperation,
					},
				},
			})
		}
	}

	return containers, volumes
}

func DeletePipelineLabels(resourceRequestIdentifier, promiseIdentifier string) map[string]string {
	return newPipelineLabels().
		WithPromiseID(promiseIdentifier).
		WithResourceRequestID(resourceRequestIdentifier).
		WithPipelineType("delete")
}

func deletePipelineName(promiseIdentifier string) string {
	return pipelineName("delete", promiseIdentifier)
}
