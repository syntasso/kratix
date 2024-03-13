package resourceutil

import (
	"context"
	"fmt"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const MAX_NAME_LENGTH = 63 - 1 - 5 //five char sha, plus the separator

func GenerateObjectName(name string) string {
	if len(name) > MAX_NAME_LENGTH {
		name = name[0 : MAX_NAME_LENGTH-1]
	}

	id := uuid.NewUUID()

	return name + "-" + string(id[0:5])
}

func getExistingWorks(k8sClient client.Client, namespace, promiseName, resourceName, pipelineName string) ([]v1alpha1.Work, error) {
	l := map[string]string{
		"kratix.io/promise-name": promiseName,
	}

	if pipelineName != "" {
		l["kratix.io/pipeline-name"] = pipelineName
	}
	if resourceName != "" {
		l["kratix.io/resource-name"] = resourceName
	}

	workSelectorLabel := labels.FormatLabels(l)
	selector, err := labels.Parse(workSelectorLabel)
	if err != nil {
		return nil, err
	}
	works := v1alpha1.WorkList{}
	err = k8sClient.List(context.Background(), &works, &client.ListOptions{
		LabelSelector: selector,
		Namespace:     namespace,
	})
	if err != nil {
		return nil, err
	}

	return works.Items, nil
}

func GetAllWorksForResource(k8sClient client.Client, namespace, promiseName, resourceName string) ([]v1alpha1.Work, error) {
	return getExistingWorks(k8sClient, namespace, promiseName, resourceName, "")
}

func GetAllWorksForPromise(k8sClient client.Client, namespace, promiseName string) ([]v1alpha1.Work, error) {
	return getExistingWorks(k8sClient, namespace, promiseName, "", "")
}

func GetExistingWorkForResource(k8sClient client.Client, namespace, promiseName, resourceName, pipelineName string) (*v1alpha1.Work, error) {
	works, err := getExistingWorks(k8sClient, namespace, promiseName, resourceName, pipelineName)
	if err != nil {
		return nil, err
	}

	//TODO test
	if len(works) > 1 {
		return nil, fmt.Errorf("more than 1 work exist with the matching labels for Promise: %q, Resource: %q, Pipeline: %q. unable to update",
			promiseName, resourceName, pipelineName)
	}

	if len(works) == 0 {
		return nil, nil
	}

	return &works[0], nil
}

func GetExistingWorkForPromise(k8sClient client.Client, namespace, promiseName, pipelineName string) (*v1alpha1.Work, error) {
	return GetExistingWorkForResource(k8sClient, namespace, promiseName, "", pipelineName)
}
