package pipeline

import (
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/uuid"
)

func readerContainerAndVolume(rr *unstructured.Unstructured) (v1.Container, v1.Volume) {
	resourceKindNameNamespace := fmt.Sprintf("%s.%s %s --namespace %s",
		strings.ToLower(rr.GetKind()), rr.GroupVersionKind().Group, rr.GetName(), rr.GetNamespace())

	resourceRequestCommand := fmt.Sprintf("kubectl get %s -oyaml > /output/object.yaml", resourceKindNameNamespace)
	container := v1.Container{
		Name:    "reader",
		Image:   "bitnami/kubectl:1.20.10",
		Command: []string{"sh", "-c", resourceRequestCommand},
		VolumeMounts: []v1.VolumeMount{
			{
				MountPath: "/output",
				Name:      "vol0",
			},
		},
	}

	volume := v1.Volume{Name: "vol0", VolumeSource: v1.VolumeSource{EmptyDir: &v1.EmptyDirVolumeSource{}}}
	return container, volume
}

func SharedLabels(resourceRequestIdentifier, promiseIdentifier string) map[string]string {
	return map[string]string{
		"kratix-promise-id":                  promiseIdentifier,
		"kratix-promise-resource-request-id": resourceRequestIdentifier,
	}
}

func pipelineLabels(pipelineType, resourceRequestIdentifier, promiseIdentifier string) map[string]string {
	labels := SharedLabels(resourceRequestIdentifier, promiseIdentifier)
	labels["kratix-pipeline-type"] = pipelineType
	return labels
}

func pipelineName(pipelineType, promiseIdentifier string) string {
	return pipelineType + "-pipeline-" + promiseIdentifier + "-" + getShortUuid()
}

func getShortUuid() string {
	return string(uuid.NewUUID()[0:5])
}
