package resourceutil

import (
	"context"
	"fmt"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// labels[WorkPromiseNameLabel] = promiseName
// labels[WorkResourceNameLabel] = resourceName
// labels[WorkPipelineNameLabel] = pipelineName
// labels[WorkTypeLabel] = v1alpha1.WorkTypeResource

func SetPromiseWorkLabels(l map[string]string, promiseName, pipelineName string) {
	setWorkLabels(l, promiseName, "", pipelineName)
}

func SetResourceWorkLabels(l map[string]string, promiseName, resourceName, pipelineName string) {
	setWorkLabels(l, promiseName, resourceName, pipelineName)
}

func SetStaticDependencyWorkLabels(l map[string]string, promiseName string) {
	setWorkLabels(l, promiseName, "", "")
}

func setWorkLabels(l map[string]string, promiseName, resourceName, pipelineName string) {
	l[v1alpha1.PromiseNameLabel] = promiseName
	l[v1alpha1.WorkTypeLabel] = v1alpha1.WorkTypeStaticDependency

	if pipelineName != "" {
		l[v1alpha1.PipelineNameLabel] = pipelineName
		l[v1alpha1.WorkTypeLabel] = v1alpha1.WorkTypePromise
	}

	if resourceName != "" {
		l[v1alpha1.ResourceNameLabel] = resourceName
		l[v1alpha1.WorkTypeLabel] = v1alpha1.WorkTypeResource
	}
}

func GetAllWorksForResource(k8sClient client.Client, namespace, promiseName, resourceName string) ([]v1alpha1.Work, error) {
	return getExistingWorks(k8sClient, namespace, promiseName, resourceName, "")
}

func GetAllWorksForPromise(k8sClient client.Client, namespace, promiseName string) ([]v1alpha1.Work, error) {
	return getExistingWorks(k8sClient, namespace, promiseName, "", "")
}

func GetWorkForStaticDependencies(k8sClient client.Client, namespace, promiseName string) (*v1alpha1.Work, error) {
	return GetWorkForResourcePipeline(k8sClient, namespace, promiseName, "", "")
}

func GetWorkForResourcePipeline(k8sClient client.Client, namespace, promiseName, resourceName, pipelineName string) (*v1alpha1.Work, error) {
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

func GetWorkForPromisePipeline(k8sClient client.Client, namespace, promiseName, pipelineName string) (*v1alpha1.Work, error) {
	return GetWorkForResourcePipeline(k8sClient, namespace, promiseName, "", pipelineName)
}

func getExistingWorks(k8sClient client.Client, namespace, promiseName, resourceName, pipelineName string) ([]v1alpha1.Work, error) {
	l := map[string]string{}
	setWorkLabels(l, promiseName, resourceName, pipelineName)
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
