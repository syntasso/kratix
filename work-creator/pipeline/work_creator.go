package pipeline

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
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
	files, _ := ioutil.ReadDir(input_directory)
	resources := []unstructured.Unstructured{}

	for _, fileInfo := range files {
		fileName := filepath.Join(input_directory, fileInfo.Name())

		file, _ := os.Open(fileName)

		decoder := yaml.NewYAMLOrJSONDecoder(file, 2048)
		for {
			us := &unstructured.Unstructured{}
			err := decoder.Decode(&us)
			if err == io.EOF {
				//We reached the end of the file, move on to looking for the resource
				break
			} else {
				//append the first resource to the resource slice, and go back through the loop
				resources = append(resources, *us)
			}
		}
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
