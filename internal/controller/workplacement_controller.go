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
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/internal/telemetry"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/delivery"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/writers"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"go.opentelemetry.io/otel/attribute"
)

const (
	resourcesDir                             = "resources"
	dependenciesDir                          = "dependencies"
	repoCleanupWorkPlacementFinalizer        = "finalizers.workplacement.kratix.io/repo-cleanup"
	kratixFileCleanupWorkPlacementFinalizer  = "finalizers.workplacement.kratix.io/kratix-dot-files-cleanup"
	scheduleSucceededConditionMismatchReason = "DestinationSelectorMismatch"
	scheduleSucceededConditionMismatchMsg    = "Target destination no longer matches destinationSelectors"
	deliveryModeChangedReason                = "DeliveryModeChanged"
)

type StateFile struct {
	Files []string `json:"files"`
}

// WorkPlacementReconciler reconciles a WorkPlacement object
type WorkPlacementReconciler struct {
	Client client.Client
	// APIReader reads a WorkPlacement past the cache before its removal is delivered.
	APIReader     client.Reader
	Log           logr.Logger
	VersionCache  map[string]string
	EventRecorder events.EventRecorder

	RepositoryCache RepositoryCache

	// StateStoreReconciliationInterval is how often a WorkPlacement re-writes its
	// files to the state store even when nothing has changed. When zero, no periodic
	// re-write is scheduled and the WorkPlacement is only reconciled on events.
	StateStoreReconciliationInterval time.Duration
}

