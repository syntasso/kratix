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
	"sort"
	"strings"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/internal/telemetry"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/writers"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"go.opentelemetry.io/otel/attribute"
)

const (
	resourcesDir                             = "resources"
	dependenciesDir                          = "dependencies"
	repoCleanupWorkPlacementFinalizer        = "finalizers.workplacement.kratix.io/repo-cleanup"
	kratixFileCleanupWorkPlacementFinalizer  = "finalizers.workplacement.kratix.io/kratix-dot-files-cleanup"
	scheduleSucceededConditionMismatchReason = "DestinationSelectorMismatch"
	scheduleSucceededConditionMismatchMsg    = "Target destination no longer matches destinationSelectors"
	failedDeleteEventReason                  = "FailedDelete"
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

	RepositoryCache RepositoryCache
}

type workPlacementReconcileContext struct {
	ctx        context.Context
	controller string

	logger        logr.Logger
	trace         *reconcileTrace
	client        client.Client
	eventRecorder record.EventRecorder

	workPlacement   *v1alpha1.WorkPlacement
	destination     *v1alpha1.Destination
	repositoryCache RepositoryCache

	versionCache map[string]string
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/finalizers,verbs=update

func (r *WorkPlacementReconciler) newReconcileContext(ctx context.Context, logger logr.Logger, req ctrl.Request) (*workPlacementReconcileContext, error) {
	workPlacement := &v1alpha1.WorkPlacement{}
	if err := r.Client.Get(ctx, req.NamespacedName, workPlacement); err != nil {
		if k8sErrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	dest := &v1alpha1.Destination{}
	targetDestination := workPlacement.Spec.TargetDestinationName
	logger = logger.WithValues("destination", targetDestination)
	key := client.ObjectKey{Name: targetDestination}

	if err := r.Client.Get(ctx, key, dest); err != nil {
		if k8sErrors.IsNotFound(err) {
			logging.Warn(logger, "destination not found, cleaning up deletion finalizers")
			cleanupDeletionFinalizers(workPlacement)
			return nil, r.Client.Update(ctx, workPlacement)
		}
		logging.Error(logger, err, "failed to retrieve Destination")
		return nil, err
	}

	return &workPlacementReconcileContext{
		ctx:             ctx,
		controller:      "workplacement-controller",
		logger:          logger,
		client:          r.Client,
		eventRecorder:   r.EventRecorder,
		workPlacement:   workPlacement,
		destination:     dest,
		repositoryCache: r.RepositoryCache,
		versionCache:    r.VersionCache,
	}, nil
}

func (w *workPlacementReconcileContext) reconcileWithSpanAttributes() (result ctrl.Result, retErr error) {
	promiseName := w.workPlacement.Spec.PromiseName
	resourceName := w.workPlacement.Spec.ResourceName

	spanName := fmt.Sprintf("%s/WorkPlacementReconcile", promiseName)
	if resourceName != "" {
		spanName = fmt.Sprintf("%s/%s", resourceName, spanName)
	}
	w.ctx, w.logger, w.trace = setupReconcileTrace(w.ctx, "workplacement-controller", spanName, w.workPlacement, w.logger)
	defer finishReconcileTrace(w.trace, &retErr)()

	addWorkPlacementSpanAttributes(w.trace, promiseName, w.workPlacement)

	if err := persistReconcileTrace(w.trace, w.client, w.logger); err != nil {
		logging.Error(w.logger, err, "failed to persist trace annotations")
		return ctrl.Result{}, err
	}

	return w.Reconcile()
}

func (w *workPlacementReconcileContext) Reconcile() (result ctrl.Result, retErr error) {
	repo, err := w.repositoryCache.GetRepositoryByTypeAndName(
		w.destination.Spec.StateStoreRef.Kind,
		w.destination.Spec.StateStoreRef.Name,
	)
	if err != nil {
		if errors.Is(err, ErrCacheMiss) {
			logging.Debug(w.logger, "repository not initialised, requeuing")
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}

	repo.Lock()
	defer repo.Unlock()

	if err := repo.Writer.Reset(); err != nil {
		return defaultRequeue, nil
	}

	if !w.workPlacement.DeletionTimestamp.IsZero() {
		return w.handleDeletion(repo)
	}

	if missingFinalizers := w.checkWorkPlacementFinalizers(); len(missingFinalizers) > 0 {
		if err := addFinalizers(opts{client: w.client, logger: w.logger, ctx: w.ctx}, w.workPlacement, missingFinalizers); err != nil {
			if !kerrors.IsConflict(err) {
				return ctrl.Result{}, err
			}
		}
		return fastRequeue, nil
	}

	versionID, requeue, err := w.writeToStateStore(repo)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue.RequeueAfter > 0 {
		return requeue, nil
	}

	if err := w.updateResourceStatus(versionID, nil); err != nil {
		return defaultRequeue, nil
	}

	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	logger := r.Log.WithValues(
		"controller", "workPlacement",
		"name", req.Name,
		"namespace", req.Namespace,
	)

	return withTrace(logger, func() (ctrl.Result, error) {
		workPlacementCtx, err := r.newReconcileContext(ctx, logger, req)
		if err != nil {
			logging.Error(logger, err, "error getting WorkPlacement")
			return defaultRequeue, nil
		}

		if workPlacementCtx == nil {
			return ctrl.Result{}, nil
		}
		return workPlacementCtx.reconcileWithSpanAttributes()
	})
}

func (w *workPlacementReconcileContext) updateResourceStatus(versionID string, err error) error {
	var updated bool
	var clearVersionCache bool

	if err != nil {
		updated = w.workPlacement.SetWriteFailedCondition(err)
	} else {
		versionID = w.getCachedVersionID(versionID)

		versionChanged := versionID != "" && w.workPlacement.Status.VersionID != versionID
		if versionChanged {
			w.workPlacement.Status.VersionID = versionID
		}
		writeSucceededCondChanged := w.workPlacement.SetWriteSucceededCondition()
		condChanged := w.workPlacement.SetWorkplacementReadyStatus()

		updated = versionChanged || writeSucceededCondChanged || condChanged
		clearVersionCache = true
	}

	if updated {
		logging.Debug(w.logger, "updating workplacement status")
		if err := w.client.Status().Update(w.ctx, w.workPlacement); err != nil {
			return err
		}
	}

	if clearVersionCache {
		w.removeVersionIDFromCache()
	}

	return nil
}

func (w *workPlacementReconcileContext) publishWriteEvent(reason, versionID string, err error) {
	if err != nil {
		w.eventRecorder.Eventf(w.workPlacement, v1.EventTypeWarning, reason,
			fmt.Sprintf("failed writing to Destination: %s with error: %s; check kubectl get destination for more info", w.workPlacement.Spec.TargetDestinationName, err.Error()))
		return
	}

	if versionID != "" {
		w.eventRecorder.Eventf(w.workPlacement, v1.EventTypeNormal, reason,
			"successfully written to Destination: %s with versionID: %s", w.workPlacement.Spec.TargetDestinationName, versionID)
		return
	}

	w.eventRecorder.Eventf(w.workPlacement, v1.EventTypeNormal, reason,
		"successfully written to Destination: %s", w.workPlacement.Spec.TargetDestinationName)
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkPlacementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.WorkPlacement{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Complete(r)
}

func (w *workPlacementReconcileContext) checkWorkPlacementFinalizers() []string {
	filepathMode := w.destination.GetFilepathMode()
	var missingFinalizers []string
	if !controllerutil.ContainsFinalizer(w.workPlacement, repoCleanupWorkPlacementFinalizer) {
		missingFinalizers = append(missingFinalizers, repoCleanupWorkPlacementFinalizer)
	}
	if filepathMode == v1alpha1.FilepathModeNone && !controllerutil.ContainsFinalizer(w.workPlacement, kratixFileCleanupWorkPlacementFinalizer) {
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

func (w *workPlacementReconcileContext) setVersionID(versionID string) {
	if versionID == "" {
		return
	}
	w.versionCache[w.workPlacement.GetUniqueID()] = versionID
}

func (w *workPlacementReconcileContext) getCachedVersionID(versionID string) string {
	if versionID != "" {
		return versionID
	}
	return w.versionCache[w.workPlacement.GetUniqueID()]
}

func (w *workPlacementReconcileContext) removeVersionIDFromCache() {
	delete(w.versionCache, w.workPlacement.GetUniqueID())
}

func addWorkPlacementSpanAttributes(traceCtx *reconcileTrace, promiseName string, workPlacement *v1alpha1.WorkPlacement) {
	traceCtx.AddAttributes(
		attribute.String("kratix.promise.name", promiseName),
		attribute.String("kratix.workplacement.name", workPlacement.GetName()),
		attribute.String("kratix.workplacement.namespace", workPlacement.GetNamespace()),
		attribute.String("kratix.workplacement.target_destination", workPlacement.Spec.TargetDestinationName),
		attribute.String("kratix.action", traceCtx.Action()),
	)
}

func (w *workPlacementReconcileContext) handleDeletion(repo *Repository) (ctrl.Result, error) {
	logging.Info(w.logger, "deleting workplacement")

	if w.destination == nil {
		logging.Debug(w.logger, "destination not found; cleaning up deletion finalizers")
		cleanupDeletionFinalizers(w.workPlacement)
		if err := w.client.Update(w.ctx, w.workPlacement); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	return w.deleteWorkPlacement(repo)
}

func (w *workPlacementReconcileContext) logAndRecordError(err error, message string, args ...any) {
	logging.Error(w.logger, err, message, args...)
	w.eventRecorder.Eventf(
		w.workPlacement,
		v1.EventTypeWarning,
		failedDeleteEventReason,
		"%s: %s", message, err.Error(),
	)
}

func (w *workPlacementReconcileContext) generateKratixStateFile(repo *Repository) (v1alpha1.Workload, StateFile, error) {
	oldStateFile, err := w.readKratixStateFile(repo, true)
	if err != nil {
		return v1alpha1.Workload{}, StateFile{}, err
	}

	newStateFile := StateFile{
		Files: workloadsFilenames(w.workPlacement.Spec.Workloads),
	}
	stateFileContent, marshalErr := yaml.Marshal(newStateFile)
	if marshalErr != nil {
		return v1alpha1.Workload{}, StateFile{}, fmt.Errorf("failed to marshal new .kratix state file: %w", err)
	}

	stateFileWorkload := v1alpha1.Workload{
		Filepath: w.kratixStateFilePath(),
		Content:  string(stateFileContent),
	}
	return stateFileWorkload, oldStateFile, nil
}

func (w *workPlacementReconcileContext) kratixStateFilePath() string {
	return filepath.Join(w.destination.Spec.Path, fmt.Sprintf(".kratix/%s-%s.yaml", w.workPlacement.Namespace, w.workPlacement.Name))
}

func (w *workPlacementReconcileContext) readKratixStateFile(repo *Repository, ignoreNotFound bool) (StateFile, error) {
	kratixFilePath := w.kratixStateFilePath()
	kratixFile, err := repo.Writer.ReadFile(w.kratixStateFilePath())
	if err != nil {
		if ignoreNotFound && errors.Is(err, writers.ErrFileNotFound) {
			return StateFile{}, nil
		}
		w.logAndRecordError(err, "failed to read .kratix state file", "filePath", kratixFilePath)
		return StateFile{}, err
	}

	stateFile := StateFile{}
	if err = yaml.Unmarshal(kratixFile, &stateFile); err != nil {
		w.logAndRecordError(err, "failed to unmarshal .kratix state file")
		return StateFile{}, err
	}

	return stateFile, nil
}

func (w *workPlacementReconcileContext) getAggregatedWorkload() (v1alpha1.Workload, error) {
	workload := v1alpha1.Workload{
		Filepath: filepath.Join(w.destination.Spec.Path, w.destination.Spec.Filepath.Filename),
	}
	activeWorkplacements, err := w.getAllWorkplacementsForDestination()
	if err != nil {
		w.logAndRecordError(err, "failed to get all workplacements for destination")
		return v1alpha1.Workload{}, err
	}

	if len(activeWorkplacements) == 0 {
		return workload, nil
	}

	combinedWorkloads, err := w.combineAllWorkloads(activeWorkplacements)
	if err != nil {
		w.logAndRecordError(err, "failed to generate aggregated workload yaml")
		return v1alpha1.Workload{}, err
	}

	workload.Content = combinedWorkloads
	return workload, nil
}

func (w *workPlacementReconcileContext) getAllWorkplacementsForDestination() ([]v1alpha1.WorkPlacement, error) {
	allWorkplacements := &v1alpha1.WorkPlacementList{}
	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			TargetDestinationNameLabel: w.destination.Name,
		}),
	}

	if err := w.client.List(w.ctx, allWorkplacements, opts); err != nil {
		return nil, fmt.Errorf("failed to list all WorkPlacements: %w", err)
	}

	var active []v1alpha1.WorkPlacement
	for _, wp := range allWorkplacements.Items {
		if wp.DeletionTimestamp.IsZero() {
			active = append(active, wp)
		}
	}
	// sort active workplacements by name
	sort.Slice(active, func(i, j int) bool {
		return active[i].Name < active[j].Name
	})
	return active, nil
}

func (w *workPlacementReconcileContext) combineAllWorkloads(workPlacements []v1alpha1.WorkPlacement) (string, error) {
	combinedWorkloads := []v1alpha1.Workload{}
	for _, wp := range workPlacements {
		for _, workload := range wp.Spec.Workloads {
			decompressedContent, err := compression.DecompressContent([]byte(workload.Content))
			if err != nil {
				return "", fmt.Errorf("unable to decompress file content: %w", err)
			}

			workload.Content = string(decompressedContent)
			combinedWorkloads = append(combinedWorkloads, workload)
		}
	}

	var sb strings.Builder

	for i, workload := range combinedWorkloads {
		if i > 0 {
			sb.WriteString("\n---\n")
		}
		sb.WriteString(workload.Content)
	}

	return sb.String(), nil
}

func (w *workPlacementReconcileContext) delete(repo *Repository, workloadsToDelete []string) (ctrl.Result, error) {
	if err := repo.Writer.DeleteFiles(w.workPlacement.Name, workloadsToDelete); err != nil {
		w.logAndRecordError(err, "failed to delete files from repository")
		return defaultRequeue, nil
	}

	controllerutil.RemoveFinalizer(w.workPlacement, repoCleanupWorkPlacementFinalizer)
	controllerutil.RemoveFinalizer(w.workPlacement, kratixFileCleanupWorkPlacementFinalizer)

	if err := w.client.Update(w.ctx, w.workPlacement); err != nil {
		if k8sErrors.IsConflict(err) {
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (w *workPlacementReconcileContext) writeToStateStore(repo *Repository) (string, ctrl.Result, error) {
	metricAttrs := telemetry.WorkPlacementWriteAttributes(
		w.workPlacement.Spec.PromiseName,
		w.workPlacement.Spec.ResourceName,
		w.workPlacement.Spec.TargetDestinationName,
		w.workPlacement.PipelineName(),
	)

	logging.Debug(w.logger, "writing files to state store")
	versionID, workloadErr := w.writeWorkloadsToStateStore(repo)
	if workloadErr != nil {
		telemetry.RecordWorkPlacementWrite(w.ctx, telemetry.WorkPlacementWriteResultFailure, metricAttrs...)
		w.logAndRecordError(workloadErr, "error writing to destination; check kubectl get destination for more info")
		return "", defaultRequeue, w.updateResourceStatus("", workloadErr)
	}

	telemetry.RecordWorkPlacementWrite(w.ctx, telemetry.WorkPlacementWriteResultSuccess, metricAttrs...)
	w.setVersionID(versionID)
	w.publishWriteEvent("WorkloadsWrittenToStateStore", versionID, nil)

	return versionID, ctrl.Result{}, nil
}

func (w *workPlacementReconcileContext) writeWorkloadsToStateStore(repo *Repository) (string, error) {
	var err error
	var workloadsToDelete []string
	var workloadsToCreate []v1alpha1.Workload
	var dir string

	switch w.destination.GetFilepathMode() {

	case v1alpha1.FilepathModeAggregatedYAML:
		workload, err := w.getAggregatedWorkload()
		if err != nil {
			return "", err
		}
		workloadsToCreate = []v1alpha1.Workload{workload}

	case v1alpha1.FilepathModeNone:
		newWorkload, oldStateFile, err := w.generateKratixStateFile(repo)
		if err != nil {
			return "", err
		}
		workloadsToCreate = append(workloadsToCreate, newWorkload)
		workloadsToDelete = cleanupWorkloads(oldStateFile.Files, w.workPlacement.Spec.Workloads)
		fallthrough

	case v1alpha1.FilepathModeNestedByMetadata:
		logging.Trace(w.logger, "handling file path mode nestedByMetadata")
		dir = getDir(w.destination.Spec.Path, *w.workPlacement) + "/"

		for _, workload := range w.workPlacement.Spec.Workloads {
			decompressedContent, err := compression.DecompressContent([]byte(workload.Content))
			if err != nil {
				return "", fmt.Errorf("unable to decompress file content: %w", err)
			}

			workload.Content = string(decompressedContent)
			workloadsToCreate = append(workloadsToCreate, workload)
		}

	default:
		return "", fmt.Errorf("unsupported file path mode: %s", w.destination.GetFilepathMode())
	}

	versionID, err := repo.Writer.UpdateFiles(dir, w.workPlacement.Name, workloadsToCreate, workloadsToDelete)
	if err != nil {
		logging.Error(w.logger, err, "error writing resources to repository")
	}
	return versionID, err
}

func (w *workPlacementReconcileContext) deleteWorkPlacement(repo *Repository) (ctrl.Result, error) {
	pendingRepoCleanup := controllerutil.ContainsFinalizer(w.workPlacement, repoCleanupWorkPlacementFinalizer)
	pendingKratixFileCleanup := controllerutil.ContainsFinalizer(w.workPlacement, kratixFileCleanupWorkPlacementFinalizer)

	var dir string
	workloadsToDelete := []string{}

	if pendingRepoCleanup {
		logging.Debug(w.logger, "deleting files from repository")

		filePathMode := w.destination.GetFilepathMode()

		switch filePathMode {

		case v1alpha1.FilepathModeNestedByMetadata:
			logging.Trace(w.logger, "handling file path mode nestedByMetadata")
			dir = getDir(w.destination.Spec.Path, *w.workPlacement) + "/"
			workloadsToDelete = append(workloadsToDelete, dir)

		case v1alpha1.FilepathModeNone:
			logging.Trace(w.logger, "handling file path mode none")
			stateFile, err := w.readKratixStateFile(repo, false)
			if err != nil {
				logging.Debug(w.logger, "failed to read .kratix state file", "error", err)
				return defaultRequeue, nil
			}
			workloadsToDelete = stateFile.Files

		case v1alpha1.FilepathModeAggregatedYAML:
			logging.Trace(w.logger, "handling file path mode aggregatedYAML")
			workload, err := w.getAggregatedWorkload()
			if err != nil {
				logging.Debug(w.logger, "failed to get aggregated workload", "error", err)
				return defaultRequeue, nil
			}
			workloadsToDelete = []string{workload.Filepath}

			if workload.Content != "" { // there are still other workplacements for this destination
				_, err := repo.Writer.UpdateFiles(workload.Filepath, w.workPlacement.Name, []v1alpha1.Workload{workload}, []string{})
				if err != nil {
					logging.Debug(w.logger, "failed to update files in repository", "error", err)
					return defaultRequeue, nil
				}
				workloadsToDelete = []string{}
			}

		default:
			return ctrl.Result{}, fmt.Errorf("unsupported file path mode: %s", filePathMode)
		}
	}

	if pendingKratixFileCleanup {
		logging.Debug(w.logger, "cleaning up .kratix state file")
		workloadsToDelete = append(workloadsToDelete, w.kratixStateFilePath())
	}

	return w.delete(repo, workloadsToDelete)
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

func getDir(destinationPath string, workPlacement v1alpha1.WorkPlacement) string {
	if workPlacement.Spec.ResourceName == "" {
		// destinationPath/dependencies/<promise-name>/<pipeline-name>/<dir-sha>/
		return filepath.Join(destinationPath, dependenciesDir, workPlacement.Spec.PromiseName, workPlacement.PipelineName(), shortID(workPlacement.Spec.ID))
	} else {
		// destinationPath/resources/<rr-namespace>/<promise-name>/<rr-name>/<pipeline-name>/<dir-sha>/
		return filepath.Join(destinationPath, resourcesDir, workPlacement.GetNamespace(), workPlacement.Spec.PromiseName, workPlacement.Spec.ResourceName, workPlacement.PipelineName(), shortID(workPlacement.Spec.ID))
	}
}
