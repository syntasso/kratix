package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	goerr "errors"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WorkCreator struct {
	K8sClient client.Client
}

func (w *WorkCreator) Execute(rootDirectory, promiseName, namespace, resourceName, workflowType string) error {
	identifier := fmt.Sprintf("%s-%s", promiseName, resourceName)

	if namespace == "" {
		namespace = "kratix-platform-system"
	}

	var logger = ctrl.Log.WithName("work-creator").
		WithValues("identifier", identifier).
		WithValues("workName", identifier).
		WithValues("namespace", namespace).
		WithValues("resourceName", resourceName).
		WithValues("promiseName", promiseName)

	pipelineOutputDir := filepath.Join(rootDirectory, "input")
	workloadGroups, err := w.getWorkloadGroupsFromDir(nil, pipelineOutputDir, pipelineOutputDir)
	if err != nil {
		return err
	}

	work := &v1alpha1.Work{}

	work.Name = identifier
	work.Namespace = namespace
	work.Spec.Replicas = v1alpha1.ResourceRequestReplicas
	work.Spec.WorkloadGroups = workloadGroups
	for i := range work.Spec.WorkloadGroups {
		work.Spec.WorkloadGroups[i].PromiseName = promiseName
		work.Spec.WorkloadGroups[i].ResourceName = resourceName
	}

	pipelineScheduling, err := w.getPipelineScheduling(rootDirectory)
	if err != nil {
		return err
	}
	work.Spec.DestinationSelectors.Resource = pipelineScheduling

	promiseScheduling, err := w.getPromiseScheduling(rootDirectory)
	if err != nil {
		return err
	}
	work.Spec.DestinationSelectors.Promise = promiseScheduling

	if workflowType == v1alpha1.KratixWorkflowTypePromise {
		work.Name = promiseName
		work.Namespace = v1alpha1.KratixSystemNamespace
		work.Spec.Replicas = v1alpha1.DependencyReplicas
		work.Spec.DestinationSelectors.Resource = nil
		work.Labels = v1alpha1.GenerateSharedLabelsForPromise(promiseName)
	}

	err = w.K8sClient.Create(context.Background(), work)

	if errors.IsAlreadyExists(err) {
		logger.Info("Work already exists, will update")
		currentWork := v1alpha1.Work{}
		key := client.ObjectKeyFromObject(work)

		err := w.K8sClient.Get(context.Background(), key, &currentWork)
		if err != nil {
			logger.Error(err, "Error retrieving Work")
		}

		currentWork.Spec = work.Spec
		err = w.K8sClient.Update(context.Background(), &currentWork)

		if err != nil {
			logger.Error(err, "Error updating Work")
		}
		logger.Info("Work updated")
		return nil
	} else if err != nil {
		return err
	} else {
		logger.Info("Work created")
		return nil
	}
}

func (w *WorkCreator) getWorkloadGroupsFromDir(groups []v1alpha1.WorkloadGroup, prefixToTrimFromWorkloadFilepath, rootDir string) ([]v1alpha1.WorkloadGroup, error) {
	filesAndDirs, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, err
	}

	for _, info := range filesAndDirs {
		// TODO: currently we assume everything is a file or a dir, we don't handle
		// more advanced scenarios, e.g. symlinks, file sizes, file permissions etc
		if info.IsDir() {
			dir := filepath.Join(rootDir, info.Name())
			groups, err = w.getWorkloadGroupsFromDir(groups, prefixToTrimFromWorkloadFilepath, dir)
			if err != nil {
				return nil, err
			}
		} else {
			filePath := filepath.Join(rootDir, info.Name())
			file, err := os.Open(filePath)
			if err != nil {
				return nil, err
			}
			byteValue, err := io.ReadAll(file)
			if err != nil {
				return nil, err
			}

			// trim /kratix/output/ from the filepath
			path, err := filepath.Rel(prefixToTrimFromWorkloadFilepath, filePath)
			if err != nil {
				return nil, err
			}

			deps, err := splitDocumentInDependencies(byteValue)
			if err != nil {
				return nil, err
			}

			depGrouping, err := v1alpha1.SplitDependenciesBySelector(deps)
			if err != nil {
				return nil, err
			}

			groups, err = v1alpha1.MergeDependencyGroupIntoExistingWorkloadGroup(depGrouping, groups, "", path)
			if err != nil {
				return nil, err
			}

		}
	}

	return groups, nil
}

func splitDocumentInDependencies(byteValue []byte) (v1alpha1.Dependencies, error) {
	var deps v1alpha1.Dependencies
	documents := strings.Split(string(byteValue), "\n---\n")
	for _, document := range documents {
		dep := &unstructured.Unstructured{}
		err := yaml.Unmarshal([]byte(document), dep)
		if err != nil {
			return nil, err
		}
		deps = append(deps, v1alpha1.Dependency{Unstructured: *dep})
	}
	return deps, nil
}

func getDestinationSelectorOverrides(yamlBytes []byte) *v1alpha1.Selector {
	type k8sMetadata struct {
		metav1.ObjectMeta `json:"metadata,omitempty"`
	}
	objMeta := k8sMetadata{}
	err := yaml.Unmarshal(yamlBytes, &objMeta)
	if err != nil {
		return nil
	}

	override, found := objMeta.GetAnnotations()[v1alpha1.DestinationSelectorsOverride]
	if !found {
		return nil
	}

	fmt.Println("found overrides")
	selector := &v1alpha1.Selector{}
	if err := yaml.Unmarshal([]byte(override), selector); err != nil {
		return nil
	}

	return selector
}

func (w *WorkCreator) getPipelineScheduling(rootDirectory string) ([]v1alpha1.Selector, error) {
	metadataDirectory := filepath.Join(rootDirectory, "metadata")
	return getSelectorsFromFile(filepath.Join(metadataDirectory, "destination-selectors.yaml"))
}

func (w *WorkCreator) getPromiseScheduling(rootDirectory string) ([]v1alpha1.Selector, error) {
	kratixSystemDirectory := filepath.Join(rootDirectory, "kratix-system")
	return getSelectorsFromFile(filepath.Join(kratixSystemDirectory, "promise-scheduling"))
}

func getSelectorsFromFile(filepath string) ([]v1alpha1.Selector, error) {
	fileContents, err := os.ReadFile(filepath)
	if err != nil {
		if goerr.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var schedulingConfig []v1alpha1.Selector
	err = yaml.Unmarshal(fileContents, &schedulingConfig)
	return schedulingConfig, err
}
