package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/work-creator/lib"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
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
	controlFile := filepath.Join(baseDir, "workflow-control.yaml")

	existingObj, err := objectClient.Get(ctx, params.ObjectName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get existing object: %w", err)
	}

	existingStatus := map[string]any{}
	if existingObj.Object["status"] != nil {
		existingStatus = existingObj.Object["status"].(map[string]any)
	}

	// Load incoming status.yaml if exists
	incomingStatus, err := readStatusFile(statusFile)
	if err != nil {
		return fmt.Errorf("failed to load incoming status: %w", err)
	}

	if _, ok := incomingStatus["kratix"]; ok {
		return fmt.Errorf("'kratix' is a kratix managed status field that cannot be updated via workflows; " +
			"remove update to 'kratix' from the '/kratix/metadata/status.yaml' file")
	}

	mergedStatus := lib.MergeStatuses(existingStatus, incomingStatus)

	if params.WorkflowType == v1alpha1.WorkflowTypePromise {
		if nonMessageKeys := lib.NonMessageStatusKeys(incomingStatus); len(nonMessageKeys) > 0 {
			fmt.Fprintf(
				os.Stdout,
				"Warning: promise workflow status has unsupported keys: %s in status.yaml; only 'message' can be updated in Promise status.\n",
				strings.Join(nonMessageKeys, ", "),
			)
		}
	}

	if params.IsLastPipeline {
		mergedStatus = lib.MarkAsCompleted(mergedStatus, params.WorkflowType)
	}

	control, err := readWorkflowControlFile(controlFile)
	if err != nil {
		return err
	}

	if control != nil && control.Suspend {
		fmt.Fprintln(
			os.Stdout,
			"Info: workflow-control.yaml file found with suspend set to true; will label the object and update its pipeline execution status.")
		existingObj, err = addWorkflowSuspendLabel(ctx, objectClient, existingObj)
		if err != nil {
			return err
		}

		mergedStatus, err = lib.MarkPipelineAsSuspended(mergedStatus, params.PipelineName, control.Message, existingObj.GetGeneration())
		if err != nil {
			return err
		}
	}

	// Apply merged status to the existing object
	existingObj.Object["status"] = mergedStatus

	// Update the object's status
	if _, err = objectClient.UpdateStatus(ctx, existingObj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}
	return nil
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
	labels[v1alpha1.WorkflowSuspendLabel] = "true"
	metadata["labels"] = labels
	existingObj.Object["metadata"] = metadata

	updatedObj, err := objectClient.Update(ctx, existingObj, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update object labels: %w", err)
	}

	fmt.Fprintf(
		os.Stdout,
		"Info: labelled the object with %q label to 'true'.\n ", v1alpha1.WorkflowSuspendLabel)
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

type WorkflowControl struct {
	Suspend bool   `json:"suspend"`
	Message string `json:"message"`
}

func readWorkflowControlFile(workflowControlFile string) (*WorkflowControl, error) {
	var workflowControl WorkflowControl
	if _, err := os.Stat(workflowControlFile); err == nil {
		bytes, err := os.ReadFile(workflowControlFile)
		if err != nil {
			return nil, fmt.Errorf("failed to workflow control file: %w", err)
		}
		if err := yaml.Unmarshal(bytes, &workflowControl); err != nil {
			return nil, fmt.Errorf("failed to unmarshal control file: %w", err)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return &workflowControl, nil
}