type workPlacementReconcileContext struct {
	ctx        context.Context
	controller string

	logger        logr.Logger
	trace         *reconcileTrace
	client        client.Client
	apiReader     client.Reader
	eventRecorder events.EventRecorder

	workPlacement   *v1alpha1.WorkPlacement
	destination     *v1alpha1.Destination
	repositoryCache RepositoryCache

	versionCache map[string]string

	cachedWorkloadsHash string

	stateStoreReconciliationInterval time.Duration
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/finalizers,verbs=update

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

func (r *WorkPlacementReconciler) newReconcileContext(ctx context.Context, logger logr.Logger, req ctrl.Request) (*workPlacementReconcileContext, error) {
	workPlacement := &v1alpha1.WorkPlacement{}
	if err := r.Client.Get(ctx, req.NamespacedName, workPlacement); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	dest := &v1alpha1.Destination{}
	targetDestination := workPlacement.Spec.TargetDestinationName
	logger = logger.WithValues("destination", targetDestination)
	key := client.ObjectKey{Name: targetDestination}

	if err := r.Client.Get(ctx, key, dest); err != nil {
		if apierrors.IsNotFound(err) {
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
		logger:          logger.WithValues("generation", workPlacement.GetGeneration()),
		client:          r.Client,
		apiReader:       r.APIReader,
		eventRecorder:   r.EventRecorder,
		workPlacement:   workPlacement,
		destination:     dest,
		repositoryCache: r.RepositoryCache,
		versionCache:    r.VersionCache,

		stateStoreReconciliationInterval: r.StateStoreReconciliationInterval,
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
	if w.destination.Spec.StateStoreRef.IsManagedByCustomController() {
		logging.Debug(w.logger, "state store kind is not managed by Kratix, skipping",
			"kind", w.destination.Spec.StateStoreRef.Kind)
		return ctrl.Result{}, nil
	}

	if w.workPlacement.DeletionTimestamp.IsZero() && w.rejectedWorkloadsUnchanged() {
		return w.reconcileUnchangedRejection()
	}

	destinationReady := apiMeta.FindStatusCondition(w.destination.Status.Conditions, "Ready")
	destinationNotReady := destinationReady != nil && destinationReady.Status == metav1.ConditionFalse
	if destinationNotReady && w.workPlacement.DeletionTimestamp.IsZero() {
		return w.markDestinationNotReady(destinationReady.Message)
	}

	repo, err := w.repositoryCache.GetRepositoryByTypeAndName(
		w.destination.Spec.StateStoreRef.Kind,
		w.destination.Spec.StateStoreRef.Name,
	)
	if err != nil {
		return w.repositoryUnavailable(err)
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
			if !apierrors.IsConflict(err) {
				return ctrl.Result{}, err
			}
		}
		return fastRequeue, nil
	}

	if w.deliveryModeChangedTo(repo.DeliveryMode) && w.holdsEarlierDelivery() {
		return w.deliverAgainWithNewMode(repo.DeliveryMode)
	}

	if receipt := w.workPlacement.Status.DeliveryReceipt; receipt != "" {
		return w.resolveOutstandingDelivery(repo, receipt)
	}

	if err := w.recordDeliveryModeBeforeFirstDelivery(repo.DeliveryMode); err != nil {
		return ctrl.Result{}, err
	}

	if err := w.clearSupersededFailure(); err != nil {
		return ctrl.Result{}, err
	}

	written, err := w.writeToStateStore(repo)
	if err != nil {
		return ctrl.Result{}, err
	}
	return w.recordDeliveryResult(repo.DeliveryMode, written)
}

func (w *workPlacementReconcileContext) repositoryUnavailable(err error) (ctrl.Result, error) {
	if errors.Is(err, ErrCacheMiss) {
		logging.Debug(w.logger, "repository not initialised, requeuing")
		return defaultRequeue, nil
	}
	return ctrl.Result{}, err
}

func (w *workPlacementReconcileContext) reconcileUnchangedRejection() (ctrl.Result, error) {
	repo, err := w.repositoryCache.GetRepositoryByTypeAndName(
		w.destination.Spec.StateStoreRef.Kind,
		w.destination.Spec.StateStoreRef.Name,
	)
	if err != nil {
		return w.repositoryUnavailable(err)
	}
	if w.deliveryModeChangedTo(repo.DeliveryMode) {
		return w.deliverAgainWithNewMode(repo.DeliveryMode)
	}
	return w.updateRejectedGeneration(repo.DeliveryMode)
}

func (w *workPlacementReconcileContext) rejectedWorkloadsUnchanged() bool {
	written := apiMeta.FindStatusCondition(w.workPlacement.Status.Conditions, v1alpha1.WriteSucceededConditionType)
	if written == nil || written.Reason != v1alpha1.WorkloadsRejectedReason {
		return false
	}
	if w.workPlacement.Status.DeliveryWorkloadsHash == "" {
		return written.ObservedGeneration == w.workPlacement.GetGeneration()
	}
	return w.workPlacement.Status.DeliveryWorkloadsHash == w.currentWorkloadsHash()
}

func (w *workPlacementReconcileContext) updateRejectedGeneration(mode string) (ctrl.Result, error) {
	written := apiMeta.FindStatusCondition(w.workPlacement.Status.Conditions, v1alpha1.WriteSucceededConditionType)
	generationChanged := written.ObservedGeneration != w.workPlacement.GetGeneration()
	hashMissing := w.workPlacement.Status.DeliveryWorkloadsHash == ""
	modeMissing := w.workPlacement.Status.DeliveryMode == nil
	if !generationChanged && !hashMissing && !modeMissing {
		return ctrl.Result{}, nil
	}

	w.workPlacement.SetWriteRejectedCondition(written.Message)
	if hashMissing {
		w.workPlacement.Status.DeliveryWorkloadsHash = w.currentWorkloadsHash()
	}
	if modeMissing {
		w.workPlacement.Status.DeliveryMode = &mode
	}
	if err := w.client.Status().Update(w.ctx, w.workPlacement); err != nil {
		if apierrors.IsConflict(err) {
			return fastRequeue, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// currentWorkloadsHash hashes each file's path and decompressed content, once per spec read
func (w *workPlacementReconcileContext) currentWorkloadsHash() string {
	if w.cachedWorkloadsHash != "" {
		return w.cachedWorkloadsHash
	}
	var sb strings.Builder
	for _, workload := range w.workPlacement.Spec.Workloads {
		content := []byte(workload.Content)
		if decompressed, err := compression.DecompressContent(content); err == nil {
			content = decompressed
		}
		fmt.Fprintf(&sb, "%s\x00%s\x00", workload.Filepath, content)
	}
	w.cachedWorkloadsHash = hash.ComputeHash(sb.String())
	return w.cachedWorkloadsHash
}

func (w *workPlacementReconcileContext) markDestinationNotReady(destinationMessage string) (ctrl.Result, error) {
	logging.Debug(w.logger, "destination is not ready, skipping write")
	message := fmt.Sprintf("Destination %q is not ready: %s", w.destination.GetName(), destinationMessage)
	if !w.workPlacement.SetDestinationNotReadyCondition(message) {
		return defaultRequeue, nil
	}

	if err := w.client.Status().Update(w.ctx, w.workPlacement); err != nil {
		if apierrors.IsConflict(err) {
			return fastRequeue, nil
		}
		return ctrl.Result{}, err
	}
	return defaultRequeue, nil
}

func (w *workPlacementReconcileContext) updateWriteFailedStatus(err error) error {
	if !w.workPlacement.SetWriteFailedCondition(err) {
		return nil
	}
	logging.Debug(w.logger, "updating workplacement status")
	return w.client.Status().Update(w.ctx, w.workPlacement)
}

func (w *workPlacementReconcileContext) updateWrittenStatus(mode string, written delivery.Result) error {
	versionID := w.getCachedVersionID(written.VersionID)

	versionChanged := versionID != "" && w.workPlacement.Status.VersionID != versionID
	if versionChanged {
		w.workPlacement.Status.VersionID = versionID
	}
	urlChanged := w.workPlacement.Status.DeliveryURL != written.URL
	w.workPlacement.Status.DeliveryURL = written.URL
	w.workPlacement.Status.DeliveryWorkloadsHash = ""
	modeChanged := !w.deliveryModeIs(mode)
	w.workPlacement.Status.DeliveryMode = &mode
	writeSucceededCondChanged := w.workPlacement.SetWriteSucceededCondition()
	condChanged := w.workPlacement.SetWorkplacementReadyStatus()

	if versionChanged || urlChanged || modeChanged || writeSucceededCondChanged || condChanged {
		logging.Debug(w.logger, "updating workplacement status")
		if err := w.client.Status().Update(w.ctx, w.workPlacement); err != nil {
			return err
		}
	}

	w.removeVersionIDFromCache()
	return nil
}

func (w *workPlacementReconcileContext) publishWriteEvent(reason, versionID string, err error) {
	if err != nil {
		w.eventRecorder.Eventf(w.workPlacement, nil, v1.EventTypeWarning, reason, reason, "%s", fmt.Sprintf("failed writing to Destination: %s with error: %s; check kubectl get destination for more info", w.workPlacement.Spec.TargetDestinationName, err.Error()))
		return
	}

	if versionID != "" {
		w.eventRecorder.Eventf(w.workPlacement, nil, v1.EventTypeNormal, reason, reason, "successfully written to Destination: %s with versionID: %s", w.workPlacement.Spec.TargetDestinationName, versionID)
		return
	}

	w.eventRecorder.Eventf(w.workPlacement, nil, v1.EventTypeNormal, reason, reason, "successfully written to Destination: %s", w.workPlacement.Spec.TargetDestinationName)
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkPlacementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.WorkPlacement{}, builder.WithPredicates(workPlacementReconcilePredicate())).
		Watches(
			&v1alpha1.GitStateStore{},
			handler.EnqueueRequestsFromMapFunc(r.workPlacementsForStateStore(v1alpha1.GitStateStoreKind)),
			builder.WithPredicates(stateStoreObservedGenerationChanged()),
		).
		Watches(
			&v1alpha1.BucketStateStore{},
			handler.EnqueueRequestsFromMapFunc(r.workPlacementsForStateStore(v1alpha1.BucketStateStoreKind)),
			builder.WithPredicates(stateStoreObservedGenerationChanged()),
		).
		Complete(r)
}

func workPlacementReconcilePredicate() predicate.Funcs {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldDeletionTimestamp := e.ObjectOld.GetDeletionTimestamp()
			newDeletionTimestamp := e.ObjectNew.GetDeletionTimestamp()
			deletionStarted := (oldDeletionTimestamp == nil) != (newDeletionTimestamp == nil)

			return e.ObjectNew.GetGeneration() != e.ObjectOld.GetGeneration() || deletionStarted
		},
	}
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

func (w *workPlacementReconcileContext) logAndRecordError(err error, reason, message string, args ...any) {
	logging.Error(w.logger, err, message, args...)
	w.eventRecorder.Eventf(w.workPlacement, nil, v1.EventTypeWarning, reason, reason, "%s: %s", message, err.Error())
}

func (w *workPlacementReconcileContext) generateKratixStateFile(repo *Repository, pathPrefix string) (v1alpha1.Workload, StateFile, error) {
	oldStateFile, err := w.readKratixStateFile(repo, true)
	if err != nil {
		return v1alpha1.Workload{}, StateFile{}, err
	}

	workloadPaths := make([]string, 0, len(w.workPlacement.Spec.Workloads))
	for _, workload := range w.workPlacement.Spec.Workloads {
		workloadPaths = append(workloadPaths, filepath.Join(pathPrefix, workload.Filepath))
	}
	newStateFile := StateFile{Files: workloadPaths}
	stateFileContent, marshalErr := yaml.Marshal(newStateFile)
	if marshalErr != nil {
		return v1alpha1.Workload{}, StateFile{}, fmt.Errorf("failed to marshal new .kratix state file: %w", marshalErr)
	}

	kratixRelativePath := filepath.Join(pathPrefix, ".kratix", fmt.Sprintf("%s-%s.yaml", w.workPlacement.Namespace, w.workPlacement.Name))
	stateFileWorkload := v1alpha1.Workload{
		Filepath: kratixRelativePath,
		Content:  string(stateFileContent),
	}
	return stateFileWorkload, oldStateFile, nil
}

func (w *workPlacementReconcileContext) kratixStateFilePath() string {
	return filepath.Join(w.destination.Spec.Path, fmt.Sprintf(".kratix/%s-%s.yaml", w.workPlacement.Namespace, w.workPlacement.Name))
}

func (w *workPlacementReconcileContext) readKratixStateFile(repo *Repository, ignoreNotFound bool) (StateFile, error) {
	kratixFilePath := w.kratixStateFilePath()
	kratixFile, err := repo.Writer.ReadFile(kratixFilePath)
	if err != nil {
		if ignoreNotFound && errors.Is(err, writers.ErrFileNotFound) {
			return StateFile{}, nil
		}
		w.logAndRecordError(err, "FailedReadKratixStateFile", "failed to read .kratix state file", "filePath", kratixFilePath)
		return StateFile{}, err
	}

	stateFile := StateFile{}
	if err = yaml.Unmarshal(kratixFile, &stateFile); err != nil {
		w.logAndRecordError(err, "FailedUnmarshalKratixStateFile", "failed to unmarshal .kratix state file")
		return StateFile{}, err
	}

	return stateFile, nil
}

func (w *workPlacementReconcileContext) getAggregatedWorkload() (v1alpha1.Workload, error) {
	filename := w.destination.Spec.Filepath.Filename
	if filename == "" {
		filename = "aggregated.yaml"
	}
	workload := v1alpha1.Workload{
		Filepath: filepath.Join(w.destination.Spec.Path, filename),
	}
	activeWorkplacements, err := w.getAllWorkplacementsForDestination()
	if err != nil {
		w.logAndRecordError(err, "FailedGetAllWorkplacementsForDestination", "failed to get all workplacements for destination")
		return v1alpha1.Workload{}, err
	}

	if len(activeWorkplacements) == 0 {
		return workload, nil
	}

	combinedWorkloads, err := w.combineAllWorkloads(activeWorkplacements)
	if err != nil {
		w.logAndRecordError(err, "FailedGenerateAggregatedWorkloadYAML", "failed to generate aggregated workload yaml")
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

func (w *workPlacementReconcileContext) releaseFinalizers() (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(w.workPlacement, repoCleanupWorkPlacementFinalizer)
	controllerutil.RemoveFinalizer(w.workPlacement, kratixFileCleanupWorkPlacementFinalizer)

	if err := w.client.Update(w.ctx, w.workPlacement); err != nil {
		if apierrors.IsConflict(err) {
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// defaultDeliveryCheckInterval is the interval a pending delivery is verified
const defaultDeliveryCheckInterval = 30 * time.Second

// resolveOutstandingDelivery verifies if a pending delivery has been completed
func (w *workPlacementReconcileContext) resolveOutstandingDelivery(repo *Repository, receipt string) (ctrl.Result, error) {
	// Resolving a delivery the workloads have moved past would mark stale content as written.
	if w.workPlacement.Status.DeliveryGeneration != w.workPlacement.GetGeneration() &&
		w.workPlacement.Status.DeliveryWorkloadsHash != w.currentWorkloadsHash() {
		w.clearOutstandingDelivery()
		return fastRequeue, w.client.Status().Update(w.ctx, w.workPlacement)
	}

	result, err := repo.Writer.Check(w.ctx, receipt)
	if errors.Is(err, delivery.ErrUnknownReceipt) {
		// Keeping a receipt the writer cannot resolve would stop the WorkPlacement being written again.
		w.clearOutstandingDelivery()
		w.workPlacement.Status.DeliveryURL = ""
		return fastRequeue, w.client.Status().Update(w.ctx, w.workPlacement)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return w.recordDeliveryResult(repo.DeliveryMode, result)
}

// recordDeliveryModeBeforeFirstDelivery saves the mode so a deleting Work waits for whatever the first delivery opens
func (w *workPlacementReconcileContext) recordDeliveryModeBeforeFirstDelivery(mode string) error {
	if w.workPlacement.Status.DeliveryMode != nil {
		return nil
	}
	_, err := w.persistDeliveryStatus(func() bool {
		if w.workPlacement.Status.DeliveryMode != nil {
			return false
		}
		w.workPlacement.Status.DeliveryMode = &mode
		return true
	})
	return err
}

// clearSupersededFailure marks the WorkPlacement as Pending before a new delivery starts when the generation has changed
func (w *workPlacementReconcileContext) clearSupersededFailure() error {
	written := apiMeta.FindStatusCondition(w.workPlacement.Status.Conditions, v1alpha1.WriteSucceededConditionType)
	if written == nil || written.Status != metav1.ConditionFalse ||
		written.ObservedGeneration == w.workPlacement.GetGeneration() {
		return nil
	}

	_, err := w.persistDeliveryStatus(func() bool {
		w.workPlacement.SetWriteDeferredCondition("delivering the current workloads")
		return true
	})
	return err
}

// persistDeliveryStatus retries on conflict: losing a receipt strands the delivery it names.
// apply returns false to drop a result the re-read WorkPlacement has moved past.
func (w *workPlacementReconcileContext) persistDeliveryStatus(apply func() bool) (bool, error) {
	applied := true
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if applied = apply(); !applied {
			return nil
		}
		err := w.client.Status().Update(w.ctx, w.workPlacement)
		if apierrors.IsConflict(err) {
			if getErr := w.client.Get(w.ctx, client.ObjectKeyFromObject(w.workPlacement), w.workPlacement); getErr != nil {
				return getErr
			}
			w.cachedWorkloadsHash = ""
		}
		return err
	})
	return applied, err
}

func checkInterval(result delivery.Result) time.Duration {
	if result.CheckAfter <= 0 {
		return defaultDeliveryCheckInterval
	}
	return result.CheckAfter
}

func (w *workPlacementReconcileContext) recordDeliveryResult(mode string, result delivery.Result) (ctrl.Result, error) {
	switch result.Outcome {
	case delivery.Pending:
		return w.recordPendingDelivery(mode, result, false)

	case delivery.Rejected:
		rejectedHash := w.currentWorkloadsHash()
		applied, err := w.persistDeliveryStatus(func() bool {
			if w.currentWorkloadsHash() != rejectedHash {
				return false
			}
			w.clearOutstandingDelivery()
			w.workPlacement.Status.DeliveryURL = result.URL
			w.workPlacement.Status.DeliveryWorkloadsHash = rejectedHash
			w.workPlacement.Status.DeliveryMode = &mode
			w.workPlacement.SetWriteRejectedCondition(resourceutil.CutToRunes(result.Reason, writeMessageMaxRunes))
			return true
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		if !applied {
			return fastRequeue, nil
		}
		return ctrl.Result{}, nil

	case delivery.Delivered:
		w.clearOutstandingDelivery()
		w.setVersionID(result.VersionID)
		w.publishWriteEvent("WorkloadsWrittenToStateStore", result.VersionID, nil)
		if err := w.updateWrittenStatus(mode, result); err != nil {
			if apierrors.IsConflict(err) {
				return fastRequeue, nil
			}
			return defaultRequeue, nil
		}
		return ctrl.Result{RequeueAfter: w.stateStoreReconciliationInterval}, nil

	default:
		return ctrl.Result{}, fmt.Errorf("writer reported unknown delivery outcome %q", result.Outcome)
	}
}

// recordPendingDelivery saves removing as given, never read from the WorkPlacement: a re-read
// after a conflict can be deleting while the delivery it records is an update.
func (w *workPlacementReconcileContext) recordPendingDelivery(mode string, result delivery.Result, removing bool) (ctrl.Result, error) {
	deliveredGeneration := w.workPlacement.GetGeneration()
	deliveredHash := w.currentWorkloadsHash()
	applied, err := w.persistDeliveryStatus(func() bool {
		if removalWasRejected(*w.workPlacement) {
			return false
		}
		w.workPlacement.Status.DeliveryReceipt = result.Receipt
		w.workPlacement.Status.DeliveryGeneration = deliveredGeneration
		w.workPlacement.Status.DeliveryWorkloadsHash = deliveredHash
		w.workPlacement.Status.DeliveryURL = result.URL
		w.workPlacement.Status.DeliveryMode = &mode
		w.workPlacement.Status.DeliveryRemoval = removing
		w.workPlacement.SetWriteDeferredCondition(resourceutil.CutToRunes(result.Reason, writeMessageMaxRunes))
		return true
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if !applied {
		return fastRequeue, nil
	}
	return ctrl.Result{RequeueAfter: checkInterval(result)}, nil
}

func (w *workPlacementReconcileContext) writeToStateStore(repo *Repository) (delivery.Result, error) {
	metricAttrs := telemetry.WorkPlacementWriteAttributes(
		w.workPlacement.Spec.PromiseName,
		w.workPlacement.Spec.ResourceName,
		w.workPlacement.Spec.TargetDestinationName,
		w.workPlacement.PipelineName(),
	)

	logging.Debug(w.logger, "writing files to state store")
	result, workloadErr := w.writeWorkloadsToStateStore(repo)
	if workloadErr != nil {
		telemetry.RecordWorkPlacementWrite(w.ctx, telemetry.WorkPlacementWriteResultFailure, metricAttrs...)
		w.logAndRecordError(workloadErr, "FailedWriteToDestination", "error writing to destination; check kubectl get destination for more info")
		if err := w.updateWriteFailedStatus(workloadErr); err != nil {
			return delivery.Result{}, err
		}
		return delivery.Result{}, workloadErr
	}

	if result.Outcome == delivery.Delivered {
		telemetry.RecordWorkPlacementWrite(w.ctx, telemetry.WorkPlacementWriteResultSuccess, metricAttrs...)
	}
	return result, nil
}

func (w *workPlacementReconcileContext) writeWorkloadsToStateStore(repo *Repository) (delivery.Result, error) {
	var workloadsToDelete []string
	var workloadsToCreate []v1alpha1.Workload
	dir := getDir(w.destination.Spec.Path, *w.workPlacement) + "/"

	switch w.destination.GetFilepathMode() {

	case v1alpha1.FilepathModeAggregatedYAML:
		dir = ""
		workload, err := w.getAggregatedWorkload()
		if err != nil {
			return delivery.Result{}, err
		}
		workloadsToCreate = []v1alpha1.Workload{workload}

	case v1alpha1.FilepathModeNone:
		pathPrefix := w.destination.Spec.Path + "/"
		dir = ""
		newWorkload, oldStateFile, err := w.generateKratixStateFile(repo, pathPrefix)
		if err != nil {
			return delivery.Result{}, err
		}
		workloadsToCreate = append(workloadsToCreate, newWorkload)
		workloadsToDelete = cleanupWorkloadsWithPrefix(oldStateFile.Files, w.workPlacement.Spec.Workloads, pathPrefix)

		for _, workload := range w.workPlacement.Spec.Workloads {
			decompressedContent, err := compression.DecompressContent([]byte(workload.Content))
			if err != nil {
				return delivery.Result{}, fmt.Errorf("unable to decompress file content: %w", err)
			}

			workload.Content = string(decompressedContent)
			workload.Filepath = filepath.Join(pathPrefix, workload.Filepath)
			workloadsToCreate = append(workloadsToCreate, workload)
		}

	case v1alpha1.FilepathModeNestedByMetadata:
		logging.Trace(w.logger, "handling file path mode nestedByMetadata")

		for _, workload := range w.workPlacement.Spec.Workloads {
			decompressedContent, err := compression.DecompressContent([]byte(workload.Content))
			if err != nil {
				return delivery.Result{}, fmt.Errorf("unable to decompress file content: %w", err)
			}

			workload.Content = string(decompressedContent)
			workloadsToCreate = append(workloadsToCreate, workload)
		}

	default:
		return delivery.Result{}, fmt.Errorf("unsupported file path mode: %s", w.destination.GetFilepathMode())
	}

	result, err := repo.Writer.Deliver(w.ctx, w.deliveryTarget(dir, false), workloadsToCreate, workloadsToDelete)
	if err != nil {
		logging.Error(w.logger, err, "error writing resources to repository")
		return delivery.Result{}, err
	}
	return result, nil
}

func (w *workPlacementReconcileContext) deliveryTarget(dir string, removing bool) delivery.Target {
	return delivery.Target{
		Dir:           dir,
		Namespace:     w.workPlacement.GetNamespace(),
		WorkPlacement: w.workPlacement.GetName(),
		Promise:       w.workPlacement.Spec.PromiseName,
		Resource:      w.workPlacement.Spec.ResourceName,
		Pipeline:      w.workPlacement.PipelineName(),
		Destination:   w.workPlacement.Spec.TargetDestinationName,
		Removing:      removing,
	}
}

func (w *workPlacementReconcileContext) deleteWorkPlacement(repo *Repository) (ctrl.Result, error) {
	status := w.workPlacement.Status
	if status.DeliveryReceipt != "" && !status.DeliveryRemoval {
		if dropped, err := w.dropDeliveryForRemoval(status.DeliveryReceipt); !dropped || err != nil {
			return fastRequeue, err
		}
		return w.deliverRemoval(repo, status.DeliveryReceipt)
	}

	if status.DeliveryReceipt != "" {
		return w.resolveOutstandingRemoval(repo, status.DeliveryReceipt)
	}

	if status.DeliveryRemoval && w.writeRejected() {
		return ctrl.Result{}, nil
	}
	return w.deliverRemoval(repo, "")
}

// dropDeliveryForRemoval forgets receipt so the removal can be delivered. It returns false when the
// re-read WorkPlacement already holds a different removal, which a stale copy must never clear.
func (w *workPlacementReconcileContext) dropDeliveryForRemoval(receipt string) (bool, error) {
	return w.persistDeliveryStatus(func() bool {
		current := w.workPlacement.Status
		if current.DeliveryRemoval && current.DeliveryReceipt != receipt {
			return false
		}
		w.clearOutstandingDelivery()
		w.workPlacement.Status.DeliveryURL = ""
		w.workPlacement.SetWriteDeferredCondition("removing the workloads")
		return true
	})
}

func (w *workPlacementReconcileContext) resolveOutstandingRemoval(repo *Repository, receipt string) (ctrl.Result, error) {
	result, err := repo.Writer.Check(w.ctx, receipt)
	if errors.Is(err, delivery.ErrUnknownReceipt) {
		if dropped, err := w.dropDeliveryForRemoval(receipt); !dropped || err != nil {
			return fastRequeue, err
		}
		return w.deliverRemoval(repo, "")
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return w.recordRemovalResult(repo.DeliveryMode, result)
}

func (w *workPlacementReconcileContext) deliverRemoval(repo *Repository, supersedes string) (ctrl.Result, error) {
	if held, err := w.removalHeldOnServer(); held || err != nil {
		return fastRequeue, err
	}

	dir, create, remove, err := w.removal(repo)
	if err != nil {
		logging.Debug(w.logger, "failed to list the files to remove", "error", err)
		return defaultRequeue, nil
	}

	target := w.deliveryTarget(dir, true)
	target.Supersedes = supersedes
	result, err := repo.Writer.Deliver(w.ctx, target, create, remove)
	if err != nil {
		w.logAndRecordError(err, "FailedDeleteFilesFromRepository", "failed to delete files from repository")
		if err := w.updateWriteFailedStatus(err); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
		return defaultRequeue, nil
	}
	return w.recordRemovalResult(repo.DeliveryMode, result)
}

// removalHeldOnServer is true when the API server holds a pending or rejected removal that the cached copy missed
func (w *workPlacementReconcileContext) removalHeldOnServer() (bool, error) {
	latest := &v1alpha1.WorkPlacement{}
	if err := w.apiReader.Get(w.ctx, client.ObjectKeyFromObject(w.workPlacement), latest); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return latest.Status.DeliveryRemoval && (latest.Status.DeliveryReceipt != "" || removalWasRejected(*latest)), nil
}

// removal lists what to deliver so the WorkPlacement's files leave the state store.
// A named dir is replaced wholesale, which removes it; listing it in remove would not.
func (w *workPlacementReconcileContext) removal(repo *Repository) (string, []v1alpha1.Workload, []string, error) {
	var (
		dir    string
		create []v1alpha1.Workload
		remove []string
	)

	if controllerutil.ContainsFinalizer(w.workPlacement, repoCleanupWorkPlacementFinalizer) {
		logging.Debug(w.logger, "removing files from repository")

		switch filePathMode := w.destination.GetFilepathMode(); filePathMode {
		case v1alpha1.FilepathModeNestedByMetadata:
			dir = getDir(w.destination.Spec.Path, *w.workPlacement) + "/"

		case v1alpha1.FilepathModeNone:
			stateFile, err := w.readKratixStateFile(repo, true)
			if err != nil {
				return "", nil, nil, err
			}
			remove = withWorkloadPaths(stateFile.Files, w.workPlacement.Spec.Workloads, w.destination.Spec.Path+"/")

		case v1alpha1.FilepathModeAggregatedYAML:
			workload, err := w.getAggregatedWorkload()
			if err != nil {
				return "", nil, nil, err
			}
			if workload.Content != "" {
				create = []v1alpha1.Workload{workload}
			} else {
				remove = []string{workload.Filepath}
			}

		default:
			return "", nil, nil, fmt.Errorf("unsupported file path mode: %s", filePathMode)
		}
	}

	if controllerutil.ContainsFinalizer(w.workPlacement, kratixFileCleanupWorkPlacementFinalizer) {
		logging.Debug(w.logger, "cleaning up .kratix state file")
		remove = append(remove, w.kratixStateFilePath())
	}
	return dir, create, remove, nil
}

// withWorkloadPaths adds the workloads' paths to files once each, since a stale state file can miss some
func withWorkloadPaths(files []string, workloads []v1alpha1.Workload, pathPrefix string) []string {
	var paths []string
	seen := map[string]bool{}
	add := func(path string) {
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	for _, file := range files {
		add(file)
	}
	for _, workload := range workloads {
		add(filepath.Join(pathPrefix, workload.Filepath))
	}
	return paths
}

func (w *workPlacementReconcileContext) recordRemovalResult(mode string, result delivery.Result) (ctrl.Result, error) {
	switch result.Outcome {
	case delivery.Pending:
		return w.recordPendingDelivery(mode, result, true)

	case delivery.Rejected:
		// The request shows only the start of this message, so Kratix's sentence comes first.
		message := fmt.Sprintf("Removal rejected; the workloads are still on %s. %s",
			w.workPlacement.Spec.TargetDestinationName, result.Reason)
		if _, err := w.persistDeliveryStatus(func() bool {
			w.clearOutstandingDelivery()
			w.workPlacement.Status.DeliveryRemoval = true
			w.workPlacement.Status.DeliveryURL = result.URL
			w.workPlacement.Status.DeliveryMode = &mode
			w.workPlacement.SetWriteRejectedCondition(resourceutil.CutToRunes(message, writeMessageMaxRunes))
			return true
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil

	case delivery.Delivered:
		return w.releaseFinalizers()

	default:
		return ctrl.Result{}, fmt.Errorf("writer reported unknown delivery outcome %q", result.Outcome)
	}
}

func (w *workPlacementReconcileContext) writeRejected() bool {
	written := apiMeta.FindStatusCondition(w.workPlacement.Status.Conditions, v1alpha1.WriteSucceededConditionType)
	return written != nil && written.Status == metav1.ConditionFalse && written.Reason == v1alpha1.WorkloadsRejectedReason
}

func cleanupWorkloadsWithPrefix(oldWorkloads []string, newWorkloads []v1alpha1.Workload, pathPrefix string) []string {
	works := make(map[string]bool)
	for _, w := range newWorkloads {
		works[filepath.Join(pathPrefix, w.Filepath)] = true
	}
	var result []string
	for _, old := range oldWorkloads {
		oldFullPath := old
		if !strings.HasPrefix(old, pathPrefix) {
			oldFullPath = filepath.Join(pathPrefix, old)
		}
		if _, ok := works[oldFullPath]; !ok {
			result = append(result, oldFullPath)
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

func (w *workPlacementReconcileContext) holdsEarlierDelivery() bool {
	if w.workPlacement.Status.DeliveryReceipt != "" {
		return true
	}
	return w.writeRejected()
}

// deliverAgainWithNewMode drops the outstanding or rejected delivery, which the new mode's writer cannot resolve.
func (w *workPlacementReconcileContext) deliverAgainWithNewMode(mode string) (ctrl.Result, error) {
	earlierDelivery := "The earlier delivery"
	if url := w.workPlacement.Status.DeliveryURL; url != "" {
		earlierDelivery += ", " + url + ","
	}
	message := fmt.Sprintf("delivery mode changed from %q to %q; delivering the workloads again with the new mode. %s is left as it is",
		deliveryModeName(*w.workPlacement.Status.DeliveryMode), deliveryModeName(mode), earlierDelivery)

	if _, err := w.persistDeliveryStatus(func() bool {
		w.clearOutstandingDelivery()
		w.workPlacement.Status.DeliveryWorkloadsHash = ""
		w.workPlacement.Status.DeliveryURL = ""
		w.workPlacement.Status.DeliveryMode = &mode
		w.workPlacement.SetWriteDeferredCondition("delivering the current workloads")
		return true
	}); err != nil {
		return ctrl.Result{}, err
	}

	logging.Warn(w.logger, message)
	w.eventRecorder.Eventf(w.workPlacement, nil, v1.EventTypeWarning, deliveryModeChangedReason, deliveryModeChangedReason, "%s", message)
	return fastRequeue, nil
}

func deliveryModeName(mode string) string {
	if mode == delivery.BuiltIn {
		return "built-in"
	}
	return mode
}

func (w *workPlacementReconcileContext) deliveryModeIs(mode string) bool {
	recorded := w.workPlacement.Status.DeliveryMode
	return recorded != nil && *recorded == mode
}

// deliveryModeChangedTo is false while no mode is recorded: an unknown mode is not a change.
func (w *workPlacementReconcileContext) deliveryModeChangedTo(mode string) bool {
	return w.workPlacement.Status.DeliveryMode != nil && !w.deliveryModeIs(mode)
}

func (w *workPlacementReconcileContext) clearOutstandingDelivery() {
	w.workPlacement.Status.DeliveryReceipt = ""
	w.workPlacement.Status.DeliveryGeneration = 0
	w.workPlacement.Status.DeliveryRemoval = false
}

// workPlacementsForStateStore lists the WorkPlacements to deliver again once the state store's writer is rebuilt.
func (r *WorkPlacementReconciler) workPlacementsForStateStore(stateStoreKind string) handler.MapFunc {
	return func(ctx context.Context, stateStore client.Object) []reconcile.Request {
		destinations := &v1alpha1.DestinationList{}
		if err := r.Client.List(ctx, destinations); err != nil {
			logging.Error(r.Log, err, "error listing destinations for state store", "stateStore", stateStore.GetName())
			return nil
		}

		var requests []reconcile.Request
		for _, destination := range destinations.Items {
			ref := destination.Spec.StateStoreRef
			if ref == nil || ref.IsManagedByCustomController() ||
				ref.Kind != stateStoreKind || ref.Name != stateStore.GetName() {
				continue
			}

			workPlacements := &v1alpha1.WorkPlacementList{}
			if err := r.Client.List(ctx, workPlacements, client.MatchingLabels{
				TargetDestinationNameLabel: destination.GetName(),
			}); err != nil {
				logging.Error(r.Log, err, "error listing workplacements for destination", "destination", destination.GetName())
				continue
			}
			for _, workPlacement := range workPlacements.Items {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&workPlacement)})
			}
		}
		return requests
	}
}

// stateStoreObservedGenerationChanged passes a state store once its controller has rebuilt the writer.
func stateStoreObservedGenerationChanged() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return false },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldStore, oldOK := e.ObjectOld.(StateStore)
			newStore, newOK := e.ObjectNew.(StateStore)
			return oldOK && newOK && oldStore.GetObservedGeneration() != newStore.GetObservedGeneration()
		},
	}
}

// removalWasRejected is true when a reviewer turned down the WorkPlacement's removal.
func removalWasRejected(wp v1alpha1.WorkPlacement) bool {
	write := apiMeta.FindStatusCondition(wp.Status.Conditions, v1alpha1.WriteSucceededConditionType)
	return wp.Status.DeliveryRemoval && write != nil && write.Reason == v1alpha1.WorkloadsRejectedReason
}
