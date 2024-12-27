package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

type Status struct {
	Message    string      `json:"message,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}

type Condition struct {
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
	Status             string `json:"status,omitempty"`
	Type               string `json:"type,omitempty"`
	Reason             string `json:"reason,omitempty"`
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run() error {
	workspaceDir := "/work-creator-files"
	statusFile := filepath.Join(workspaceDir, "metadata", "status.yaml")

	// Get environment variables
	// objectKind := os.Getenv("OBJECT_KIND")
	objectGroup := os.Getenv("OBJECT_GROUP")
	objectName := os.Getenv("OBJECT_NAME")
	objectVersion := os.Getenv("OBJECT_VERSION")
	plural := os.Getenv("CRD_PLURAL")
	isLastPipeline := os.Getenv("IS_LAST_PIPELINE") == "true"

	clusterScoped := os.Getenv("CLUSTER_SCOPED") == "true"
	objectNamespace := os.Getenv("OBJECT_NAMESPACE")
	if clusterScoped {
		objectNamespace = "" // promises are cluster scoped
	}

	// Initialize Kubernetes client
	client, err := getK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	// Create dynamic client for the specified GVR
	gvr := schema.GroupVersionResource{
		Group:    objectGroup,
		Version:  objectVersion,
		Resource: plural,
	}

	dynamicClient := client.Resource(gvr).Namespace(objectNamespace)

	// Get existing object
	existingObj, err := dynamicClient.Get(context.TODO(), objectName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get existing object: %v", err)
	}

	// Get existing obj status
	existingStatus := map[string]interface{}{}
	if existingObj.Object["status"] != nil {
		existingStatus = existingObj.Object["status"].(map[string]interface{})
	}

	// Load incoming status if exists
	incomingStatus := map[string]interface{}{}
	if _, err := os.Stat(statusFile); err == nil {
		incomingStatusBytes, err := os.ReadFile(statusFile)
		if err != nil {
			return fmt.Errorf("failed to read status file: %v", err)
		}
		if err := yaml.Unmarshal(incomingStatusBytes, &incomingStatus); err != nil {
			return fmt.Errorf("failed to unmarshal incoming status: %v", err)
		}
	}

	// Merge statuses
	mergedStatus := mergeMaps(existingStatus, incomingStatus)

	// Update conditions if this is the last pipeline
	if isLastPipeline {
		currentMessage, _ := mergedStatus["message"].(string)
		if currentMessage == "Pending" {
			mergedStatus["message"] = "Resource requested"
		}

		// Add or update the ConfigureWorkflowCompleted condition
		conditions, _ := mergedStatus["conditions"].([]interface{})
		newCondition := Condition{
			Message:            "Pipelines completed",
			LastTransitionTime: time.Now().UTC().Format(time.RFC3339),
			Status:             "True",
			Type:               "ConfigureWorkflowCompleted",
			Reason:             "PipelinesExecutedSuccessfully",
		}

		updatedConditions := updateConditions(conditions, newCondition)
		mergedStatus["conditions"] = updatedConditions
	}

	// Apply merged status to the existing object
	existingObj.Object["status"] = mergedStatus

	// Update the object's status
	_, err = dynamicClient.UpdateStatus(context.TODO(), existingObj, metav1.UpdateOptions{})
	if err != nil {
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

func mergeMaps(m1, m2 map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range m1 {
		result[k] = v
	}
	for k, v := range m2 {
		result[k] = v
	}
	return result
}

func updateConditions(conditions []interface{}, newCondition Condition) []interface{} {
	// Convert newCondition to map for consistent handling
	newCondBytes, _ := yaml.Marshal(newCondition)
	var newCondMap map[string]interface{}
	yaml.Unmarshal(newCondBytes, &newCondMap)

	// Initialize if nil
	if conditions == nil {
		return []interface{}{newCondMap}
	}

	// Update existing or append
	found := false
	for i, cond := range conditions {
		if c, ok := cond.(map[string]interface{}); ok {
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
