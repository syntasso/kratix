package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"

	"sigs.k8s.io/yaml"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run() error {
	// Parse environment variables
	objectGroup := os.Getenv("OBJECT_GROUP")
	objectName := os.Getenv("OBJECT_NAME")
	objectNamespace := os.Getenv("OBJECT_NAMESPACE")
	objectVersion := os.Getenv("OBJECT_VERSION")
	crdPlural := os.Getenv("CRD_PLURAL")
	healthcheck := os.Getenv("HEALTHCHECK")
	outputDir := os.Getenv("OUTPUT_DIR")
	clusterScoped := os.Getenv("CLUSTER_SCOPED") == "true"
	if clusterScoped {
		objectNamespace = "" // promises are cluster scoped
	}
	if outputDir == "" {
		outputDir = "/kratix/input"
	}

	dynamicClient, err := getK8sClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %v", err)
	}

	if err := writeObjectToFile(dynamicClient, outputDir, crdPlural, objectGroup, objectVersion, objectName, objectNamespace); err != nil {
		return err
	}

	if healthcheck == "true" {
		promiseName := os.Getenv("PROMISE_NAME")
		if err := writePromiseToFile(dynamicClient, outputDir, promiseName); err != nil {
			return err
		}
	}

	return nil
}

func writeObjectToFile(client dynamic.Interface, outputDir, plural, group, version, name, namespace string) error {
	// Create GVR for the object
	gvr := schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: plural,
	}

	// Get the object
	dynamicResource := client.Resource(gvr).Namespace(namespace)
	obj, err := dynamicResource.Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get object: %v", err)
	}

	objYAML, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal object: %v", err)
	}

	objectFilePath := filepath.Join(outputDir, "object.yaml")
	if err := os.WriteFile(objectFilePath, objYAML, 0644); err != nil {
		return fmt.Errorf("failed to write object to file: %v", err)
	}

	log.Printf("Object written to %s. Head is:\n%s", objectFilePath, string(objYAML[:min(len(objYAML), 500)]))
	return nil
}

func writePromiseToFile(client dynamic.Interface, outputDir, promiseName string) error {
	gvr := schema.GroupVersionResource{
		Group:    v1alpha1.GroupVersion.Group,
		Version:  v1alpha1.GroupVersion.Version,
		Resource: "promises",
	}

	dynamicResource := client.Resource(gvr)
	obj, err := dynamicResource.Get(context.TODO(), promiseName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get Promise: %v", err)
	}

	promiseYAML, err := yaml.Marshal(obj)
	if err != nil {
		return fmt.Errorf("failed to marshal Promise: %v", err)
	}

	promiseFilePath := filepath.Join(outputDir, "promise.yaml")
	if err := os.WriteFile(promiseFilePath, promiseYAML, 0644); err != nil {
		return fmt.Errorf("failed to write Promise to file: %v", err)
	}

	log.Printf("Promise written to %s. Head is:\n%s", promiseFilePath, string(promiseYAML[:min(len(promiseYAML), 500)]))
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
