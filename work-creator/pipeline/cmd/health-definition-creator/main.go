package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"sigs.k8s.io/yaml"
)

type HealthDefinition struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   metav1.ObjectMeta `json:"metadata"`
	Spec       HealthDefSpec     `json:"spec"`
}

type HealthDefSpec struct {
	PromiseRef  PromiseRef                 `json:"promiseRef"`
	ResourceRef ResourceRef                `json:"resourceRef"`
	Input       string                     `json:"input"`
	Workflow    *unstructured.Unstructured `json:"workflow"`
	Schedule    string                     `json:"schedule"`
}

type PromiseRef struct {
	Name string `json:"name"`
}

type ResourceRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

func main() {
	// Get input and output directories from environment variables or use defaults
	inputDir := os.Getenv("INPUT_DIR")
	if inputDir == "" {
		inputDir = "/kratix/input"
	}

	outputDir := os.Getenv("OUTPUT_DIR")
	if outputDir == "" {
		outputDir = "/kratix/output"
	}

	// Check for required input files
	objectPath := filepath.Join(inputDir, "object.yaml")
	promisePath := filepath.Join(inputDir, "promise.yaml")

	if _, err := os.Stat(objectPath); os.IsNotExist(err) {
		fmt.Println("Object file not found")
		os.Exit(1)
	}

	if _, err := os.Stat(promisePath); os.IsNotExist(err) {
		fmt.Println("Promise file not found")
		os.Exit(1)
	}

	// Read and parse object.yaml as a runtime.Object
	objectData, err := os.ReadFile(objectPath)
	if err != nil {
		fmt.Printf("Error reading object file: %v\n", err)
		os.Exit(1)
	}

	var resourceReq metav1.ObjectMeta
	if err := yaml.Unmarshal(objectData, &struct {
		Metadata *metav1.ObjectMeta `yaml:"metadata"`
	}{&resourceReq}); err != nil {
		fmt.Printf("Error parsing object YAML: %v\n", err)
		os.Exit(1)
	}

	// Read and parse promise.yaml
	promiseData, err := os.ReadFile(promisePath)
	if err != nil {
		fmt.Printf("Error reading promise file: %v\n", err)
		os.Exit(1)
	}

	var promise v1alpha1.Promise
	if err := yaml.Unmarshal(promiseData, &promise); err != nil {
		fmt.Printf("Error parsing promise YAML: %v\n", err)
		os.Exit(1)
	}

	// Create health definition
	healthDef := HealthDefinition{
		APIVersion: v1alpha1.GroupVersion.String(),
		Kind:       "HealthDefinition",
		Metadata: metav1.ObjectMeta{
			Name:      fmt.Sprintf("default-%s-%s", resourceReq.Name, promise.Name),
			Namespace: "default",
		},
		Spec: HealthDefSpec{
			PromiseRef: PromiseRef{
				Name: promise.Name,
			},
			ResourceRef: ResourceRef{
				Name:      resourceReq.Name,
				Namespace: resourceReq.Namespace,
			},
			Input:    string(objectData),
			Workflow: promise.Spec.HealthChecks.Resource.Workflow,
			Schedule: promise.Spec.HealthChecks.Resource.Schedule,
		},
	}

	// Marshal and write the health definition
	outputData, err := yaml.Marshal(healthDef)
	if err != nil {
		fmt.Printf("Error marshaling health definition: %v\n", err)
		os.Exit(1)
	}

	outputPath := filepath.Join(outputDir, "healthdefinition.yaml")
	if err := os.WriteFile(outputPath, outputData, 0644); err != nil {
		fmt.Printf("Error writing health definition: %v\n", err)
		os.Exit(1)
	}
}
