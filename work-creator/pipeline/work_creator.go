package pipeline

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"

	goerr "errors"

	ctrl "sigs.k8s.io/controller-runtime"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type WorkCreator struct {
	K8sClient client.Client
}

func (w *WorkCreator) Execute(rootDirectory, promiseName, namespace, resourceName string) error {
	identifier := fmt.Sprintf("%s-%s", promiseName, resourceName)

	var logger = ctrl.Log.WithName("work-creator").
		WithValues("identifier", identifier).
		WithValues("workName", identifier).
		WithValues("namespace", namespace).
		WithValues("resourceName", resourceName).
		WithValues("promiseName", promiseName)

	pipelineOutputDir := filepath.Join(rootDirectory, "input")
	workloads, err := w.getWorkloadsFromDir(pipelineOutputDir, pipelineOutputDir)
	if err != nil {
		return err
	}

	work := platformv1alpha1.Work{}
	work.Name = identifier
	work.Namespace = namespace
	work.Spec.Replicas = platformv1alpha1.ResourceRequestReplicas
	work.Spec.Workloads = workloads
	work.Spec.PromiseName = promiseName
	work.Spec.ResourceName = resourceName

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

	err = w.K8sClient.Create(context.Background(), &work)

	if errors.IsAlreadyExists(err) {
		logger.Info("Work already exists, will update")
		currentWork := platformv1alpha1.Work{}
		key := client.ObjectKeyFromObject(&work)

		err := w.K8sClient.Get(context.Background(), key, &currentWork)
		if err != nil {
			logger.Error(err, "Error retrieving Work")
		}

		currentWork.Spec.Workloads = workloads
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

func (w *WorkCreator) getWorkloadsFromDir(prefixToTrimFromWorkloadFilepath, rootDir string) ([]platformv1alpha1.Workload, error) {
	filesAndDirs, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, err
	}

	workloads := []platformv1alpha1.Workload{}

	for _, info := range filesAndDirs {
		// TODO: currently we assume everything is a file or a dir, we don't handle
		// more advanced scenarios, e.g. symlinks, file sizes, file permissions etc
		if info.IsDir() {
			dir := filepath.Join(rootDir, info.Name())
			newWorkloads, err := w.getWorkloadsFromDir(prefixToTrimFromWorkloadFilepath, dir)
			if err != nil {
				return nil, err
			}
			workloads = append(workloads, newWorkloads...)
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

			workload := platformv1alpha1.Workload{
				Content:  []byte(base64.StdEncoding.EncodeToString(byteValue)),
				Filepath: path,
			}

			workloads = append(workloads, workload)
		}
	}
	return workloads, nil
}

func (w *WorkCreator) getPipelineScheduling(rootDirectory string) ([]platformv1alpha1.Selector, error) {
	metadataDirectory := filepath.Join(rootDirectory, "metadata")
	return getSelectorsFromFile(filepath.Join(metadataDirectory, "destination-selectors.yaml"))
}

func (w *WorkCreator) getPromiseScheduling(rootDirectory string) ([]platformv1alpha1.Selector, error) {
	kratixSystemDirectory := filepath.Join(rootDirectory, "kratix-system")
	return getSelectorsFromFile(filepath.Join(kratixSystemDirectory, "promise-scheduling"))
}

func getSelectorsFromFile(filepath string) ([]platformv1alpha1.Selector, error) {
	fileContents, err := os.ReadFile(filepath)
	if err != nil {
		if goerr.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var schedulingConfig []platformv1alpha1.Selector
	err = yaml.Unmarshal(fileContents, &schedulingConfig)
	return schedulingConfig, err
}
