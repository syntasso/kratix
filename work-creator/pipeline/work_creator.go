package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	goerr "errors"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WorkCreator struct {
	K8sClient client.Client
}

func (w *WorkCreator) Execute(rootDirectory, promiseName, namespace, resourceName string, addPromiseDependencies bool) error {
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
	workloadGroups, err := w.getWorkloadGroupsFromDir(pipelineOutputDir, pipelineOutputDir)
	if err != nil {
		return err
	}

	work := &v1alpha1.Work{}
	work.Spec.WorkloadGroups = []v1alpha1.WorkloadGroup{}
	if addPromiseDependencies {
		promiseBytes, err := os.ReadFile(filepath.Join(rootDirectory, "promise", "object.yaml"))
		if err != nil {
			return err
		}
		promise := v1alpha1.Promise{}
		err = yaml.Unmarshal(promiseBytes, &promise)
		if err != nil {
			return err
		}

		work, err = v1alpha1.NewPromiseDependenciesWork(&promise)
		if err != nil {
			return err
		}

		work.MergeWorkloadGroups(workloadGroups)
	} else {
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
	}

	err = w.K8sClient.Create(context.Background(), work)
	if err != nil {
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
		}
		return err
	}

	logger.Info("Work created")
	return nil
}

func (w *WorkCreator) getWorkloadGroupsFromDir(prefixToTrimFromWorkloadFilepath, rootDir string) ([]v1alpha1.WorkloadGroup, error) {
	filesAndDirs, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, err
	}

	groups := []v1alpha1.WorkloadGroup{}

	for _, info := range filesAndDirs {
		// TODO: currently we assume everything is a file or a dir, we don't handle
		// more advanced scenarios, e.g. symlinks, file sizes, file permissions etc
		if info.IsDir() {
			dir := filepath.Join(rootDir, info.Name())
			newWorkloads, err := w.getWorkloadGroupsFromDir(prefixToTrimFromWorkloadFilepath, dir)
			if err != nil {
				return nil, err
			}
			groups = append(groups, newWorkloads...)
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

			documents := strings.Split(string(byteValue), "\n---\n")
			fmt.Println("found documents: ", len(documents))
			for i, document := range documents {
				workloadFilename := filepath.Join(path, fmt.Sprintf("%d-%s", i, info.Name()))
				workload := v1alpha1.Workload{
					Content:  document,
					Filepath: workloadFilename,
				}

				var selector *v1alpha1.Selector
				selector, _ = getDestinationSelectorOverrides([]byte(document))
				var destinationOverrides []v1alpha1.Selector
				if selector != nil {
					destinationOverrides = []v1alpha1.Selector{*selector}
				}
				groups = append(groups, v1alpha1.WorkloadGroup{
					WorkloadCoreFields: v1alpha1.WorkloadCoreFields{
						Workloads: []v1alpha1.Workload{workload},
					},
					DestinationSelectorsOverride: destinationOverrides,
				})
			}
		}
	}

	workloadGroups := []v1alpha1.WorkloadGroup{}

	for _, wGroup := range groups {
		var found bool
		for i := range workloadGroups {
			if reflect.DeepEqual(wGroup.DestinationSelectorsOverride, workloadGroups[i].DestinationSelectorsOverride) {
				workloadGroups[i].Workloads = append(workloadGroups[i].Workloads, wGroup.Workloads...)
				found = true
				break
			}
		}

		if !found {
			workloadGroups = append(workloadGroups, wGroup)
		}
	}

	return workloadGroups, nil
}

func getDestinationSelectorOverrides(yamlBytes []byte) (*v1alpha1.Selector, bool) {
	type k8sMetadata struct {
		metav1.ObjectMeta `json:"metadata,omitempty"`
	}
	objMeta := k8sMetadata{}
	err := yaml.Unmarshal(yamlBytes, &objMeta)
	if err != nil {
		return nil, false
	}

	override, found := objMeta.GetAnnotations()[v1alpha1.DestinationSelectorsOverride]
	if !found {
		return nil, false
	}

	fmt.Println("found overrides")
	selector := &v1alpha1.Selector{}
	if err := yaml.Unmarshal([]byte(override), selector); err != nil {
		return nil, false
	}

	return selector, true
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
