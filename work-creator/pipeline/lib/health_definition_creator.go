package lib

import (
	"fmt"
	"os"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
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

// CreateHealthDefinition creates a health definition from an object and a promise
func CreateHealthDefinition(objectPath string, promisePath string) (HealthDefinition, error) {
	objectData, objectMeta, err := parseObject(objectPath)
	if err != nil {
		return HealthDefinition{}, err
	}
	promise, err := parsePromise(promisePath)
	if err != nil {
		return HealthDefinition{}, err
	}
	healthDef := HealthDefinition{
		APIVersion: v1alpha1.GroupVersion.String(),
		Kind:       "HealthDefinition",
		Metadata: metav1.ObjectMeta{
			Name:      fmt.Sprintf("default-%s-%s", objectMeta.Name, promise.Name),
			Namespace: "default",
		},
		Spec: HealthDefSpec{
			PromiseRef: PromiseRef{
				Name: promise.Name,
			},
			ResourceRef: ResourceRef{
				Name:      objectMeta.Name,
				Namespace: objectMeta.Namespace,
			},
			Input:    string(objectData),
			Workflow: promise.Spec.HealthChecks.Resource.Workflow,
			Schedule: promise.Spec.HealthChecks.Resource.Schedule,
		},
	}

	return healthDef, nil
}

func read(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading file: %v", err)
	}
	return data, nil
}

func parseObject(path string) ([]byte, metav1.ObjectMeta, error) {
	objectData, err := read(path)
	if err != nil {
		return nil, metav1.ObjectMeta{}, fmt.Errorf("error reading object file: %v", err)
	}
	var objectMeta metav1.ObjectMeta
	if err := yaml.Unmarshal(objectData, &struct {
		Metadata *metav1.ObjectMeta `yaml:"metadata"`
	}{&objectMeta}); err != nil {
		return nil, metav1.ObjectMeta{}, fmt.Errorf("error parsing object YAML: %v", err)
	}
	return objectData, objectMeta, nil
}

func parsePromise(path string) (v1alpha1.Promise, error) {
	promiseData, err := read(path)
	if err != nil {
		return v1alpha1.Promise{}, fmt.Errorf("error reading promise file: %v", err)
	}
	var promise v1alpha1.Promise
	if err := yaml.Unmarshal(promiseData, &promise); err != nil {
		return v1alpha1.Promise{}, fmt.Errorf("error parsing promise YAML: %v", err)
	}
	return promise, nil
}
