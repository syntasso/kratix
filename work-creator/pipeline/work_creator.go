package pipeline

import (
	"context"
	"fmt"
	"io/ioutil"
	"path/filepath"

	platformv1alpha1 "github.com/syntasso/synpl-platform/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WorkCreator struct {
	Identifier string
	K8sClient  client.Client
}

func (w *WorkCreator) Execute(input_directory string) {

	fmt.Println(input_directory)
	//Read all files from input_directory
	files, _ := ioutil.ReadDir(input_directory)
	resources := []unstructured.Unstructured{}

	for _, fileInfo := range files {
		fileName := filepath.Join(input_directory, fileInfo.Name())
		bytes, _ := ioutil.ReadFile(fileName)
		unstructured := &unstructured.Unstructured{}
		yaml.Unmarshal(bytes, unstructured)
		resources = append(resources, *unstructured)
	}

	work := platformv1alpha1.Work{}
	work.Name = w.Identifier
	work.Namespace = "default"

	manifests := &work.Spec.Workload.Manifests
	for _, resource := range resources {
		manifest := platformv1alpha1.Manifest{
			Unstructured: resource,
		}
		*manifests = append(*manifests, manifest)
	}

	err := w.K8sClient.Create(context.Background(), &work)
	if err != nil {
		fmt.Println(err.Error())
	}
}
