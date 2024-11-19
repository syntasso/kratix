package migrations

import (
	"context"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const deprecatedPipelineCompletedCondition = "PipelineCompleted"

func RemoveDeprecatedConditions(ctx context.Context, k8sClient client.Client, obj *unstructured.Unstructured, logger logr.Logger) (*ctrl.Result, error) {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		logger.Info("No deprecated conditions found on resource", "resource", obj.GetName())
		return nil, nil
	}

	newConditions := filterOutCondition(conditions, deprecatedPipelineCompletedCondition)
	if len(newConditions) == len(conditions) {
		return nil, nil
	}

	logger.Info("Removing deprecated condition", "conditionType", deprecatedPipelineCompletedCondition)
	if err := unstructured.SetNestedSlice(obj.Object, newConditions, "status", "conditions"); err != nil {
		logger.Error(err, "failed to remove deprecated conditions on resource")
		return nil, err
	}

	// Update the status using the client
	if err := k8sClient.Status().Update(ctx, obj); err != nil {
		logger.Error(err, "failed to update resource to remove deprecated condition")
		return nil, err
	}

	return &ctrl.Result{Requeue: true}, nil
}

func filterOutCondition(conditions []interface{}, conditionType string) []interface{} {
	var newConditions []interface{}
	for _, cond := range conditions {
		conditionMap, ok := cond.(map[string]interface{})
		if !ok {
			continue
		}
		if conditionMap["type"] != conditionType {
			newConditions = append(newConditions, cond)
		}
	}
	return newConditions
}
