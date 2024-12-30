package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/syntasso/kratix/work-creator/pipeline/lib"

	"sigs.k8s.io/yaml"
)

type Inputs struct {
	InputDir  string
	OutputDir string
}

func parseInputsFromEnv() Inputs {
	inputs := Inputs{
		InputDir:  os.Getenv("INPUT_DIR"),
		OutputDir: os.Getenv("OUTPUT_DIR"),
	}
	if inputs.InputDir == "" {
		inputs.InputDir = "/kratix/input"
	}
	if inputs.OutputDir == "" {
		inputs.OutputDir = "/kratix/output"
	}
	return inputs
}

func main() {
	inputs := parseInputsFromEnv()
	objectPath := filepath.Join(inputs.InputDir, "object.yaml")
	promisePath := filepath.Join(inputs.InputDir, "promise.yaml")

	healthDef, err := lib.CreateHealthDefinition(objectPath, promisePath)
	if err != nil {
		fmt.Printf("Error creating health definition: %v\n", err)
		os.Exit(1)
	}

	outputData, err := yaml.Marshal(healthDef)
	if err != nil {
		fmt.Printf("Error marshaling health definition: %v\n", err)
		os.Exit(1)
	}

	outputPath := filepath.Join(inputs.OutputDir, "healthdefinition.yaml")
	if err := os.WriteFile(outputPath, outputData, 0644); err != nil {
		fmt.Printf("Error writing health definition: %v\n", err)
		os.Exit(1)
	}
}
