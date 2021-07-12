package pipeline

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	platformv1alpha1 "github.com/syntasso/synpl-platform/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WorkCreator struct {
	K8sClient client.Client
}

func (w *WorkCreator) Execute(inputDirectory string, identifier string) error {
	files, err := ioutil.ReadDir(inputDirectory)
	if err != nil {
		return err
	}

	resources := []unstructured.Unstructured{}

	for _, fileInfo := range files {
		fileName := filepath.Join(inputDirectory, fileInfo.Name())

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
	work.Name = identifier
	work.Namespace = "default"

	manifests := &work.Spec.Workload.Manifests
	for _, resource := range resources {
		manifest := platformv1alpha1.Manifest{
			Unstructured: resource,
		}
		*manifests = append(*manifests, manifest)
	}

	err = w.K8sClient.Create(context.Background(), &work)

	if errors.IsAlreadyExists(err) {
		fmt.Println("Work " + identifier + " already exists")
		err = w.K8sClient.Update(context.Background(), &work)
		if err != nil {
			fmt.Println("Error updating Work " + identifier + " " + err.Error())
		}
		return nil
	} else if err != nil {
		return err
	} else {
		fmt.Println("Work " + identifier + " created")
		return nil
	}
}
