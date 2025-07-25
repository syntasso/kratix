package resourceutil

import (
	"context"
	"fmt"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetWorkLabels returns the labels for a work object.
// Those labels are set by Kratix in the Work Creator stage of the workflow.
// It can be used to filter works belonging to a particular workflow.
func GetWorkLabels(promiseName, resourceName, pipelineName, workType string) map[string]string {
	l := map[string]string{}
	l[v1alpha1.PromiseNameLabel] = promiseName
	l[v1alpha1.WorkTypeLabel] = workType

	if pipelineName != "" {
		l[v1alpha1.PipelineNameLabel] = pipelineName
	}

	if resourceName != "" {
		l[v1alpha1.ResourceNameLabel] = resourceName
	}
	return l
}

// GetAllWorksForResource returns the works for the specified resource
func GetAllWorksForResource(ctx context.Context, k8sClient client.Client, namespace, promiseName, resourceName string) ([]v1alpha1.Work, error) {
	workLabels := map[string]string{
		v1alpha1.PromiseNameLabel:  promiseName,
		v1alpha1.ResourceNameLabel: resourceName,
	}
	return getExistingWorks(ctx, k8sClient, namespace, workLabels)
}

// GetWorksByType returns the works for the specified workflow type
func GetWorksByType(ctx context.Context, k8sClient client.Client, workflowType v1alpha1.Type, obj *unstructured.Unstructured) ([]v1alpha1.Work, error) {
	namespace := obj.GetNamespace()
	if namespace == "" {
		namespace = v1alpha1.SystemNamespace
	}

	l := map[string]string{
		v1alpha1.WorkTypeLabel: string(workflowType),
	}
	promiseName := obj.GetName()
	if workflowType == v1alpha1.WorkflowTypeResource {
		promiseName = obj.GetLabels()[v1alpha1.PromiseNameLabel]
		l[v1alpha1.ResourceNameLabel] = obj.GetName()
	}
	l[v1alpha1.PromiseNameLabel] = promiseName
	return getExistingWorks(ctx, k8sClient, namespace, l)
}

// GetWork returns a Work object based on the provided inputs.
func GetWork(ctx context.Context, k8sClient client.Client, namespace string, labels map[string]string) (*v1alpha1.Work, error) {
	works, err := getExistingWorks(ctx, k8sClient, namespace, labels)
	if err != nil {
		return nil, err
	}

	//TODO test
	if len(works) > 1 {
		return nil, fmt.Errorf("more than 1 work exist with the matching labels: %q. Unable to update", labels)
	}

	if len(works) == 0 {
		return nil, nil
	}

	return &works[0], nil
}

func getExistingWorks(ctx context.Context, k8sClient client.Client, namespace string, workLabels map[string]string) ([]v1alpha1.Work, error) {
	workSelectorLabel := labels.FormatLabels(workLabels)
	selector, err := labels.Parse(workSelectorLabel)
	if err != nil {
		return nil, err
	}
	works := v1alpha1.WorkList{}
	err = k8sClient.List(ctx, &works, &client.ListOptions{
		LabelSelector: selector,
		Namespace:     namespace,
	})
	if err != nil {
		return nil, err
	}

	return works.Items, nil
}
