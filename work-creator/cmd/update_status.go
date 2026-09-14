package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/lib"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/yaml"
)

func updateStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update-status",
		Short: "Update status of Kubernetes resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			return runUpdateStatus(ctx)
		},
	}

	return cmd
}

func runUpdateStatus(ctx context.Context) error {
	workspaceDir := filepath.Join("/work-creator-files", "metadata")

	params := helpers.GetParametersFromEnv()

	client, err := helpers.GetK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	objectClient := client.Resource(helpers.ObjectGVR(params)).Namespace(params.ObjectNamespace)

	err = updateStatus(ctx, workspaceDir, params, objectClient)
	if err != nil {
		return err
	}

	return nil
}

func updateStatus(ctx context.Context, baseDir string, params *helpers.Parameters, objectClient dynamic.ResourceInterface) error {
	statusFile := filepath.Join(baseDir, "status.yaml")

	// Load incoming status.yaml if exists
	incomingStatus, err := readStatusFile(statusFile)
	if err != nil {
		return fmt.Errorf("failed to load incoming status: %w", err)
	}

	if _, ok := incomingStatus["kratix"]; ok {
		return fmt.Errorf("'kratix' is a kratix managed status field that cannot be updated via workflows; " +
			"remove update to 'kratix' from the '/kratix/metadata/status.yaml' file")
	}

	if params.WorkflowType == v1alpha1.WorkflowTypePromise {
		if nonMessageKeys := lib.NonMessageStatusKeys(incomingStatus); len(nonMessageKeys) > 0 {
			fmt.Fprintf(
				os.Stdout,
				"Warning: promise workflow status has unsupported keys: %s in status.yaml; only 'message' can be updated in Promise status.\n",
				strings.Join(nonMessageKeys, ", "),
			)
		}
	}

	control, err := lib.ReadWorkflowControlFile(filepath.Join(baseDir, "workflow-control.yaml"))
	if err != nil {
		return err
	}

	// The engine writes this object's status while the pipeline is still running,
	// so losing the race is routine: without a retry the 409 fails this container
	// and the engine records the pipeline as Failed though every user container
	// in it succeeded. The merge is only valid against the copy of the object it
	// was built from, so each attempt re-Gets and rebuilds it.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		existingObj, err := objectClient.Get(ctx, params.ObjectName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get existing object: %w", err)
		}

		// Deferred: handleWorkflowControlFile may Update the object to add the
		// suspend label, and that Update hands back a copy already carrying status
		// the engine wrote since our Get. Merging before it drops that status.
		mergeStatus := func(obj *unstructured.Unstructured) map[string]any {
			merged := lib.MergeStatuses(objectStatus(obj), incomingStatus)
			if params.IsLastPipeline && !control.IfSuspendOrRetry() {
				merged = lib.MarkAsCompleted(merged, params.WorkflowStatusKey(), params.WorkflowType)
			}
			return merged
		}

		existingObj, mergedStatus, err := handleWorkflowControlFile(ctx, params,
			existingObj, objectClient, mergeStatus, control)
		if err != nil {
			return err
		}

		if os.Getenv(v1alpha1.KratixDryRunEnvVar) == "true" {
			mergedStatus["message"] = "Dry run executed"
		}

		existingObj.Object["status"] = mergedStatus
		if _, err = objectClient.UpdateStatus(ctx, existingObj, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to update status: %w", err)
		}
		return nil
	})
}

func objectStatus(obj *unstructured.Unstructured) map[string]any {
	status, ok := obj.Object["status"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return status
}

func handleWorkflowControlFile(ctx context.Context, params *helpers.Parameters,
	existingObj *unstructured.Unstructured, objectClient dynamic.ResourceInterface,
	mergeStatus func(*unstructured.Unstructured) map[string]any,
	control *lib.WorkflowControl) (*unstructured.Unstructured, map[string]any, error) {
	// Deriving the key from KRATIX_WORKFLOW_TYPE addresses a key the engine never
	// reads and leaves the real entry unsuspended: the type names the lane
	// (promise/resource), not the workflow. Only KRATIX_WORKFLOW_ACTION keys
	// Kratix's own workflows; a controller that embeds the engine is never told
	// its key, so workflow-control writes are a no-op for it — issue #920.
	if params.WorkflowType != v1alpha1.WorkflowTypePromise && params.WorkflowType != v1alpha1.WorkflowTypeResource {
		return existingObj, mergeStatus(existingObj), nil
	}

	if !control.IfSuspendOrRetry() {
		mergedStatus, err := lib.ClearPipelineSuspension(mergeStatus(existingObj),
			params.WorkflowStatusKey(), params.PipelineName)
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
	existingObj, err := addWorkflowSuspendLabel(ctx, objectClient, existingObj)
	if err != nil {
		return nil, nil, err
	}

	mergedStatus, err := lib.MarkPipelineAsSuspended(mergeStatus(existingObj),
		params.WorkflowStatusKey(), params.PipelineName, control.Message,
		retryAfterTimestamp, existingObj.GetGeneration())
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
