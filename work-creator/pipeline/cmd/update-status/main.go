package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/syntasso/kratix/work-creator/pipeline/lib"
	"github.com/syntasso/kratix/work-creator/pipeline/lib/helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func main() {
	ctx := context.Background()
	if err := run(ctx); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run(ctx context.Context) error {
	workspaceDir := "/work-creator-files"
	statusFile := filepath.Join(workspaceDir, "metadata", "status.yaml")

	params := helpers.GetParametersFromEnv()

	client, err := helpers.GetK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	objectClient := client.Resource(helpers.ObjectGVR(params)).Namespace(params.ObjectNamespace)

	existingObj, err := objectClient.Get(ctx, params.ObjectName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get existing object: %v", err)
	}

	existingStatus := map[string]any{}
	if existingObj.Object["status"] != nil {
		existingStatus = existingObj.Object["status"].(map[string]any)
	}

	// Load incoming status.yaml if exists
	incomingStatus, err := readStatusFile(statusFile)
	if err != nil {
		return fmt.Errorf("failed to load incoming status: %v", err)
	}

	mergedStatus := lib.MergeStatuses(existingStatus, incomingStatus)
	if params.IsLastPipeline {
		mergedStatus = lib.MarkAsCompleted(mergedStatus)
	}

	// Apply merged status to the existing object
	existingObj.Object["status"] = mergedStatus

	// Update the object's status
	if _, err = objectClient.UpdateStatus(ctx, existingObj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update status: %v", err)
	}

	return nil
}

func readStatusFile(statusFile string) (map[string]any, error) {
	incomingStatus := map[string]any{}
	if _, err := os.Stat(statusFile); err == nil {
		incomingStatusBytes, err := os.ReadFile(statusFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read status file: %v", err)
		}
		if err := yaml.Unmarshal(incomingStatusBytes, &incomingStatus); err != nil {
			return nil, fmt.Errorf("failed to unmarshal incoming status: %v", err)
		}
	}
	return incomingStatus, nil
}
