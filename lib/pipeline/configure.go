package pipeline

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/hash"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func NewConfigureResource(
	rr *unstructured.Unstructured,
	crdPlural string,
	pipelines []v1alpha1.Pipeline,
	resourceRequestIdentifier,
	promiseIdentifier string,
	promiseDestinationSelectors []v1alpha1.PromiseScheduling,
	logger logr.Logger,
) ([]client.Object, error) {

	pipelineResources := NewPipelineArgs(promiseIdentifier, resourceRequestIdentifier, rr.GetNamespace())
	destinationSelectorsConfigMap, err := destinationSelectorsConfigMap(pipelineResources, promiseDestinationSelectors, nil)
	if err != nil {
		return nil, err
	}

	pipeline, err := ConfigurePipeline(rr, pipelines, pipelineResources, promiseIdentifier, false, logger)
	if err != nil {
		return nil, err
	}

	resources := []client.Object{
		serviceAccount(pipelineResources),
		role(rr, crdPlural, pipelineResources),
		roleBinding((pipelineResources)),
		destinationSelectorsConfigMap,
		pipeline,
	}

	return resources, nil
}

func NewConfigurePromise(
	unstructedPromise *unstructured.Unstructured,
	pipelines []v1alpha1.Pipeline,
	promiseIdentifier string,
	promiseDestinationSelectors []v1alpha1.PromiseScheduling,
	logger logr.Logger,
) ([]client.Object, error) {

	pipelineResources := NewPipelineArgs(promiseIdentifier, "", v1alpha1.SystemNamespace)
	destinationSelectorsConfigMap, err := destinationSelectorsConfigMap(pipelineResources, promiseDestinationSelectors, nil)
	if err != nil {
		return nil, err
	}

	pipeline, err := ConfigurePipeline(unstructedPromise, pipelines, pipelineResources, promiseIdentifier, true, logger)
	if err != nil {
		return nil, err
	}

	resources := []client.Object{
		serviceAccount(pipelineResources),
		clusterRole(pipelineResources),
		clusterRoleBinding(pipelineResources),
		destinationSelectorsConfigMap,
		pipeline,
	}

	return resources, nil
}

func ConfigurePipeline(obj *unstructured.Unstructured, pipelines []v1alpha1.Pipeline, pipelineArgs PipelineArgs, promiseName string, promiseWorkflow bool, logger logr.Logger) (*batchv1.Job, error) {
	volumes := metadataAndSchedulingVolumes(pipelineArgs.ConfigMapName())

	initContainers, pipelineVolumes := configurePipelineInitContainers(obj, pipelines, promiseName, promiseWorkflow, logger)
	volumes = append(volumes, pipelineVolumes...)

	objHash, err := hash.ComputeHashForResource(obj)
	if err != nil {
		return nil, err
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pipelineArgs.ConfigurePipelineName(),
			Namespace: pipelineArgs.Namespace(),
			Labels:    pipelineArgs.ConfigurePipelinePodLabels(objHash),
		},
		Spec: batchv1.JobSpec{
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: pipelineArgs.ConfigurePipelinePodLabels(objHash),
				},
				Spec: v1.PodSpec{
					RestartPolicy:      v1.RestartPolicyOnFailure,
					ServiceAccountName: pipelineArgs.ServiceAccountName(),
					Containers: []v1.Container{
						{
							Name:    "status-writer",
							Image:   os.Getenv("WC_IMG"),
							Command: []string{"sh", "-c", "update-status"},
							Env: []v1.EnvVar{
								{Name: "OBJECT_KIND", Value: strings.ToLower(obj.GetKind())},
								{Name: "OBJECT_GROUP", Value: obj.GroupVersionKind().Group},
								{Name: "OBJECT_NAME", Value: obj.GetName()},
								{Name: "OBJECT_NAMESPACE", Value: pipelineArgs.Namespace()},
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
	}

	if err := controllerutil.SetControllerReference(obj, job, scheme.Scheme); err != nil {
		logger.Error(err, "Error setting ownership")
		return nil, err
	}
	return job, nil
}

func configurePipelineInitContainers(obj *unstructured.Unstructured, pipelines []v1alpha1.Pipeline, promiseName string, promiseWorkflow bool, logger logr.Logger) ([]v1.Container, []v1.Volume) {
	volumes, volumeMounts := pipelineVolumes()

	kratixWorkflowType := v1alpha1.WorkflowTypeResource
	if promiseWorkflow {
		kratixWorkflowType = v1alpha1.WorkflowTypePromise
	}

	readerContainer := readerContainer(obj, kratixWorkflowType, "shared-input")
	containers := []v1.Container{
		readerContainer,
	}

	var pipelineName string
	if len(pipelines) > 0 {
		pipelineName = pipelines[0].Name
		//TODO: We only support 1 workflow for now
		if len(pipelines[0].Spec.Volumes) > 0 {
			volumes = append(volumes, pipelines[0].Spec.Volumes...)
		}
		for i, c := range pipelines[0].Spec.Containers {
			kratixEnvVars := []v1.EnvVar{
				{
					Name:  kratixActionEnvVar,
					Value: string(v1alpha1.WorkflowActionConfigure),
				},
				{
					Name:  kratixTypeEnvVar,
					Value: string(kratixWorkflowType),
				},
				{
					Name:  kratixPromiseEnvVar,
					Value: promiseName,
				},
			}
			if len(c.VolumeMounts) > 0 {
				volumeMounts = append(volumeMounts, c.VolumeMounts...)
			}
			containers = append(containers, v1.Container{
				Name:         providedOrDefaultName(c.Name, i),
				Image:        c.Image,
				VolumeMounts: volumeMounts,
				Args:         c.Args,
				Command:      c.Command,
				Env:          append(kratixEnvVars, c.Env...),
				EnvFrom:      c.EnvFrom,
			})
		}
	}

	workCreatorCommand := fmt.Sprintf("./work-creator -input-directory /work-creator-files -promise-name %s -pipeline-name %s", promiseName, pipelineName)
	logger.Info("workcreatorcommand before", "command", workCreatorCommand)

	if promiseWorkflow {
		workCreatorCommand += fmt.Sprintf(" -namespace %s -workflow-type %s", v1alpha1.SystemNamespace, v1alpha1.WorkflowTypePromise)
	} else {
		workCreatorCommand += fmt.Sprintf(" -namespace %s -resource-name %s -workflow-type %s", obj.GetNamespace(), obj.GetName(), v1alpha1.WorkflowTypeResource)
	}

	logger.Info("pipelines", "configure[0]", pipelines[0])
	logger.Info("workcreatorcommand after", "command", workCreatorCommand)

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

func providedOrDefaultName(providedName string, index int) string {
	if providedName == "" {
		return fmt.Sprintf("default-container-name-%d", index)
	}
	return providedName
}
