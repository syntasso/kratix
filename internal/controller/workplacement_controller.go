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

package controller

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/writers"
)

const (
	resourcesDir                            = "resources"
	dependenciesDir                         = "dependencies"
	repoCleanupWorkPlacementFinalizer       = "finalizers.workplacement.kratix.io/repo-cleanup"
	kratixFileCleanupWorkPlacementFinalizer = "finalizers.workplacement.kratix.io/kratix-dot-files-cleanup"
	misscheduledConditionType               = "Misscheduled"
	misscheduledConditionMismatchReason     = "DestinationSelectorMismatch"
	misscheduledConditionMismatchMsg        = "Target destination no longer matches destinationSelectors"
	writeSucceededConditionType             = "WriteSucceeded"
	failedDeleteEventReason                 = "FailedDelete"
)

type StateFile struct {
	Files []string `json:"files"`
}

// WorkPlacementReconciler reconciles a WorkPlacement object
type WorkPlacementReconciler struct {
	Client        client.Client
	Log           logr.Logger
	VersionCache  map[string]string
	EventRecorder record.EventRecorder
}

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

	opts := opts{client: r.Client, ctx: ctx, logger: logger}
	destination, err := r.getDestination(ctx, logger, workPlacement)
	if err != nil && workPlacement.DeletionTimestamp.IsZero() {
		logger.Error(err, "Error retrieving destination")
		return ctrl.Result{}, err
	}

	if !workPlacement.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, workPlacement, destination, opts, err, logger)
	}

	versionID, requeue, err := r.writeToStateStore(workPlacement, destination, opts)
	if err != nil || requeue.RequeueAfter > 0 {
		return requeue, err
	}

	if statusRequeue, statusErr := r.updateStatus(ctx, logger, workPlacement, versionID); statusErr != nil || statusRequeue.RequeueAfter > 0 {
		return statusRequeue, statusErr
	}

	filepathMode := destination.GetFilepathMode()
	if missingFinalizers := checkWorkPlacementFinalizers(workPlacement, filepathMode); len(missingFinalizers) > 0 {
		return addFinalizers(opts, workPlacement, missingFinalizers)
	}

	logger.Info("WorkPlacement successfully reconciled", "versionID", versionID)

	return ctrl.Result{}, r.setWorkplacementReady(ctx, workPlacement)
}

func (r *WorkPlacementReconciler) getDestination(ctx context.Context, logger logr.Logger, wp *v1alpha1.WorkPlacement) (*v1alpha1.Destination, error) {
	dest := &v1alpha1.Destination{}
	key := client.ObjectKey{Name: wp.Spec.TargetDestinationName}

	if err := r.Client.Get(ctx, key, dest); err != nil {
		if k8sErrors.IsNotFound(err) {
			logger.Info("Destination not found", "name", wp.Spec.TargetDestinationName)
		} else {
			logger.Error(err, "Failed to retrieve Destination", "name", wp.Spec.TargetDestinationName)
		}
		return nil, err
	}

	return dest, nil
}

