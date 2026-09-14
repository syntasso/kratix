package lib

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/syntasso/kratix/api/v1alpha1"
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

// NonMessageStatusKeys finds keys from status.yaml that's not message
// It's used in Promise workflow because Promise does not preserve unknown fields
func NonMessageStatusKeys(status map[string]any) []string {
	var keys []string
	for key := range status {
		if key != "message" {
			keys = append(keys, key)
		}
	}
	return keys
}

// MarkAsCompleted takes a status map and returns a new status map with the
// "ConfigureWorkflowCompleted" condition set to true. It will also update the
// "message" field to "Resource requested" or "Promise configured" if the
// message is currently "Pending".
func MarkAsCompleted(status map[string]any, workflowType v1alpha1.Type, workflow string) map[string]any {
	currentMessage, _ := status["message"].(string)
	if currentMessage == "Pending" {
		switch workflowType {
		case v1alpha1.WorkflowTypeResource:
			status["message"] = "Resource requested"
		case v1alpha1.WorkflowTypePromise:
			status["message"] = "Promise configured"
		}
	}

	if workflowStatus, found := workflowStatus(status, workflow); found {
		delete(workflowStatus, "suspendedGeneration")
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

func MarkPipelineAsSuspended(status map[string]any, workflow, pipelineName, msg, retryAtTimeStamp string, generation int64) (map[string]any, error) {
	workflowStatus, found := workflowStatus(status, workflow)
	if !found {
		return nil, fmt.Errorf("missing status.kratix.workflows.%s while marking pipeline %q as suspended", workflow, pipelineName)
	}

	pipeline, err := findPipeline(workflowStatus, workflow, pipelineName)
	if err != nil {
		return nil, err
	}

	pipeline["phase"] = "Suspended"
	if msg == "" {
		delete(pipeline, "message")
	} else {
		pipeline["message"] = msg
	}

	if retryAtTimeStamp != "" {
		pipeline["nextRetryAt"] = retryAtTimeStamp
		attempts := int64(1)
		if existing, found := pipeline["attempts"]; found {
			attempts = existing.(int64) + 1
		}
		pipeline["attempts"] = attempts
	} else {
		delete(pipeline, "nextRetryAt")
		delete(pipeline, "attempts")
	}

	workflowStatus["suspendedGeneration"] = generation
	return status, nil
}

func ClearPipelineSuspension(status map[string]any, workflow, pipelineName string) (map[string]any, error) {
	workflowStatus, found := workflowStatus(status, workflow)
	if !found {
		return status, nil
	}
	if _, found := workflowStatus["pipelines"]; !found {
		return status, nil
	}

	pipeline, err := findPipeline(workflowStatus, workflow, pipelineName)
	if err != nil {
		return nil, err
	}

	pipeline["phase"] = "Running"
	delete(pipeline, "message")
	delete(pipeline, "attempts")
	delete(pipeline, "nextRetryAt")
	return status, nil
}

// workflowStatus returns the maps inside the object's status, so changing what
// it returns changes the status.
func workflowStatus(status map[string]any, workflow string) (map[string]any, bool) {
	kratix, ok := status["kratix"].(map[string]any)
	if !ok {
		return nil, false
	}
	workflows, ok := kratix["workflows"].(map[string]any)
	if !ok {
		return nil, false
	}
	recorded, ok := workflows[workflow].(map[string]any)
	return recorded, ok
}

func findPipeline(workflowStatus map[string]any, workflow, pipelineName string) (map[string]any, error) {
	pipelines, ok := workflowStatus["pipelines"].([]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix.workflows.%s.pipelines while updating pipeline %q", workflow, pipelineName)
	}

	for i, entry := range pipelines {
		pipeline, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid pipeline status at index %d in status.kratix.workflows.%s.pipelines", i, workflow)
		}
		if pipeline["name"] == pipelineName {
			return pipeline, nil
		}
	}

	return nil, fmt.Errorf("pipeline %q not found in status.kratix.workflows.%s.pipelines", pipelineName, workflow)
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
