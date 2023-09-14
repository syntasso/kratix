package pipeline

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/hash"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	kratixConfigureOperation = "configure"
)

func NewConfigureResource(
	rr *unstructured.Unstructured,
	//TODO change to plural string
	crdNames apiextensionsv1.CustomResourceDefinitionNames,
	pipelines []platformv1alpha1.Pipeline,
	resourceRequestIdentifier,
	promiseIdentifier string,
	promiseDestinationSelectors []platformv1alpha1.Selector,
	logger logr.Logger,
) ([]client.Object, error) {

	pipelineResources := NewPipelineArgs(promiseIdentifier, resourceRequestIdentifier, rr.GetNamespace())
	destinationSelectorsConfigMap, err := destinationSelectorsConfigMap(pipelineResources, promiseDestinationSelectors)
	if err != nil {
		return nil, err
	}

	pipeline, err := ConfigureResourcePipeline(rr, pipelines, pipelineResources, promiseIdentifier)
	if err != nil {
		return nil, err
	}

	resources := []client.Object{
		serviceAccount(pipelineResources),
		role(rr, crdNames.Plural, pipelineResources),
		roleBinding((pipelineResources)),
		destinationSelectorsConfigMap,
		pipeline,
	}

	return resources, nil
}

func NewConfigurePromise(
	unstructedPromise *unstructured.Unstructured,
	pipelines []platformv1alpha1.Pipeline,
	promiseIdentifier string,
	promiseDestinationSelectors []platformv1alpha1.Selector,
	logger logr.Logger,
) ([]client.Object, error) {

	pipelineResources := NewPipelineArgs(promiseIdentifier, "", "kratix-platform-system")
	destinationSelectorsConfigMap, err := destinationSelectorsConfigMap(pipelineResources, promiseDestinationSelectors)
	if err != nil {
		return nil, err
	}

	pipeline, err := ConfigureResourcePipeline(unstructedPromise, pipelines, pipelineResources, promiseIdentifier)
	if err != nil {
		return nil, err
	}

	resources := []client.Object{
		serviceAccount(pipelineResources),
		role(unstructedPromise, "promises", pipelineResources),
		roleBinding(pipelineResources),
		destinationSelectorsConfigMap,
		pipeline,
	}

	return resources, nil
}

func ConfigureResourcePipeline(rr *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline, pipelineResources PipelineArgs, promiseName string) (*batchv1.Job, error) {
	volumes := metadataAndSchedulingVolumes(pipelineResources.ConfigMapName())

	initContainers, pipelineVolumes := configurePipelineInitContainers(rr, pipelines, promiseName)
	volumes = append(volumes, pipelineVolumes...)

	rrKind := fmt.Sprintf("%s.%s", strings.ToLower(rr.GetKind()), rr.GroupVersionKind().Group)
	rrSpecHash, err := hash.ComputeHash(rr)
	if err != nil {
		return nil, err
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pipelineResources.ConfigurePipelineName(),
			Namespace: rr.GetNamespace(),
			Labels:    pipelineResources.ConfigurePipelinePodLabels(rrSpecHash),
		},
		Spec: batchv1.JobSpec{
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: pipelineResources.ConfigurePipelinePodLabels(rrSpecHash),
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
			},
		},
	}, nil
}

func configurePipelineInitContainers(rr *unstructured.Unstructured, pipelines []platformv1alpha1.Pipeline, promiseName string) ([]v1.Container, []v1.Volume) {
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

	workCreatorCommand := fmt.Sprintf("./work-creator -input-directory /work-creator-files -promise-name %s -namespace %s -resource-name %s", promiseName, rr.GetNamespace(), rr.GetName())
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
						Key:  "destinationSelectors",
						Path: "promise-scheduling",
					}},
				},
			},
		},
	}
}
