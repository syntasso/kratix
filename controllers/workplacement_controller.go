/*
Copyright 2021 Syntasso.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v2"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/writers"
)

const (
	resourcesDir    = "resources"
	dependenciesDir = "dependencies"
)

type StateFile struct {
	Files []string `json:"files"`
}

// WorkPlacementReconciler reconciles a WorkPlacement object
type WorkPlacementReconciler struct {
	Client client.Client
	Log    logr.Logger

	VersionCache map[string]string
}

const (
	repoCleanupWorkPlacementFinalizer       = "finalizers.workplacement.kratix.io/repo-cleanup"
	kratixFileCleanupWorkPlacementFinalizer = "finalizers.workplacement.kratix.io/kratix-dot-files-cleanup"
)

//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/finalizers,verbs=update

func (r *WorkPlacementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("work-placement-controller", req.NamespacedName)

	workPlacement := &v1alpha1.WorkPlacement{}
	err := r.Client.Get(context.Background(), req.NamespacedName, workPlacement)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error getting WorkPlacement", "workPlacement", req.Name)
		return defaultRequeue, nil
	}

	logger.Info("Reconciling WorkPlacement")

	destination := &v1alpha1.Destination{}
	destinationName := client.ObjectKey{
		Name: workPlacement.Spec.TargetDestinationName,
	}

	opts := opts{
		client: r.Client,
		ctx:    ctx,
		logger: logger,
	}

	if !workPlacement.DeletionTimestamp.IsZero() {
		var destinationExists = true
		var filepathMode string
		err = r.Client.Get(context.Background(), destinationName, destination)
		if err != nil {
			if k8sErrors.IsNotFound(err) {
				logger.Info("Destination not found, skipping destination file cleanup", "destination", destinationName)
				destinationExists = false
			} else {
				logger.Error(err, "Error getting destination", "destination", destinationName)
				return ctrl.Result{}, err
			}
		}

		var writer writers.StateStoreWriter
		if destinationExists {
			filepathMode = destination.GetFilepathMode()
			writer, err = newWriter(opts, *destination)
			if err != nil {
				if k8sErrors.IsNotFound(err) {
					return defaultRequeue, nil
				}
				return ctrl.Result{}, err
			}
		}

		return r.deleteWorkPlacement(ctx, destinationExists, writer, workPlacement, filepathMode, logger)
	}

	err = r.Client.Get(context.Background(), destinationName, destination)
	if err != nil {
		logger.Error(err, "Error listing available destinations")
		return ctrl.Result{}, err
	}

	//Mock this out
	writer, err := newWriter(opts, *destination)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}

	logger.Info("Updating files in statestore if required")
	versionID, err := r.writeWorkloadsToStateStore(writer, *workPlacement, *destination, logger)
	if err != nil {
		logger.Error(err, "Error writing to repository, will try again in 5 seconds")
		return defaultRequeue, err
	}

	if versionID == "" && r.VersionCache[workPlacement.GetUniqueID()] != "" {
		versionID = r.VersionCache[workPlacement.GetUniqueID()]
		delete(r.VersionCache, workPlacement.GetUniqueID())
	}

	if versionID != "" && workPlacement.Status.VersionID != versionID {
		workPlacement.Status.VersionID = versionID
		logger.Info("Updating version status", "versionID", versionID)
		err = r.Client.Status().Update(ctx, workPlacement)
		if kerrors.IsConflict(err) {
			r.VersionCache[workPlacement.GetUniqueID()] = versionID
			r.Log.Info("failed to update WorkPlacement status due to update conflict, requeue...")
			return fastRequeue, nil
		} else if err != nil {
			r.VersionCache[workPlacement.GetUniqueID()] = versionID
			logger.Error(err, "Error updating WorkPlacement status")
			return ctrl.Result{}, err
		}
	}

	filepathMode := destination.GetFilepathMode()
	if missingFinalizers := checkWorkPlacementFinalizers(workPlacement, filepathMode); len(missingFinalizers) > 0 {
		return addFinalizers(opts, workPlacement, missingFinalizers)
	}

	logger.Info("WorkPlacement successfully reconciled", "workPlacement", workPlacement.Name, "versionID", versionID)
	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) deleteWorkPlacement(ctx context.Context, destinationExists bool, writer writers.StateStoreWriter, workPlacement *v1alpha1.WorkPlacement, filePathMode string, logger logr.Logger) (ctrl.Result, error) {
	if !destinationExists {
		logger.Info("cleaning up deletion finalizers")
		cleanupDeletionFinalizers(workPlacement)
		if err := r.Client.Update(ctx, workPlacement); err != nil {
			return ctrl.Result{}, err
		}
	}

	pendingRepoCleanup := controllerutil.ContainsFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer)
	pendingKratixFileCleanup := controllerutil.ContainsFinalizer(workPlacement, kratixFileCleanupWorkPlacementFinalizer)

	var err error
	kratixFilePath := fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name)

	var dir string
	switch filePathMode {
	case v1alpha1.FilepathModeNestedByMetadata:
		dir = getDir(*workPlacement) + "/"
	}

	if pendingRepoCleanup {
		logger.Info("cleaning up work on repository", "workplacement", workPlacement.Name)
		var workloadsToDelete []string
		if filePathMode == v1alpha1.FilepathModeNone {
			var kratixFile []byte
			if kratixFile, err = writer.ReadFile(kratixFilePath); err != nil {
				logger.Error(err, "failed to read .kratix state file", "file path", kratixFilePath)
				return ctrl.Result{}, err
			}
			stateFile := StateFile{}
			if err = yaml.Unmarshal(kratixFile, &stateFile); err != nil {
				logger.Error(err, "failed to unmarshal .kratix state file")
				return defaultRequeue, err
			}
			workloadsToDelete = stateFile.Files
		}

		return r.delete(ctx, writer, dir, workPlacement, workloadsToDelete, repoCleanupWorkPlacementFinalizer, logger)
	}

	if pendingKratixFileCleanup {
		logger.Info("cleaning up .kratix state file", "workplacement", workPlacement.Name)
		return r.delete(ctx, writer, "", workPlacement, []string{kratixFilePath}, kratixFileCleanupWorkPlacementFinalizer, logger)
	}
	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) delete(ctx context.Context, writer writers.StateStoreWriter, dir string, workPlacement *v1alpha1.WorkPlacement, workloadsToDelete []string, finalizerToRemove string, logger logr.Logger) (ctrl.Result, error) {
	if _, err := writer.UpdateFiles(dir, workPlacement.Name, nil, workloadsToDelete); err != nil {
		logger.Error(err, "error removing work from repository, will try again in 5 seconds")
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(workPlacement, finalizerToRemove)
	if err := r.Client.Update(ctx, workPlacement); err != nil {
		return ctrl.Result{}, err
	}
	return fastRequeue, nil
}

func (r *WorkPlacementReconciler) writeWorkloadsToStateStore(writer writers.StateStoreWriter, workPlacement v1alpha1.WorkPlacement, destination v1alpha1.Destination, logger logr.Logger) (string, error) {
	var err error
	var workloadsToDelete []string
	var dir = getDir(workPlacement)
	var workloadsToCreate []v1alpha1.Workload

	//loop through workloads and decompress them so the works written to the State Store are decompressed
	for _, workload := range workPlacement.Spec.Workloads {
		decompressedContent, err := compression.DecompressContent([]byte(workload.Content))
		if err != nil {
			return "", fmt.Errorf("unable to decompress file content: %w", err)
		}

		workload.Content = string(decompressedContent)
		workloadsToCreate = append(workloadsToCreate, workload)
	}

	if destination.GetFilepathMode() == v1alpha1.FilepathModeNone {
		var kratixFile []byte
		if kratixFile, err = writer.ReadFile(fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name)); ignoreNotFound(err) != nil {
			return "", fmt.Errorf("failed to read .kratix state file: %w", err)
		}
		oldStateFile := StateFile{}
		if err = yaml.Unmarshal(kratixFile, &oldStateFile); err != nil {
			return "", fmt.Errorf("failed to unmarshal .kratix state file: %w", err)
		}

		newStateFile := StateFile{
			Files: workloadsFilenames(workPlacement.Spec.Workloads),
		}
		stateFileContent, marshalErr := yaml.Marshal(newStateFile)
		if marshalErr != nil {
			return "", fmt.Errorf("failed to marshal new .kratix state file: %w", err)
		}

		stateFileWorkload := v1alpha1.Workload{
			Filepath: fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name),
			Content:  string(stateFileContent),
		}

		dir = ""
		workloadsToCreate = append(workloadsToCreate, stateFileWorkload)
		workloadsToDelete = cleanupWorkloads(oldStateFile.Files, workPlacement.Spec.Workloads)
	}

	versionID, err := writer.UpdateFiles(
		dir,
		workPlacement.Name,
		workloadsToCreate,
		workloadsToDelete,
	)
	if err != nil {
		logger.Error(err, "Error writing resources to repository")
		return "", err
	}
	return versionID, nil
}

func ignoreNotFound(err error) error {
	if errors.Is(err, writers.ErrFileNotFound) {
		return nil
	}
	return err
}

func workloadsFilenames(works []v1alpha1.Workload) []string {
	var result []string
	for _, w := range works {
		result = append(result, w.Filepath)
	}
	return result
}

func cleanupWorkloads(oldWorkloads []string, newWorkloads []v1alpha1.Workload) []string {
	works := make(map[string]bool)
	for _, w := range newWorkloads {
		works[w.Filepath] = true
	}
	var result []string
	for _, w := range oldWorkloads {
		if _, ok := works[w]; !ok {
			result = append(result, w)
		}
	}
	return result
}

func getDir(workPlacement v1alpha1.WorkPlacement) string {
	if workPlacement.Spec.ResourceName == "" {
		//dependencies/<promise-name>/<pipeline-name>/<dir-sha>/
		return filepath.Join(dependenciesDir, workPlacement.Spec.PromiseName, workPlacement.PipelineName(), shortID(workPlacement.Spec.ID))
	} else {
		//resources/<rr-namespace>/<promise-name>/<rr-name>/<pipeline-name>/<dir-sha>/
		return filepath.Join(resourcesDir, workPlacement.GetNamespace(), workPlacement.Spec.PromiseName, workPlacement.Spec.ResourceName, workPlacement.PipelineName(), shortID(workPlacement.Spec.ID))
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkPlacementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.WorkPlacement{}).
		Complete(r)
}

func checkWorkPlacementFinalizers(workPlacement *v1alpha1.WorkPlacement, filepathMode string) []string {
	var missingFinalizers []string
	if !controllerutil.ContainsFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer) {
		missingFinalizers = append(missingFinalizers, repoCleanupWorkPlacementFinalizer)
	}
	if filepathMode == v1alpha1.FilepathModeNone && !controllerutil.ContainsFinalizer(workPlacement, kratixFileCleanupWorkPlacementFinalizer) {
		missingFinalizers = append(missingFinalizers, kratixFileCleanupWorkPlacementFinalizer)
	}
	return missingFinalizers
}

func cleanupDeletionFinalizers(workPlacement *v1alpha1.WorkPlacement) {
	if controllerutil.ContainsFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer) {
		controllerutil.RemoveFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer)
	}
	if controllerutil.ContainsFinalizer(workPlacement, kratixFileCleanupWorkPlacementFinalizer) {
		controllerutil.RemoveFinalizer(workPlacement, kratixFileCleanupWorkPlacementFinalizer)
	}
}
