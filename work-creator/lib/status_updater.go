package lib

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
)

// UpdateStatus reads the pipeline-produced status.yaml from baseDir,
// merges it with the existing object's status, and applies workflow-control
// suspension/retry handling. baseDir is typically "/work-creator-files/metadata".
func UpdateStatus(ctx context.Context, baseDir string, params *helpers.Parameters, objectClient dynamic.ResourceInterface) error {
	statusFile := filepath.Join(baseDir, "status.yaml")

	existingObj, err := objectClient.Get(ctx, params.ObjectName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get existing object: %w", err)
	}

	existingStatus := map[string]any{}
	if existingObj.Object["status"] != nil {
		existingStatus = existingObj.Object["status"].(map[string]any)
	}

	incomingStatus, err := readStatusFile(statusFile)
	if err != nil {
		return fmt.Errorf("failed to load incoming status: %w", err)
	}

	if _, ok := incomingStatus["kratix"]; ok {
		return fmt.Errorf("'kratix' is a kratix managed status field that cannot be updated via workflows; " +
			"remove update to 'kratix' from the '/kratix/metadata/status.yaml' file")
	}

	mergedStatus := MergeStatuses(existingStatus, incomingStatus)

	if params.WorkflowType == v1alpha1.WorkflowTypePromise {
		if nonMessageKeys := NonMessageStatusKeys(incomingStatus); len(nonMessageKeys) > 0 {
			fmt.Fprintf(
				os.Stdout,
				"Warning: promise workflow status has unsupported keys: %s in status.yaml; only 'message' can be updated in Promise status.\n",
				strings.Join(nonMessageKeys, ", "),
			)
		}
	}

	control, err := ReadWorkflowControlFile(filepath.Join(baseDir, "workflow-control.yaml"))
	if err != nil {
		return err
	}

	if params.IsLastPipeline && !control.IfSuspendOrRetry() {
		mergedStatus = MarkAsCompleted(mergedStatus, params.WorkflowType)
	}

	existingObj, mergedStatus, err = handleWorkflowControlFile(ctx, params,
		existingObj, objectClient, mergedStatus, control)
	if err != nil {
		return err
	}

	existingObj.Object["status"] = mergedStatus

	if _, err = objectClient.UpdateStatus(ctx, existingObj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}
	return nil
}

func handleWorkflowControlFile(ctx context.Context, params *helpers.Parameters,
	existingObj *unstructured.Unstructured, objectClient dynamic.ResourceInterface,
	mergedStatus map[string]any, control *WorkflowControl) (*unstructured.Unstructured, map[string]any, error) {
	if params.WorkflowType != v1alpha1.WorkflowTypePromise && params.WorkflowType != v1alpha1.WorkflowTypeResource {
		return existingObj, mergedStatus, nil
	}

	var err error

	if !control.IfSuspendOrRetry() {
		mergedStatus, err = ClearPipelineSuspension(mergedStatus, params.PipelineName)
		return existingObj, mergedStatus, err
	}

	retryAfterTimestamp := ""
	if control.IsRetry() {
		fmt.Fprintf(os.Stdout, "Info: workflow-control.yaml has retryAfter: %q \n", control.RetryAfter)
		after, parseErr := control.RetryDuration()
		if parseErr != nil {
			fmt.Fprintf(os.Stdout, "Error: failed to parse retryAfter duration specified in "+
				"the workflow-control.yaml file: %q \n", control.RetryAfter)
			return nil, nil, parseErr
		}
		retryAfterTimestamp = time.Now().UTC().Add(after).Format(time.RFC3339)
	}

	fmt.Fprintln(os.Stdout, "Info: workflow-control.yaml is suspending the pipeline execution; will label the object and update its pipeline execution status.")
	existingObj, err = addWorkflowSuspendLabel(ctx, objectClient, existingObj)
	if err != nil {
		return nil, nil, err
	}

	mergedStatus, err = MarkPipelineAsSuspended(mergedStatus, params.PipelineName, control.Message, retryAfterTimestamp, existingObj.GetGeneration())
	return existingObj, mergedStatus, err
}

func addWorkflowSuspendLabel(ctx context.Context, objectClient dynamic.ResourceInterface, existingObj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	metadata, ok := existingObj.Object["metadata"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("existing object is missing metadata")
	}

	labels, ok := metadata["labels"].(map[string]any)
	if !ok {
		labels = map[string]any{}
	}
	labels[v1alpha1.WorkflowSuspendedLabel] = "true"
	metadata["labels"] = labels
	existingObj.Object["metadata"] = metadata

	updatedObj, err := objectClient.Update(ctx, existingObj, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update object labels: %w", err)
	}

	fmt.Fprintf(
		os.Stdout,
		"Info: labelled the object with %q label to 'true'.\n ", v1alpha1.WorkflowSuspendedLabel)
	return updatedObj, nil
}

func readStatusFile(statusFile string) (map[string]any, error) {
	incomingStatus := map[string]any{}
	if _, err := os.Stat(statusFile); err == nil {
		incomingStatusBytes, err := os.ReadFile(statusFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read status file: %w", err)
		}
		if err := yaml.Unmarshal(incomingStatusBytes, &incomingStatus); err != nil {
			return nil, fmt.Errorf("failed to unmarshal incoming status: %w", err)
		}
	}
	return incomingStatus, nil
}

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
			delete(workflows, "suspendedGeneration")
			kratix["workflows"] = workflows
			status["kratix"] = kratix
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

func MarkPipelineAsSuspended(status map[string]any, pipelineName, msg, retryAtTimeStamp string, generation int64) (map[string]any, error) {
	kratix, ok := status["kratix"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix while marking pipeline %q as suspended", pipelineName)
	}

	workflows, ok := kratix["workflows"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix.workflows while marking pipeline %q as suspended", pipelineName)
	}

	pipelines, ok := workflows["pipelines"].([]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix.workflows.pipelines while marking pipeline %q as suspended", pipelineName)
	}

	for i, pipeline := range pipelines {
		pipelineMap, ok := pipeline.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid pipeline status type at index %d in status.kratix.workflows.pipelines", i)
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
		workflows["suspendedGeneration"] = generation
		workflows["pipelines"] = pipelines
		kratix["workflows"] = workflows
		status["kratix"] = kratix
		return status, nil
	}

	return nil, fmt.Errorf("pipeline %q not found in status.kratix.workflows.pipelines", pipelineName)
}

func ClearPipelineSuspension(status map[string]any, pipelineName string) (map[string]any, error) {
	kratix, ok := status["kratix"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix while clearing suspension for pipeline %q", pipelineName)
	}

	workflows, ok := kratix["workflows"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix.workflows while clearing suspension for pipeline %q", pipelineName)
	}

	pipelines, ok := workflows["pipelines"].([]any)
	if !ok {
		return nil, fmt.Errorf("missing status.kratix.workflows.pipelines while clearing suspension for pipeline %q", pipelineName)
	}

	for i, pipeline := range pipelines {
		pipelineMap, ok := pipeline.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid pipeline status type at index %d in status.kratix.workflows.pipelines", i)
		}

		if pipelineMap["name"] != pipelineName {
			continue
		}

		pipelineMap["phase"] = "Running"
		delete(pipelineMap, "message")
		delete(pipelineMap, "attempts")
		delete(pipelineMap, "nextRetryAt")
		pipelines[i] = pipelineMap
		workflows["pipelines"] = pipelines
		kratix["workflows"] = workflows
		status["kratix"] = kratix
		return status, nil
	}

	return nil, fmt.Errorf("pipeline %q not found in status.kratix.workflows.pipelines", pipelineName)
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
