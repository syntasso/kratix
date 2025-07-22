package lib

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/syntasso/kratix/lib/resourceutil"
)

// MergeStatuses takes two status maps and returns a new status map that is a
// merge of the two.
//
// If a key exists in both maps, the value from the incoming map will be used.
// `conditions` are treated differently. The incoming conditions will be merged
// with the existing conditions.
func MergeStatuses(existing map[string]any, incoming map[string]any) map[string]any {
	return mergeRecursive(existing, incoming)
}

// MarkAsCompleted takes a status map and returns a new status map with the
// "ConfigureWorkflowCompleted" condition set to true. It will also update the
// "message" field to "Resource requested" if the message is currently
// "Pending".
func MarkAsCompleted(status map[string]any) map[string]any {
	currentMessage, _ := status["message"].(string)
	if currentMessage == "Pending" {
		status["message"] = "Resource requested"
	}

	existingConditions, _ := status["conditions"].([]any)
	newCondition := metav1.Condition{
		Message:            "Pipelines completed",
		LastTransitionTime: metav1.NewTime(time.Now().UTC()),
		Status:             metav1.ConditionTrue,
		Type:               string(resourceutil.ConfigureWorkflowCompletedCondition),
		Reason:             resourceutil.PipelinesExecutedSuccessfully,
	}

	status["conditions"] = updateConditions(existingConditions, newCondition)
	return status
}

func updateConditions(conditions []any, newCondition metav1.Condition) []any {
	newCondBytes, _ := yaml.Marshal(newCondition)
	var newCondMap map[string]any
	//nolint:errcheck,gosec // Marshal/Unmarshal is safe here
	yaml.Unmarshal(newCondBytes, &newCondMap)

	if conditions == nil {
		return []any{newCondMap}
	}

	found := false
	for i, cond := range conditions {
		if c, ok := cond.(map[string]any); ok {
			if c["type"] == newCondition.Type {
				conditions[i] = newCondMap
				found = true
				break
			}
		}
	}

	if !found {
		conditions = append(conditions, newCondMap)
	}

	return conditions
}

func mergeRecursive(existing, incoming map[string]any) map[string]any {
	result := make(map[string]any)

	// First, copy all keys from base
	for k, v := range existing {
		result[k] = v
	}

	// Then merge or overwrite with overlay
	for k, v := range incoming {
		// Special handling for conditions
		if k == "conditions" {
			if existingConditions, existingFound := result[k].([]any); existingFound {
				if incomingConditions, incomingFound := v.([]any); incomingFound {
					result[k] = mergeConditions(existingConditions, incomingConditions)
					continue
				}
			}
		}

		// For all other keys, simply overwrite
		result[k] = v
	}

	return result
}

func mergeConditions(existing, incoming []any) []any {
	// Create a map to track conditions by their type
	merged := make(map[string]any)

	// Add base conditions first
	for _, cond := range existing {
		if condMap, ok := cond.(map[string]any); ok {
			if condType, typeOk := condMap["type"].(string); typeOk {
				merged[condType] = condMap
			}
		}
	}

	// Merge or add overlay conditions
	for _, cond := range incoming {
		if condMap, ok := cond.(map[string]any); ok {
			if condType, typeOk := condMap["type"].(string); typeOk {
				merged[condType] = condMap
			}
		}
	}

	// Convert back to slice
	result := make([]any, 0, len(merged))
	for _, item := range merged {
		result = append(result, item)
	}

	return result
}
