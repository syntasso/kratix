package lib

import (
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sjson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/yaml"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
)

// MergeStatuses takes two status maps and returns a new status map that is a
// merge of the two, following JSON merge patch (RFC 7386): nested maps are
// merged key by key, a null incoming value removes the key, and anything else
// (including lists) is replaced by the incoming value.
//
// `conditions` are the exception. Like Kubernetes does, they are treated as a
// list keyed by `type`: the incoming conditions are merged with the existing
// ones, so a Pipeline cannot drop individual conditions it did not write.
// Setting `conditions` to null still removes them all.
func MergeStatuses(existing map[string]any, incoming map[string]any) (map[string]any, error) {
	existing, incoming = maps.Clone(existing), maps.Clone(incoming)

	existingConditions, existingOK := existing["conditions"].([]any)
	incomingConditions, incomingOK := incoming["conditions"].([]any)
	mergeConditionsByType := existingOK && incomingOK
	if mergeConditionsByType {
		delete(existing, "conditions")
		delete(incoming, "conditions")
	}

	merged, err := mergePatch(existing, incoming)
	if err != nil {
		return nil, err
	}

	if mergeConditionsByType {
		merged["conditions"] = mergeConditions(existingConditions, incomingConditions)
	}
	return merged, nil
}

// SetExpectedHealth sets expectedPromiseVersion and healthDefinitions on
// healthStatus and leaves every other key, such as state and healthRecords, alone.
func SetExpectedHealth(status map[string]any, promiseVersion string, healthDefinitions int) map[string]any {
	healthStatus, ok := status["healthStatus"].(map[string]any)
	if !ok {
		healthStatus = map[string]any{}
	}
	healthStatus["expectedPromiseVersion"] = promiseVersion
	healthStatus["healthDefinitions"] = int64(healthDefinitions)
	status["healthStatus"] = healthStatus
	return status
}

// MarkAsCompleted takes a status map and returns a new status map with the
// "ConfigureWorkflowCompleted" condition set to true. It will also update the
// "message" field to "Resource requested" or "Promise configured" if the
// message is currently "Pending".
func MarkAsCompleted(status map[string]any, workflowType v1alpha1.Type) map[string]any {
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
			if configure, ok := workflows["configure"].(map[string]any); ok {
				delete(configure, "suspendedGeneration")
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

func MarkPipelineAsSuspended(status map[string]any, pipelineName, msg, retryAtTimeStamp string, generation int64, workflowKey string) (map[string]any, error) {
	kratix, ok := status["kratix"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix while marking pipeline %q as suspended", pipelineName)
	}

	workflows, ok := kratix["workflows"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix.workflows while marking pipeline %q as suspended", pipelineName)
	}

	workflow, _ := workflows[workflowKey].(map[string]any)
	pipelines, ok := workflow["pipelines"].([]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix.workflows.%s.pipelines while marking pipeline %q as suspended", workflowKey, pipelineName)
	}

	for i, pipeline := range pipelines {
		pipelineMap, ok := pipeline.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid pipeline status type at index %d in status.kratix.workflows.%s.pipelines", i, workflowKey)
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
		return status, nil
	}

	return nil, fmt.Errorf("pipeline %q not found in status.kratix.workflows.%s.pipelines", pipelineName, workflowKey)
}

func ClearPipelineSuspension(status map[string]any, pipelineName, workflowKey string) (map[string]any, error) {
	kratix, ok := status["kratix"].(map[string]any)
	if !ok {
		return status, nil
	}

	workflows, ok := kratix["workflows"].(map[string]any)
	if !ok {
		return status, nil
	}

	workflow, _ := workflows[workflowKey].(map[string]any)
	pipelines, ok := workflow["pipelines"].([]any)
	if !ok {
		return status, nil
	}

	for i, pipeline := range pipelines {
		pipelineMap, ok := pipeline.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid pipeline status type at index %d in status.kratix.workflows.%s.pipelines", i, workflowKey)
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
		return status, nil
	}

	return nil, fmt.Errorf("pipeline %q not found in status.kratix.workflows.%s.pipelines", pipelineName, workflowKey)
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

// mergePatch goes through JSON because that is what the merge patch library
// operates on. The k8s json package turns whole numbers back into int64, which
// is the type the rest of the status handling (and the dynamic client) uses.
func mergePatch(existing, incoming map[string]any) (map[string]any, error) {
	existingJSON, err := json.Marshal(existing)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal existing status: %w", err)
	}
	incomingJSON, err := json.Marshal(incoming)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal incoming status: %w", err)
	}

	mergedJSON, err := jsonpatch.MergePatch(existingJSON, incomingJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to merge statuses: %w", err)
	}

	merged := map[string]any{}
	if err := k8sjson.Unmarshal(mergedJSON, &merged); err != nil {
		return nil, fmt.Errorf("failed to unmarshal merged status: %w", err)
	}
	return merged, nil
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

	// Convert back to a slice, sorted by type so the order is stable
	types := make([]string, 0, len(merged))
	for condType := range merged {
		types = append(types, condType)
	}
	sort.Strings(types)

	result := make([]any, 0, len(merged))
	for _, condType := range types {
		result = append(result, merged[condType])
	}

	return result
}