func (r *WorkPlacementReconciler) updateStatus(ctx context.Context, logger logr.Logger, wp *v1alpha1.WorkPlacement, versionID string) (ctrl.Result, error) {
	if versionID == "" && r.VersionCache[wp.GetUniqueID()] != "" {
		versionID = r.VersionCache[wp.GetUniqueID()]
		delete(r.VersionCache, wp.GetUniqueID())
	}

	if versionID != "" && wp.Status.VersionID != versionID {
		wp.Status.VersionID = versionID
		logger.Info("Updating version status", "versionID", versionID)
		err := r.Client.Status().Update(ctx, wp)
		if kerrors.IsConflict(err) {
			r.VersionCache[wp.GetUniqueID()] = versionID
			r.Log.Info("failed to update WorkPlacement status due to update conflict, requeue...")
			return fastRequeue, nil
		} else if err != nil {
			r.VersionCache[wp.GetUniqueID()] = versionID
			logger.Error(err, "Error updating WorkPlacement status")
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) setWriteFailStatusConditions(ctx context.Context, workPlacement *v1alpha1.WorkPlacement, err error) error {
	writeSucceededUpdated := setWorkplacementStatusCondition(workPlacement, metav1.ConditionFalse, writeSucceededConditionType, "WorkloadsFailedWrite", err.Error())
	readyUpdated := setWorkplacementStatusCondition(workPlacement, metav1.ConditionFalse, "Ready", "", "Failing")
	if writeSucceededUpdated || readyUpdated {
		return r.Client.Status().Update(ctx, workPlacement)
	}
	return nil
}

func (r *WorkPlacementReconciler) setWorkplacementReady(ctx context.Context, workPlacement *v1alpha1.WorkPlacement) error {
	var writeSucceeded, misscheduled bool
	for _, cond := range workPlacement.Status.Conditions {
		if cond.Type == misscheduledConditionType {
			misscheduled = true
		}
		if cond.Type == writeSucceededConditionType && cond.Status == metav1.ConditionTrue {
			writeSucceeded = true
		}
	}
	if writeSucceeded && !misscheduled {
		return r.updateStatusCondition(ctx, workPlacement,
			metav1.ConditionTrue, "Ready", "WorkloadsWrittenToTargetDestination", "Ready")
	}
	return nil
}

func (r *WorkPlacementReconciler) updateStatusCondition(ctx context.Context, workPlacement *v1alpha1.WorkPlacement, status metav1.ConditionStatus, conditionType, reason, message string) error {
	if setWorkplacementStatusCondition(workPlacement, status, conditionType, reason, message) {
		return r.Client.Status().Update(ctx, workPlacement)
	}
	return nil
}

func (r *WorkPlacementReconciler) publishWriteEvent(workPlacement *v1alpha1.WorkPlacement, reason, versionID string, err error) {
	if err == nil && versionID != "" {
		r.EventRecorder.Eventf(workPlacement, v1.EventTypeNormal, reason,
			"successfully written to Destination: %s with versionID: %s", workPlacement.Spec.TargetDestinationName, versionID)
	} else if err == nil {
		r.EventRecorder.Eventf(workPlacement, v1.EventTypeNormal, reason,
			"successfully written to Destination: %s", workPlacement.Spec.TargetDestinationName)
	} else {
		r.EventRecorder.Eventf(workPlacement, v1.EventTypeWarning, reason,
			fmt.Sprintf("failed writing to Destination: %s with error: %s; check kubectl get destination for more info", workPlacement.Spec.TargetDestinationName, err.Error()))
	}
}

func (r *WorkPlacementReconciler) deleteWorkPlacement(
	ctx context.Context,
	destination *v1alpha1.Destination,
	writer writers.StateStoreWriter,
	workPlacement *v1alpha1.WorkPlacement,
	filePathMode string,
	logger logr.Logger,
) (ctrl.Result, error) {
	if destination == nil {
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

	if filePathMode == v1alpha1.FilepathModeNestedByMetadata {
		dir = getDir(*workPlacement) + "/"
	}

	if pendingRepoCleanup {
		logger.Info("cleaning up work on repository", "workplacement", workPlacement.Name)
		var workloadsToDelete []string
		if filePathMode == v1alpha1.FilepathModeNone {
			var kratixFile []byte
			if kratixFile, err = writer.ReadFile(kratixFilePath); err != nil {
				logger.Error(err, "failed to read .kratix state file", "file path", kratixFilePath)
				r.EventRecorder.Eventf(workPlacement, v1.EventTypeWarning,
					failedDeleteEventReason, "failed to read .kratix state file: %s", err.Error())
				return ctrl.Result{}, err
			}
			stateFile := StateFile{}
			if err = yaml.Unmarshal(kratixFile, &stateFile); err != nil {
				logger.Error(err, "failed to unmarshal .kratix state file")
				r.EventRecorder.Eventf(workPlacement, v1.EventTypeWarning,
					failedDeleteEventReason, "failed to unmarshal .kratix state file: %s", err.Error())
				return defaultRequeue, err
			}
			workloadsToDelete = stateFile.Files
		}

		if filePathMode == v1alpha1.FilepathModeAggregatedYAML {
			logger.Info("handling aggregated YAML file path mode")
			_, requeue, err := r.handleAggregatedYAML(ctx, workPlacement, destination, dir, writer)
			if err != nil {
				r.EventRecorder.Eventf(workPlacement, v1.EventTypeWarning, failedDeleteEventReason,
					"error removing work from Destination: %s with error: %s", workPlacement.Spec.TargetDestinationName, err.Error())
				return ctrl.Result{}, err
			}
			if requeue {
				return fastRequeue, nil
			}
			workloadsToDelete = []string{destination.Spec.Filepath.Filename}
		}

		return r.delete(ctx, writer, dir, workPlacement, workloadsToDelete, repoCleanupWorkPlacementFinalizer, logger)
	}

	if pendingKratixFileCleanup {
		logger.Info("cleaning up .kratix state file", "workplacement", workPlacement.Name)
		return r.delete(ctx, writer, "", workPlacement, []string{kratixFilePath}, kratixFileCleanupWorkPlacementFinalizer, logger)
	}
	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) handleAggregatedYAML(ctx context.Context, workPlacement *v1alpha1.WorkPlacement, destination *v1alpha1.Destination, dir string, writer writers.StateStoreWriter) (v1alpha1.Workload, bool, error) {
	activeWorkplacements, err := r.getAllWorkplacementsForDestination(ctx, workPlacement.Spec.TargetDestinationName)
	if err != nil {
		return v1alpha1.Workload{}, false, err
	}

	if len(activeWorkplacements) == 0 {
		return v1alpha1.Workload{}, false, nil
	}

	combinedWorkloads, err := r.combineWorkloads(activeWorkplacements)
	if err != nil {
		return v1alpha1.Workload{}, false, err
	}

	workload := v1alpha1.Workload{
		Filepath: destination.Spec.Filepath.Filename,
		Content:  concatenateYAMLs(combinedWorkloads),
	}

	// During deletion, we need to update the files and remove finalizer
	if !workPlacement.DeletionTimestamp.IsZero() {
		_, err = writer.UpdateFiles(dir, workPlacement.Name, []v1alpha1.Workload{workload}, nil)
		if err != nil {
			return v1alpha1.Workload{}, false, fmt.Errorf("error regenerating aggregated YAML: %w", err)
		}

		controllerutil.RemoveFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer)
		if err := r.Client.Update(ctx, workPlacement); err != nil {
			return v1alpha1.Workload{}, false, err
		}
		return workload, true, nil
	}

	return workload, false, nil
}

func (r *WorkPlacementReconciler) delete(ctx context.Context, writer writers.StateStoreWriter, dir string, workPlacement *v1alpha1.WorkPlacement, workloadsToDelete []string, finalizerToRemove string, logger logr.Logger) (ctrl.Result, error) {
	if _, err := writer.UpdateFiles(dir, workPlacement.Name, nil, workloadsToDelete); err != nil {
		r.EventRecorder.Eventf(workPlacement, v1.EventTypeWarning, failedDeleteEventReason,
			"error removing work from Destination: %s  with error: %s", workPlacement.Spec.TargetDestinationName, err.Error())
		logger.Error(err, "error removing work from repository, will try again in 5 seconds", "Destination", workPlacement.Spec.TargetDestinationName)
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(workPlacement, finalizerToRemove)
	if err := r.Client.Update(ctx, workPlacement); err != nil {
		return ctrl.Result{}, err
	}
	return fastRequeue, nil
}

func (r *WorkPlacementReconciler) writeToStateStore(wp *v1alpha1.WorkPlacement, destination *v1alpha1.Destination, opts opts) (string, ctrl.Result, error) {
	writer, err := newWriter(opts, destination.Spec.StateStoreRef.Name, destination.Spec.StateStoreRef.Kind, destination.Spec.Path)
	if err != nil {
		if k8sErrors.IsNotFound(err) {
			return "", defaultRequeue, nil
		}
		return "", ctrl.Result{}, err
	}

	opts.logger.Info("Updating files in statestore if required")
	versionID, err := r.writeWorkloadsToStateStore(opts, writer, *wp, *destination)
	if err != nil {
		opts.logger.Error(err, "Error writing to repository, will try again in 5 seconds", "Destination", wp.Spec.TargetDestinationName)
		r.publishWriteEvent(wp, "WorkloadsFailedWrite", versionID, err)
		if statusUpdateErr := r.setWriteFailStatusConditions(opts.ctx, wp, err); statusUpdateErr != nil {
			opts.logger.Error(statusUpdateErr, "failed to update status condition")
		}
		return "", defaultRequeue, err
	}
	r.publishWriteEvent(wp, "WorkloadsWrittenToStateStore", versionID, err)
	if statusUpdateErr := r.updateStatusCondition(opts.ctx, wp,
		metav1.ConditionTrue, writeSucceededConditionType, "WorkloadsWrittenToStateStore", ""); statusUpdateErr != nil {
		opts.logger.Error(statusUpdateErr, "failed to update status condition")
		return "", defaultRequeue, statusUpdateErr
	}
	return versionID, ctrl.Result{}, err
}

func (r *WorkPlacementReconciler) writeWorkloadsToStateStore(o opts, writer writers.StateStoreWriter, workPlacement v1alpha1.WorkPlacement, destination v1alpha1.Destination) (string, error) {
	var err error
	var workloadsToDelete []string
	var dir = getDir(workPlacement)
	var workloadsToCreate []v1alpha1.Workload

	switch destination.GetFilepathMode() {
	case v1alpha1.FilepathModeAggregatedYAML:
		dir = ""
		workload, _, err := r.handleAggregatedYAML(o.ctx, &workPlacement, &destination, dir, writer)
		if err != nil {
			return "", err
		}
		workloadsToCreate = []v1alpha1.Workload{workload}
	case v1alpha1.FilepathModeNone:
		dir = ""
		newWorkload, oldStateFile, err := r.generateKratixStateFile(workPlacement, writer)
		if err != nil {
			return "", err
		}
		workloadsToCreate = append(workloadsToCreate, newWorkload)
		workloadsToDelete = cleanupWorkloads(oldStateFile.Files, workPlacement.Spec.Workloads)
		fallthrough
	default:
		// loop through workloads and decompress them so the works written to the State Store are decompressed
		for _, workload := range workPlacement.Spec.Workloads {
			decompressedContent, err := compression.DecompressContent([]byte(workload.Content))
			if err != nil {
				return "", fmt.Errorf("unable to decompress file content: %w", err)
			}

			workload.Content = string(decompressedContent)
			workloadsToCreate = append(workloadsToCreate, workload)
		}
	}

	versionID, err := writer.UpdateFiles(dir, workPlacement.Name, workloadsToCreate, workloadsToDelete)
	if err != nil {
		o.logger.Error(err, "Error writing resources to repository")
		return "", err
	}
	return versionID, nil
}

func setWorkplacementStatusCondition(wp *v1alpha1.WorkPlacement, status metav1.ConditionStatus, conditionType, reason, message string) bool {
	desiredCondition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Time{Time: time.Now()},
	}

	var updated bool
	conditions := wp.Status.Conditions
	for i, cond := range conditions {
		if cond.Type == conditionType {
			if cond.Status != status || cond.Reason != reason || cond.Message != message {
				wp.Status.Conditions[i] = desiredCondition
				updated = true
			} else {
				return false
			}
			break
		}
	}
	if !updated {
		wp.Status.Conditions = append(conditions, desiredCondition)
		updated = true
	}
	return updated
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
		// dependencies/<promise-name>/<pipeline-name>/<dir-sha>/
		return filepath.Join(dependenciesDir, workPlacement.Spec.PromiseName, workPlacement.PipelineName(), shortID(workPlacement.Spec.ID))
	} else {
		// resources/<rr-namespace>/<promise-name>/<rr-name>/<pipeline-name>/<dir-sha>/
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

func requeueIfNotFound(err error) (ctrl.Result, error) {
	if k8sErrors.IsNotFound(err) {
		return defaultRequeue, nil
	}
	return ctrl.Result{}, err
}

func (r *WorkPlacementReconciler) handleDeletion(
	ctx context.Context,
	workPlacement *v1alpha1.WorkPlacement,
	destination *v1alpha1.Destination,
	opts opts,
	err error,
	logger logr.Logger,
) (ctrl.Result, error) {
	var destinationExists = true
	var filepathMode string

	if err != nil {
		if k8sErrors.IsNotFound(err) {
			logger.Info(
				"Destination not found, skipping destination file cleanup",
				"destination",
				workPlacement.Spec.TargetDestinationName,
			)
			destinationExists = false
			destination = nil
		} else {
			logger.Error(err, "Error getting destination", "destination", workPlacement.Spec.TargetDestinationName)
			return ctrl.Result{}, err
		}
	}

	var writer writers.StateStoreWriter
	if destinationExists {
		filepathMode = destination.GetFilepathMode()
		writer, err = newWriter(opts, destination.Spec.StateStoreRef.Name, destination.Spec.StateStoreRef.Kind, destination.Spec.Path)
		if err != nil {
			r.EventRecorder.Eventf(workPlacement, v1.EventTypeWarning, failedDeleteEventReason,
				"error at creating a writer for Destination: %s with error: %s", destination.Name, err.Error())
			return requeueIfNotFound(err)
		}
	}
	return r.deleteWorkPlacement(ctx, destination, writer, workPlacement, filepathMode, logger)
}

func concatenateYAMLs(workloads []v1alpha1.Workload) string {
	var sb strings.Builder

	for i, workload := range workloads {
		if i > 0 {
			sb.WriteString("\n---\n")
		}
		sb.WriteString(workload.Content)
	}

	return sb.String()
}

func (r *WorkPlacementReconciler) getAllWorkplacementsForDestination(ctx context.Context, destinationName string) ([]v1alpha1.WorkPlacement, error) {
	allWorkplacements := &v1alpha1.WorkPlacementList{}
	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			TargetDestinationNameLabel: destinationName,
		}),
	}

	if err := r.Client.List(ctx, allWorkplacements, opts); err != nil {
		return nil, fmt.Errorf("failed to list all WorkPlacements: %w", err)
	}

	active := func(wps []v1alpha1.WorkPlacement) []v1alpha1.WorkPlacement {
		var activeWorkplacements []v1alpha1.WorkPlacement
		for _, wp := range wps {
			if wp.DeletionTimestamp.IsZero() {
				activeWorkplacements = append(activeWorkplacements, wp)
			}
		}
		return activeWorkplacements
	}

	return active(allWorkplacements.Items), nil
}

