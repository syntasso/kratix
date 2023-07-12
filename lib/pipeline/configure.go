package pipeline

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func NewConfigurePipelinePod(rr *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline, resourceRequestIdentifier, promiseIdentifier string) v1.Pod {
	volumes := metadataAndSchedulingVolumes(promiseIdentifier)

	initContainers, pipelineVolumes := configurePipelineInitContainers(rr, pipelines, resourceRequestIdentifier)
	volumes = append(volumes, pipelineVolumes...)

	rrKind := fmt.Sprintf("%s.%s", strings.ToLower(rr.GetKind()), rr.GroupVersionKind().Group)
	pod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configurePipelineName(promiseIdentifier),
			Namespace: "default",
			Labels:    ConfigurePipelineLabels(resourceRequestIdentifier, promiseIdentifier),
		},
		Spec: v1.PodSpec{
			RestartPolicy:      v1.RestartPolicyOnFailure,
			ServiceAccountName: promiseIdentifier + "-promise-pipeline",
			Containers: []v1.Container{
				{
					Name:    "status-writer",
					Image:   os.Getenv("WC_IMG"),
					Command: []string{"sh", "-c", "update-status"},
					Env: []v1.EnvVar{
						{Name: "RR_KIND", Value: rrKind},
						{Name: "RR_NAME", Value: rr.GetName()},
						{Name: "RR_NAMESPACE", Value: rr.GetNamespace()},
					},
					VolumeMounts: []v1.VolumeMount{{
						MountPath: "/work-creator-files/metadata",
						Name:      "metadata",
					}},
				},
			},
			InitContainers: initContainers,
			Volumes:        volumes,
		},
	}

	return pod
}

func configurePipelineInitContainers(rr *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline, rrID string) ([]v1.Container, []v1.Volume) {
	metadataVolumeMount := v1.VolumeMount{MountPath: "/metadata", Name: "metadata"}

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
					metadataVolumeMount,
					{Name: "vol" + strconv.Itoa(i), MountPath: "/input"},
					{Name: "vol" + strconv.Itoa(i+1), MountPath: "/output"},
				},
			})
		}
	}

	workCreatorCommand := fmt.Sprintf("./work-creator -identifier %s -input-directory /work-creator-files", rrID)
	writer := v1.Container{
		Name:    "work-writer",
		Image:   os.Getenv("WC_IMG"),
		Command: []string{"sh", "-c", workCreatorCommand},
		VolumeMounts: []v1.VolumeMount{
			{
				MountPath: "/work-creator-files/input",
				Name:      "vol" + strconv.Itoa(len(containers)-1),
			},
			{
				MountPath: "/work-creator-files/metadata",
				Name:      "metadata",
			},
			{
				MountPath: "/work-creator-files/kratix-system",
				Name:      "promise-scheduling",
			},
		},
	}

	containers = append(containers, writer)

	return containers, volumes
}

func metadataAndSchedulingVolumes(promiseIdentifier string) []v1.Volume {
	return []v1.Volume{
		{
			Name: "metadata", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}},
		},
		{
			Name: "promise-scheduling",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: "scheduling-" + promiseIdentifier,
					},
					Items: []v1.KeyToPath{{
						Key:  "scheduling",
						Path: "promise-scheduling",
					}},
				},
			},
		},
	}
}

func configurePipelineName(promiseIdentifier string) string {
	return pipelineName("configure", promiseIdentifier)
}

func ConfigurePipelineLabels(resourceRequestIdentifier, promiseIdentifier string) map[string]string {
	return pipelineLabels("configure", resourceRequestIdentifier, promiseIdentifier)
}
