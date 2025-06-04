package resourceutil

import (
	"context"
	"fmt"

	"github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
func GetAllWorksForResource(k8sClient client.Client, namespace, promiseName, resourceName string) ([]v1alpha1.Work, error) {
	workLabels := map[string]string{
		v1alpha1.PromiseNameLabel:  promiseName,
		v1alpha1.ResourceNameLabel: resourceName,
	}
	return getExistingWorks(k8sClient, namespace, workLabels)
}

// GetWorksByType returns the works for the specified workflow type
func GetWorksByType(k8sClient client.Client, workflowType v1alpha1.Type, obj *unstructured.Unstructured) ([]v1alpha1.Work, error) {
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
	return getExistingWorks(k8sClient, namespace, l)
}

// GetWork returns a Work object based on the provided inputs.
func GetWork(k8sClient client.Client, namespace string, labels map[string]string) (*v1alpha1.Work, error) {
	works, err := getExistingWorks(k8sClient, namespace, labels)
	if err != nil {
		return nil, err
	}

	if len(works) > 1 {
		return nil, fmt.Errorf("more than 1 work exist with the matching labels: %q. Unable to update", labels)
	}

	if len(works) == 0 {
		return nil, nil
	}

	return &works[0], nil
}

func getExistingWorks(k8sClient client.Client, namespace string, workLabels map[string]string) ([]v1alpha1.Work, error) {
	workSelectorLabel := labels.FormatLabels(workLabels)
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

// CalculateWorkflowStats returns the total number of works for a resource along
// with how many of those works are ready and how many have failed.
func CalculateWorkflowStats(k8sClient client.Client, namespace, promiseName, resourceName string) (int, int, int, error) {
	works, err := GetAllWorksForResource(k8sClient, namespace, promiseName, resourceName)
	if err != nil {
		return 0, 0, 0, err
	}

	total := len(works)
	succeeded := 0
	failed := 0
	for _, w := range works {
		for _, cond := range w.Status.Conditions {
			if cond.Type == "Ready" {
				switch cond.Status {
				case metav1.ConditionTrue:
					succeeded++
				case metav1.ConditionFalse:
					failed++
				}
			}
		}
	}
	return total, succeeded, failed, nil
}

// AggregateWorkStatus collects status information across all Works for a resource.
// It returns the total number of works, how many succeeded, how many failed,
// whether any work is misplaced, and the aggregated WorksSucceeded condition.
func AggregateWorkStatus(k8sClient client.Client, namespace, promiseName, resourceName string) (int, int, int, bool, metav1.ConditionStatus, error) {
	works, err := GetAllWorksForResource(k8sClient, namespace, promiseName, resourceName)
	if err != nil {
		return 0, 0, 0, false, metav1.ConditionUnknown, err
	}

	total := len(works)
	succeeded := 0
	failed := 0
	misplaced := false
	unknown := false
	for _, w := range works {
		readyCond := metav1.Condition{}
		for _, cond := range w.Status.Conditions {
			if cond.Type == "Ready" {
				readyCond = cond
				break
			}
		}
		switch readyCond.Status {
		case metav1.ConditionTrue:
			succeeded++
		case metav1.ConditionFalse:
			failed++
			if readyCond.Reason == "Misplaced" {
				misplaced = true
			}
		default:
			unknown = true
		}
	}

	worksSucceeded := metav1.ConditionUnknown
	if total > 0 {
		if failed > 0 {
			worksSucceeded = metav1.ConditionFalse
		} else if !unknown && succeeded == total {
			worksSucceeded = metav1.ConditionTrue
		} else {
			worksSucceeded = metav1.ConditionUnknown
		}
	}

	return total, succeeded, failed, misplaced, worksSucceeded, nil
}
