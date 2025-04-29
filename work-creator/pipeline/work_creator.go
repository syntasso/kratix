package pipeline

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/syntasso/kratix/lib/objectutil"
	"go.uber.org/zap/zapcore"

	goerr "errors"

	"slices"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/resourceutil"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type WorkCreator struct {
	K8sClient client.Client
}

func (w *WorkCreator) Execute(rootDirectory, promiseName, namespace, resourceName, workflowType, pipelineName string) error {
	identifier := fmt.Sprintf("%s-%s-%s", promiseName, resourceName, pipelineName)
	if workflowType == string(v1alpha1.WorkflowTypePromise) {
		identifier = fmt.Sprintf("%s-%s", promiseName, pipelineName)
	}
	if namespace == "" {
		namespace = "kratix-platform-system"
	}

	opts := zap.Options{
		Development: true,
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts), func(o *zap.Options) {
		o.TimeEncoder = zapcore.TimeEncoderOfLayout("2006-01-02T15:04:05Z07:00")
	}))

	var logger = ctrl.Log.WithName("work-creator").
		WithValues("identifier", identifier).
		WithValues("workName", identifier).
		WithValues("namespace", namespace).
		WithValues("resourceName", resourceName).
		WithValues("promiseName", promiseName).
		WithValues("pipelineName", pipelineName)

	workflowScheduling, err := w.getWorkflowScheduling(rootDirectory)
	if err != nil {
		return err
	}

	var workloadGroups []v1alpha1.WorkloadGroup
	var directoriesToIgnoreForTheBaseScheduling []string
	var defaultDestinationSelectors map[string]string
	pipelineOutputDir := filepath.Join(rootDirectory, "input")
	for _, workflowDestinationSelector := range workflowScheduling {
		directory := workflowDestinationSelector.Directory
		if !isRootDirectory(directory) {
			directoriesToIgnoreForTheBaseScheduling = append(directoriesToIgnoreForTheBaseScheduling, directory)

			workloads, err := w.getWorkloadsFromDir(pipelineOutputDir, filepath.Join(pipelineOutputDir, directory), nil)

			if err != nil {
				return err
			}

			workloadGroups = append(workloadGroups, v1alpha1.WorkloadGroup{
				Workloads: workloads,
				Directory: directory,
				ID:        fmt.Sprintf("%x", md5.Sum([]byte(directory))),
				DestinationSelectors: []v1alpha1.WorkloadGroupScheduling{
					{
						MatchLabels: workflowDestinationSelector.MatchLabels,
						Source:      workflowType + "-" + "workflow",
					},
				},
			})
		} else {
			defaultDestinationSelectors = workflowDestinationSelector.MatchLabels
		}
	}

	workloads, err := w.getWorkloadsFromDir(pipelineOutputDir, pipelineOutputDir, directoriesToIgnoreForTheBaseScheduling)
	if err != nil {
		return err
	}

	if len(workloads) > 0 {
		defaultWorkloadGroup := v1alpha1.WorkloadGroup{
			Workloads: workloads,
			Directory: v1alpha1.DefaultWorkloadGroupDirectory,
			ID:        hash.ComputeHash(v1alpha1.DefaultWorkloadGroupDirectory),
		}

		if defaultDestinationSelectors != nil {
			defaultWorkloadGroup.DestinationSelectors = []v1alpha1.WorkloadGroupScheduling{
				{
					MatchLabels: defaultDestinationSelectors,
					Source:      workflowType + "-" + "workflow",
				},
			}
		}

		destinationSelectors, err := w.getPromiseScheduling(rootDirectory)
		if err != nil {
			return err
		}

		if len(destinationSelectors) > 0 {
			var p []v1alpha1.PromiseScheduling
			var pw []v1alpha1.PromiseScheduling
			for _, selector := range destinationSelectors {
				switch selector.Source {
				case "promise":
					p = append(p, v1alpha1.PromiseScheduling{
						MatchLabels: selector.MatchLabels,
					})
				case "promise-workflow":
					pw = append(pw, v1alpha1.PromiseScheduling{
						MatchLabels: selector.MatchLabels,
					})
				}
			}

			if len(pw) > 0 {
				defaultWorkloadGroup.DestinationSelectors = append(defaultWorkloadGroup.DestinationSelectors, v1alpha1.WorkloadGroupScheduling{
					MatchLabels: v1alpha1.SquashPromiseScheduling(pw),
					Source:      "promise-workflow",
				})
			}

			if len(p) > 0 {
				defaultWorkloadGroup.DestinationSelectors = append(
					defaultWorkloadGroup.DestinationSelectors,
					v1alpha1.WorkloadGroupScheduling{
						MatchLabels: v1alpha1.SquashPromiseScheduling(p),
						Source:      "promise",
					},
				)
			}
		}

		workloadGroups = append(workloadGroups, defaultWorkloadGroup)
	}

	work := &v1alpha1.Work{}

	work.Name = objectutil.GenerateObjectName(identifier)
	work.Namespace = namespace
	work.Spec.WorkloadGroups = workloadGroups
	work.Spec.PromiseName = promiseName
	work.Spec.ResourceName = resourceName
	work.Labels = map[string]string{}
	logger.Info("setting work labels...")

	if workflowType != string(v1alpha1.WorkflowTypeResource) {
		logger.Info("setting promise work labels...")
		work.Namespace = v1alpha1.SystemNamespace
		work.Spec.ResourceName = ""
		work.Labels = v1alpha1.GenerateSharedLabelsForPromise(promiseName)
	}

	work.SetLabels(
		labels.Merge(
			work.GetLabels(),
			resourceutil.GetWorkLabels(promiseName, resourceName, pipelineName, workflowType),
		),
	)

	var currentWork *v1alpha1.Work
	if resourceName == "" {
		currentWork, err = resourceutil.GetWorkForPromisePipeline(w.K8sClient, namespace, promiseName, pipelineName)
	} else {
		currentWork, err = resourceutil.GetWorkForResourcePipeline(w.K8sClient, namespace, promiseName, resourceName, pipelineName)
	}

	if err != nil {
		return err
	}

	if currentWork == nil {
		err := w.K8sClient.Create(context.Background(), work)
		if err != nil {
			return err
		}
		logger.Info("Work created", "workName", work.Name)
		return nil
	}

	logger.Info("Work already exists, will update")
	currentWork.Spec = work.Spec
	err = w.K8sClient.Update(context.Background(), currentWork)

	if err != nil {
		logger.Error(err, "Error updating Work")
		return err
	}

	logger.Info("Work updated", "workName", currentWork.Name)
	return nil
}

