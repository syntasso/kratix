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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	kratixConfigureOperation = "configure"
	configurePipelineType    = "configure"
)

func NewConfigurePipeline(
	rr *unstructured.Unstructured,
	pipelines []platformv1alpha1.Pipeline,
	resourceRequestIdentifier,
	promiseIdentifier string,
	scheduling []platformv1alpha1.SchedulingConfig,
) ([]client.Object, error) {

	pipelineResources := newPipelineArgs(promiseIdentifier, resourceRequestIdentifier, rr.GetNamespace())
	configMap, err := configMap(pipelineResources, scheduling)
	if err != nil {
		return nil, err
	}

	resources := []client.Object{
		serviceAccount(pipelineResources),
		role(rr, pipelineResources),
		roleBinding((pipelineResources)),
		configMap,
		configurePipelinePod(rr, pipelines, pipelineResources),
	}

	return resources, nil
}

func configurePipelinePod(rr *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline, pipelineResources pipelineArgs) *v1.Pod {
	volumes := metadataAndSchedulingVolumes(pipelineResources.ConfigMapName())

	initContainers, pipelineVolumes := configurePipelineInitContainers(rr, pipelines, pipelineResources.ResourceRequestID())
	volumes = append(volumes, pipelineVolumes...)

	rrKind := fmt.Sprintf("%s.%s", strings.ToLower(rr.GetKind()), rr.GroupVersionKind().Group)
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pipelineResources.ConfigurePipelineName(),
			Namespace: rr.GetNamespace(),
			Labels:    pipelineResources.PipelinePodLabels(),
		},
		Spec: v1.PodSpec{
			RestartPolicy:      v1.RestartPolicyOnFailure,
			ServiceAccountName: pipelineResources.ServiceAccountName(),
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
				Env: []v1.EnvVar{
					{
						Name:  kratixOperationEnvVar,
						Value: kratixConfigureOperation,
					},
				},
			})
		}
	}

	workCreatorCommand := fmt.Sprintf("./work-creator -identifier %s -input-directory /work-creator-files -namespace %s", rrID, rr.GetNamespace())
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

func metadataAndSchedulingVolumes(configMapName string) []v1.Volume {
	return []v1.Volume{
		{
			Name: "metadata", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}},
		},
		{
			Name: "promise-scheduling",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: configMapName,
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
