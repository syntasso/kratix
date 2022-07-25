package pipeline

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"

	goerr "errors"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WorkCreator struct {
	K8sClient client.Client
}

func (w *WorkCreator) Execute(rootDirectory string, identifier string) error {
	inputDirectory := filepath.Join(rootDirectory, "input")

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
			us := unstructured.Unstructured{}

			err := decoder.Decode(&us)
			if err != nil {
				if err == io.EOF {
					//We reached the end of the file, move on to looking for the resource
					break
				}
				return err
			}
			if len(us.Object) == 0 {
				// Empty yaml documents (including only containing comments) should not be appended
				continue
			}
			//append the first resource to the resource slice, and go back through the loop
			resources = append(resources, us)
		}
	}

	work := platformv1alpha1.Work{}
	work.Name = identifier
	work.Namespace = "default"
	work.Spec.Replicas = platformv1alpha1.RESOURCE_REQUEST_REPLICAS

	work.Spec.ClusterSelector, err = w.getMergedClusterSelector(rootDirectory)

	if err != nil {
		return err
	}

	manifests := &work.Spec.Workload.Manifests
	for _, resource := range resources {
		manifest := platformv1alpha1.Manifest{
			Unstructured: resource,
		}
		*manifests = append(*manifests, manifest)
	}

	err = w.K8sClient.Create(context.Background(), &work)

	if errors.IsAlreadyExists(err) {
		fmt.Println("Work " + identifier + " already exists. Will update...")
		currentWork := platformv1alpha1.Work{}
		key := types.NamespacedName{
			Name:      work.Name,
			Namespace: work.Namespace,
		}

		err := w.K8sClient.Get(context.Background(), key, &currentWork)
		if err != nil {
			fmt.Println("Error retrieving Work " + identifier + " " + err.Error())
		}

		currentWork.Spec.Workload.Manifests = *manifests
		err = w.K8sClient.Update(context.Background(), &currentWork)

		if err != nil {
			fmt.Println("Error updating Work " + identifier + " " + err.Error())
		}
		fmt.Println("Work " + identifier + " updated")
		return nil
	} else if err != nil {
		return err
	} else {
		fmt.Println("Work " + identifier + " created")
		return nil
	}
}

func (w *WorkCreator) getMergedClusterSelector(rootDirectory string) (labels.Set, error) {
	resourceRequestClusterSelector, err := w.getResourceRequestClusterSelector(rootDirectory)
	if err != nil {
		return nil, err
	}
	promiseClusterSelector, err := w.getPromiseClusterSelector(rootDirectory)
	if err != nil {
		return nil, err
	}

	mergedSelector := labels.Merge(resourceRequestClusterSelector, promiseClusterSelector)
	return mergedSelector, nil
}

func (w *WorkCreator) getResourceRequestClusterSelector(rootDirectory string) (labels.Set, error) {
	metadataDirectory := filepath.Join(rootDirectory, "metadata")
	clusterSelectorFile := filepath.Join(metadataDirectory, "cluster-selectors.yaml")

	fileContents, err := os.ReadFile(clusterSelectorFile)
	if err != nil {
		if goerr.Is(err, os.ErrNotExist) {
			return labels.Set{}, nil
		}
		return nil, err
	}

	var labelSet labels.Set
	if err := yaml.Unmarshal(fileContents, &labelSet); err != nil {
		return nil, err
	}

	return labelSet, nil
}

func (w *WorkCreator) getPromiseClusterSelector(rootDirectory string) (labels.Set, error) {
	kratixSystemDirectory := filepath.Join(rootDirectory, "kratix-system")

	fileContents, err := os.ReadFile(filepath.Join(kratixSystemDirectory, "promise-cluster-selectors"))
	if err != nil {
		return nil, err
	}

	clusterSelectors := string(fileContents)
	if clusterSelectors == "<none>" {
		return labels.Set{}, nil
	}

	labelSet, err := labels.ConvertSelectorToLabelsMap(clusterSelectors)
	return labelSet, err
}