// /kratix/output/     /kratix/output/   "bar"
func (w *WorkCreator) getWorkloadsFromDir(prefixToTrimFromWorkloadFilepath, rootDir string, directoriesToIgnoreAtTheRootLevel []string) ([]v1alpha1.Workload, error) {
	// decompress here
	filesAndDirs, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, err
	}

	var workloads []v1alpha1.Workload

	for _, info := range filesAndDirs {
		// TODO: currently we assume everything is a file or a dir, we don't handle
		// more advanced scenarios, e.g. symlinks, file sizes, file permissions etc
		if info.IsDir() {
			if !slices.Contains(directoriesToIgnoreAtTheRootLevel, info.Name()) {
				dir := filepath.Join(rootDir, info.Name())
				newWorkloads, err := w.getWorkloadsFromDir(prefixToTrimFromWorkloadFilepath, dir, nil)
				if err != nil {
					return nil, err
				}
				workloads = append(workloads, newWorkloads...)
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

			content, err := compression.CompressContent(byteValue)
			if err != nil {
				return nil, err
			}

			workload := v1alpha1.Workload{
				Content:  string(content),
				Filepath: path,
			}

			workloads = append(workloads, workload)
		}
	}
	return workloads, nil
}

func (w *WorkCreator) getWorkflowScheduling(rootDirectory string) ([]v1alpha1.WorkflowDestinationSelectors, error) {
	destinationFile := filepath.Join(rootDirectory, "metadata", "destination-selectors.yaml")
	fileContents, err := os.ReadFile(destinationFile)
	if err != nil {
		if goerr.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return ParseDestinationSelectors(fileContents)
}

func (w *WorkCreator) getPromiseScheduling(rootDirectory string) ([]v1alpha1.WorkloadGroupScheduling, error) {
	kratixSystemDirectory := filepath.Join(rootDirectory, "kratix-system")
	file := filepath.Join(kratixSystemDirectory, "promise-scheduling")
	fileContents, err := os.ReadFile(file)
	if err != nil {
		if goerr.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var schedulingConfig []v1alpha1.WorkloadGroupScheduling
	err = yaml.Unmarshal(fileContents, &schedulingConfig)

	if err != nil {
		return nil, err
	}

	return schedulingConfig, nil
}

// ParseDestinationSelectors receives a slice of byte and validates the parsed destination selectors
func ParseDestinationSelectors(bytes []byte) ([]v1alpha1.WorkflowDestinationSelectors, error) {
	var schedulingConfig []v1alpha1.WorkflowDestinationSelectors
	err := yaml.Unmarshal(bytes, &schedulingConfig)

	if err != nil {
		return nil, fmt.Errorf("invalid destination-selectors.yaml: %w", err)
	}

	for i := range schedulingConfig {
		schedulingConfig[i].Directory = filepath.Clean(schedulingConfig[i].Directory)
	}

	for i := range schedulingConfig {
		if len(schedulingConfig[i].MatchLabels) == 0 {
			return nil, fmt.Errorf("invalid destination-selectors.yaml: entry with index %d has no selectors", i)
		}
	}

	if containsDuplicateScheduling(schedulingConfig) {
		err = fmt.Errorf("duplicate entries in destination-selectors.yaml: \n%v", schedulingConfig)
		return nil, err
	}

	if path, found := containsSubDirectory(schedulingConfig); found {
		return nil, fmt.Errorf("invalid directory in destination-selectors.yaml: %s, sub-directories are not allowed", path)
	}

	return schedulingConfig, nil
}

func containsSubDirectory(schedulingConfig []v1alpha1.WorkflowDestinationSelectors) (string, bool) {
	for _, selector := range schedulingConfig {
		directory := selector.Directory
		if filepath.Base(directory) != directory {
			return directory, true
		}
	}

	return "", false
}

func containsDuplicateScheduling(schedulingConfig []v1alpha1.WorkflowDestinationSelectors) bool {
	var directoriesSeen []string

	for _, selector := range schedulingConfig {
		if slices.Contains(directoriesSeen, selector.Directory) {
			return true
		}

		directoriesSeen = append(directoriesSeen, selector.Directory)
	}

	return false
}

// Assumes Dir has already been filepath.Clean'd
func isRootDirectory(dir string) bool {
	return dir == v1alpha1.DefaultWorkloadGroupDirectory
}
