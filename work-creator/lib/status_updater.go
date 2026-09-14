package lib

import (
	"fmt"
	"strings"
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

// workflowStatusPath renders the status path one workflow's ledger lives at, so
// an error can name the workflow whose ledger was missing. status.kratix.workflows
// holds one entry per workflow, and every read and write below has to stay inside
// the entry it was given: the neighbouring keys belong to other workflows -
// Kratix's other action, or a workflow owned by a controller that embeds the
// workflow engine - and this container is not their writer.
func workflowStatusPath(workflowKey string, fields ...string) string {
	return strings.Join(append([]string{"status", "kratix", "workflows", workflowKey}, fields...), ".")
}

// MarkAsCompleted takes a status map and returns a new status map with the
// "ConfigureWorkflowCompleted" condition set to true. It will also update the
// "message" field to "Resource requested" or "Promise configured" if the
// message is currently "Pending", and clear the suspended generation recorded
// for workflowKey.
func MarkAsCompleted(status map[string]any, workflowKey string, workflowType v1alpha1.Type) map[string]any {
	currentMessage, _ := status["message"].(string)
	if currentMessage == "Pending" {
		switch workflowType {
		case v1alpha1.WorkflowTypeResource:
			status["message"] = "Resource requested"
		case v1alpha1.WorkflowTypePromise:
			status["message"] = "Promise configured"
		}
	}

	if kratix, ok := status["kratix"].(map[string]any); ok {
		if workflows, ok := kratix["workflows"].(map[string]any); ok {
			if workflow, ok := workflows[workflowKey].(map[string]any); ok {
				delete(workflow, "suspendedGeneration")
				workflows[workflowKey] = workflow
				kratix["workflows"] = workflows
				status["kratix"] = kratix
			}
		}
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

func MarkPipelineAsSuspended(status map[string]any, workflowKey, pipelineName, msg, retryAtTimeStamp string, generation int64) (map[string]any, error) {
	kratix, ok := status["kratix"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix while marking pipeline %q as suspended", pipelineName)
	}

	workflows, ok := kratix["workflows"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix.workflows while marking pipeline %q as suspended", pipelineName)
	}

	workflow, ok := workflows[workflowKey].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing %s while marking pipeline %q as suspended",
			workflowStatusPath(workflowKey), pipelineName)
	}

	pipelines, ok := workflow["pipelines"].([]any)
	if !ok {
		return nil, fmt.Errorf("missing %s while marking pipeline %q as suspended",
			workflowStatusPath(workflowKey, "pipelines"), pipelineName)
	}

	for i, pipeline := range pipelines {
		pipelineMap, ok := pipeline.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid pipeline status type at index %d in %s",
				i, workflowStatusPath(workflowKey, "pipelines"))
		}

		if pipelineMap["name"] != pipelineName {
			continue
		}

		pipelineMap["phase"] = "Suspended"
		if msg == "" {
			delete(pipelineMap, "message")
		} else {
			pipelineMap["message"] = msg
		}

		if retryAtTimeStamp != "" {
			pipelineMap["nextRetryAt"] = retryAtTimeStamp
			attempts := int64(1)
			if existing, found := pipelineMap["attempts"]; found {
				attempts = existing.(int64) + 1
			}
			pipelineMap["attempts"] = attempts
		} else {
			delete(pipelineMap, "nextRetryAt")
			delete(pipelineMap, "attempts")
		}

		pipelines[i] = pipelineMap
		workflow["suspendedGeneration"] = generation
		workflow["pipelines"] = pipelines
		workflows[workflowKey] = workflow
		kratix["workflows"] = workflows
		status["kratix"] = kratix
		return status, nil
	}

	return nil, fmt.Errorf("pipeline %q not found in %s",
		pipelineName, workflowStatusPath(workflowKey, "pipelines"))
}

func ClearPipelineSuspension(status map[string]any, workflowKey, pipelineName string) (map[string]any, error) {
	kratix, ok := status["kratix"].(map[string]any)
	if !ok {
		return status, nil
	}

	workflows, ok := kratix["workflows"].(map[string]any)
	if !ok {
		return status, nil
	}

	workflow, ok := workflows[workflowKey].(map[string]any)
	if !ok {
		return status, nil
	}

	pipelines, ok := workflow["pipelines"].([]any)
	if !ok {
		return status, nil
	}

	for i, pipeline := range pipelines {
		pipelineMap, ok := pipeline.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid pipeline status type at index %d in %s",
				i, workflowStatusPath(workflowKey, "pipelines"))
		}

		if pipelineMap["name"] != pipelineName {
			continue
		}

		pipelineMap["phase"] = "Running"
		delete(pipelineMap, "message")
		delete(pipelineMap, "attempts")
		delete(pipelineMap, "nextRetryAt")
		pipelines[i] = pipelineMap
		workflow["pipelines"] = pipelines
		workflows[workflowKey] = workflow
		kratix["workflows"] = workflows
		status["kratix"] = kratix
		return status, nil
	}

	return nil, fmt.Errorf("pipeline %q not found in %s",
		pipelineName, workflowStatusPath(workflowKey, "pipelines"))
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
