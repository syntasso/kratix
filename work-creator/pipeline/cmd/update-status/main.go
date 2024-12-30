package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/syntasso/kratix/work-creator/pipeline/lib"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

type Inputs struct {
	ObjectGroup     string
	ObjectName      string
	ObjectVersion   string
	Plural          string
	ClusterScoped   bool
	ObjectNamespace string
	IsLastPipeline  bool
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run() error {
	workspaceDir := "/work-creator-files"
	statusFile := filepath.Join(workspaceDir, "metadata", "status.yaml")

	inputs := parseInputsFromEnv()

	// Initialize Kubernetes client
	dynamicClient, err := getClientForInputs(inputs)
	if err != nil {
		return fmt.Errorf("failed to get dynamic client: %v", err)
	}

	// Get existing object
	existingObj, err := dynamicClient.Get(context.TODO(), inputs.ObjectName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get existing object: %v", err)
	}

	// Get existing obj status
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
	if inputs.IsLastPipeline {
		mergedStatus = lib.MarkAsCompleted(mergedStatus)
	}

	// Apply merged status to the existing object
	existingObj.Object["status"] = mergedStatus

	// Update the object's status
	if _, err = dynamicClient.UpdateStatus(context.TODO(), existingObj, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update status: %v", err)
	}

	return nil
}

func getK8sClient() (dynamic.Interface, error) {
	// Try to load in-cluster config first
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
	}

	return dynamic.NewForConfig(config)
}

func parseInputsFromEnv() Inputs {
	inputs := Inputs{
		ObjectGroup:     os.Getenv("OBJECT_GROUP"),
		ObjectName:      os.Getenv("OBJECT_NAME"),
		ObjectVersion:   os.Getenv("OBJECT_VERSION"),
		Plural:          os.Getenv("CRD_PLURAL"),
		ClusterScoped:   os.Getenv("CLUSTER_SCOPED") == "true",
		ObjectNamespace: os.Getenv("OBJECT_NAMESPACE"),
		IsLastPipeline:  os.Getenv("IS_LAST_PIPELINE") == "true",
	}

	if inputs.ClusterScoped {
		inputs.ObjectNamespace = "" // promises are cluster scoped
	}

	return inputs
}

func getClientForInputs(inputs Inputs) (dynamic.ResourceInterface, error) {
	client, err := getK8sClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	// Create dynamic client for the specified GVR
	gvr := schema.GroupVersionResource{
		Group:    inputs.ObjectGroup,
		Version:  inputs.ObjectVersion,
		Resource: inputs.Plural,
	}

	return client.Resource(gvr).Namespace(inputs.ObjectNamespace), nil
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
