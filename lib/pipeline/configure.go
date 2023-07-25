package pipeline

import (
	"fmt"
	"os"
	"strings"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	kratixConfigureOperation = "configure"
)

func NewConfigurePipeline(
	rr *unstructured.Unstructured,
	crdNames apiextensionsv1.CustomResourceDefinitionNames,
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
		role(rr, crdNames, pipelineResources),
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
			Labels:    pipelineResources.ConfigurePipelinePodLabels(),
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
						Name:      "shared-metadata",
					}},
				},
			},
			InitContainers: initContainers,
			Volumes:        volumes,
		},
	}
}

func configurePipelineInitContainers(rr *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline, rrID string) ([]v1.Container, []v1.Volume) {
	volumes, volumeMounts := pipelineVolumes()
	readerContainer := readerContainer(rr, "shared-input")
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
				Name:      "shared-output",
			},
			{
				MountPath: "/work-creator-files/metadata",
				Name:      "shared-metadata",
			},
			{
				MountPath: "/work-creator-files/kratix-system",
				Name:      "promise-scheduling", // this volumemount is a configmap
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