func (r *WorkPlacementReconciler) combineWorkloads(workPlacements []v1alpha1.WorkPlacement) ([]v1alpha1.Workload, error) {
	combinedWorkloads := []v1alpha1.Workload{}
	for _, wp := range workPlacements {
		for _, workload := range wp.Spec.Workloads {
			decompressedContent, err := compression.DecompressContent([]byte(workload.Content))
			if err != nil {
				return nil, fmt.Errorf("unable to decompress file content: %w", err)
			}

			workload.Content = string(decompressedContent)
			combinedWorkloads = append(combinedWorkloads, workload)
		}
	}

	return combinedWorkloads, nil
}

func (r *WorkPlacementReconciler) generateKratixStateFile(workPlacement v1alpha1.WorkPlacement, writer writers.StateStoreWriter) (v1alpha1.Workload, StateFile, error) {
	var kratixFile []byte
	var err error
	if kratixFile, err = writer.ReadFile(fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name)); ignoreNotFound(err) != nil {
		return v1alpha1.Workload{}, StateFile{}, fmt.Errorf("failed to read .kratix state file: %w", err)
	}
	oldStateFile := StateFile{}
	if err = yaml.Unmarshal(kratixFile, &oldStateFile); err != nil {
		return v1alpha1.Workload{}, StateFile{}, fmt.Errorf("failed to unmarshal .kratix state file: %w", err)
	}

	newStateFile := StateFile{
		Files: workloadsFilenames(workPlacement.Spec.Workloads),
	}
	stateFileContent, marshalErr := yaml.Marshal(newStateFile)
	if marshalErr != nil {
		return v1alpha1.Workload{}, StateFile{}, fmt.Errorf("failed to marshal new .kratix state file: %w", err)
	}

	stateFileWorkload := v1alpha1.Workload{
		Filepath: fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name),
		Content:  string(stateFileContent),
	}
	return stateFileWorkload, oldStateFile, nil
}
