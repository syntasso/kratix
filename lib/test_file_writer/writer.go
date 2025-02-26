package utils

import (
	"path/filepath"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// WriteTestFiles writes test files to the Resource and Dependencies paths.
func WriteTestFiles(
	writer writers.StateStoreWriter,
	filePathMode,
	dependenciesDir,
	resourcesDir,
	workloadName string,
) error {
	if err := createDependenciesPathWithExample(writer, filePathMode, dependenciesDir, workloadName); err != nil {
		return err
	}

	if err := createResourcePathWithExample(writer, filePathMode, resourcesDir, workloadName); err != nil {
		return err
	}
	return nil
}

func createDependenciesPathWithExample(
	writer writers.StateStoreWriter,
	filePathMode string,
	dependenciesDir string,
	workloadName string,
) error {
	kratixNamespace := &v1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "kratix-worker-system"},
	}
	nsBytes, _ := yaml.Marshal(kratixNamespace)

	filePath := "kratix-canary-namespace.yaml"
	if filePathMode == v1alpha1.FilepathModeNestedByMetadata {
		filePath = filepath.Join(dependenciesDir, filePath)
	}

	_, err := writer.UpdateFiles("", workloadName, []v1alpha1.Workload{{
		Filepath: filePath,
		Content:  string(nsBytes)}}, nil)
	return err
}

func createResourcePathWithExample(writer writers.StateStoreWriter,
	filePathMode string,
	resourcesDir string,
	workloadName string,
) error {
	kratixConfigMap := &v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kratix-info",
			Namespace: "kratix-worker-system",
		},
		Data: map[string]string{
			"canary": "the confirms your infrastructure is reading from Kratix state stores",
		},
	}
	cmBytes, _ := yaml.Marshal(kratixConfigMap)

	filePath := "kratix-canary-configmap.yaml"
	if filePathMode == v1alpha1.FilepathModeNestedByMetadata {
		filePath = filepath.Join(resourcesDir, filePath)
	}

	_, err := writer.UpdateFiles("", workloadName, []v1alpha1.Workload{{
		Filepath: filePath,
		Content:  string(cmBytes)}}, nil)
	return err
}
