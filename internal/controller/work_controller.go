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
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/internal/telemetry"
	"github.com/syntasso/kratix/lib/resourceutil"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
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
	workCleanUpFinalizer              = v1alpha1.KratixPrefix + "work-cleanup"
	workWriteSucceededMessageMaxRunes = 4096
	// writeMessageMaxRunes keeps a WorkPlacement's write message readable, and
	// leaves room for several in the Work's WriteSucceeded message
	writeMessageMaxRunes = 1024
)

// WorkReconciler reconciles a Work object.
type WorkReconciler struct {
	Client client.Client
	// APIReader lists a deleting Work's WorkPlacements past the cache.
	APIReader     client.Reader
	Log           logr.Logger
	Scheduler     WorkScheduler
	EventRecorder events.EventRecorder
}

//counterfeiter:generate . WorkScheduler
type WorkScheduler interface {
	ReconcileWork(ctx context.Context, work *v1alpha1.Work) ([]string, error)
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=works,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=works/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=works/finalizers,verbs=update

func (r *WorkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	logger := r.Log.WithValues(
		"controller", "work",
		"name", req.Name,
		"namespace", req.Namespace,
	)
	work := &v1alpha1.Work{}
	err := r.Client.Get(ctx, req.NamespacedName, work)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logging.Error(logger, err, "error getting Work")
		return ctrl.Result{}, err
	}

	promiseName := work.Spec.PromiseName
	resourceName := work.Spec.ResourceName
	resourceNamespace := work.GetNamespace()
	baseLogger := logger.WithValues(
		"generation", work.GetGeneration(),
	)
	baseLogger = withPromiseAndResourceRequest(baseLogger, promiseName, resourceNamespace, resourceName)
	spanName := fmt.Sprintf("%s/WorkReconcile", promiseName)
	if resourceName != "" {
		spanName = fmt.Sprintf("%s/%s", resourceName, spanName)
	}
	ctx, logger, traceCtx := setupReconcileTrace(ctx, "work-controller", spanName, work, baseLogger)
	defer finishReconcileTrace(traceCtx, &retErr)()

	logging.Info(logger, "reconciliation started")
	defer logReconcileDuration(logger, time.Now(), &result, &retErr)()

	addWorkSpanAttributes(traceCtx, promiseName, work)

	if err := persistReconcileTrace(traceCtx, r.Client, logger); err != nil {
		logging.Error(logger, err, "failed to persist trace annotations")
		return ctrl.Result{}, err
	}

	if !work.DeletionTimestamp.IsZero() {
		waiting, err := r.deleteWork(ctx, logger, work)
		if err != nil {
			return ctrl.Result{}, err
		}
		if waiting {
			return defaultRequeue, nil
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(work, workCleanUpFinalizer) {
		o := opts{client: r.Client, logger: logger, ctx: ctx}
		if err := addFinalizers(o, work, []string{workFinalizer}); err != nil {
			if apierrors.IsConflict(err) {
				return fastRequeue, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	originalAnnotations := cloneStringMap(work.GetAnnotations())
	if traceCtx.HasTrace() {
		enriched := cloneStringMap(work.GetAnnotations())
		enriched = traceCtx.InjectTrace(enriched)
		work.SetAnnotations(enriched)
		defer work.SetAnnotations(originalAnnotations)
	}

	logging.Info(logger, "scheduling work")

	unscheduledWorkloadGroupIDs, err := r.Scheduler.ReconcileWork(ctx, work)
	if err != nil {
		if apierrors.IsConflict(err) {
			logging.Debug(logger, "failed to schedule Work due to update conflict; requeueing")
			return fastRequeue, nil
		}
		logging.Error(logger, err, "error scheduling Work; will retry")
		return ctrl.Result{}, err
	}

	if work.IsResourceRequest() && len(unscheduledWorkloadGroupIDs) > 0 {
		logging.Warn(logger, "no available destinations for some workload groups; trying again shortly", "workloadGroupIDs", unscheduledWorkloadGroupIDs)
		r.EventRecorder.Eventf(work, nil, v1.EventTypeNormal, "WaitingDestination", "WaitingDestination", "waiting for destination for workload group: [%s]",
			strings.Join(unscheduledWorkloadGroupIDs, ","),
		)
		if err := r.updateWriteState(ctx, logger, work); err != nil {
			return ctrl.Result{}, err
		}
		return slowRequeue, nil
	}

	if err := r.updateWorkStatus(ctx, logger, work); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *WorkReconciler) updateWorkStatus(ctx context.Context, logger logr.Logger, work *v1alpha1.Work) error {
	workplacements, err := r.listWorkPlacements(ctx, r.Client, logger, work)
	if err != nil {
		return err
	}

	writeStateChanged := setWorkPlacementWriteState(work, workplacements)
	readyChanged := r.setReadyCondition(work, workplacements)
	if writeStateChanged || readyChanged {
		return r.Client.Status().Update(ctx, work)
	}
	return nil
}

func (r *WorkReconciler) updateWriteState(ctx context.Context, logger logr.Logger, work *v1alpha1.Work) error {
	workplacements, err := r.listWorkPlacements(ctx, r.Client, logger, work)
	if err != nil {
		return err
	}

	if setWorkPlacementWriteState(work, workplacements) {
		return r.Client.Status().Update(ctx, work)
	}
	return nil
}

func (r *WorkReconciler) listWorkPlacements(ctx context.Context, reader client.Reader, logger logr.Logger, work *v1alpha1.Work) ([]v1alpha1.WorkPlacement, error) {
	workplacements, err := listWorkplacementWithLabels(ctx, reader, logger, work.GetNamespace(), map[string]string{
		workLabelKey: work.Name,
	})
	if err != nil {
		logging.Error(logger, err, "failed to list associated WorkPlacements")
	}
	return workplacements, err
}

func (r *WorkReconciler) setReadyCondition(work *v1alpha1.Work, workplacements []v1alpha1.WorkPlacement) bool {
	var failedWorkPlacements []string
	for _, wp := range workplacements {
		if removalWasRejected(wp) {
			continue
		}
		if apiMeta.IsStatusConditionFalse(wp.Status.Conditions, v1alpha1.WriteSucceededConditionType) {
			failedWorkPlacements = append(failedWorkPlacements, wp.GetName())
		}
	}

	// The scheduler owns ScheduleSucceeded, so a write failure only sets Ready.
	if len(failedWorkPlacements) > 0 {
		readyCond := metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Message:            "Failing",
			Reason:             "WorkplacementsFailing",
			ObservedGeneration: work.GetGeneration(),
		}
		if apiMeta.SetStatusCondition(&work.Status.Conditions, readyCond) {
			r.EventRecorder.Eventf(work, nil, v1.EventTypeWarning, "WorkplacementsFailing", "WorkplacementsFailing", "Workplacements failed to write: [%s]", strings.Join(failedWorkPlacements, ","))
			return true
		}
		return false
	}

	schedulerHasAlreadySetReady := !apiMeta.IsStatusConditionTrue(work.Status.Conditions, v1alpha1.ScheduleSucceededConditionType)
	if schedulerHasAlreadySetReady {
		return restoreSchedulerReadyCondition(work)
	}

	readyCond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "AllWorkplacementsWritten",
		Message:            "Ready",
		ObservedGeneration: work.GetGeneration(),
	}
	if !allWorkloadGroupsWrittenAtCurrentGeneration(work, workplacements) {
		readyCond = metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             "WaitingForWorkplacementsToBeWritten",
			Message:            "Pending",
			ObservedGeneration: work.GetGeneration(),
		}
	}
	return apiMeta.SetStatusCondition(&work.Status.Conditions, readyCond)
}

// restoreSchedulerReadyCondition puts back the scheduler's Ready once no WorkPlacement is failing
func restoreSchedulerReadyCondition(work *v1alpha1.Work) bool {
	scheduleSucceeded := apiMeta.FindStatusCondition(work.Status.Conditions, v1alpha1.ScheduleSucceededConditionType)
	ready := apiMeta.FindStatusCondition(work.Status.Conditions, "Ready")
	if scheduleSucceeded == nil || scheduleSucceeded.Status != metav1.ConditionFalse ||
		ready == nil || ready.Reason != "WorkplacementsFailing" {
		return false
	}
	readyCond := unscheduledReadyCondition(work.GetGeneration())
	if scheduleSucceeded.Reason == scheduleSucceededConditionMismatchReason {
		readyCond = misplacedReadyCondition(work.GetGeneration())
	}
	return apiMeta.SetStatusCondition(&work.Status.Conditions, readyCond)
}

// setWorkPlacementWriteState skips WorkPlacements being deleted unless their removal awaits delivery, failed or was rejected
func setWorkPlacementWriteState(work *v1alpha1.Work, workplacements []v1alpha1.WorkPlacement) bool {
	var live []v1alpha1.WorkPlacement
	statuses := []v1alpha1.WorkPlacementWriteStatus{}
	for _, wp := range workplacements {
		if wp.GetDeletionTimestamp().IsZero() {
			live = append(live, wp)
		} else if !removalIsUnresolved(wp) {
			continue
		}
		status := v1alpha1.WorkPlacementWriteStatus{
			Name:        wp.GetName(),
			Destination: wp.Spec.TargetDestinationName,
			DeliveryURL: wp.Status.DeliveryURL,
		}
		if write := apiMeta.FindStatusCondition(wp.Status.Conditions, v1alpha1.WriteSucceededConditionType); write != nil {
			status.Status = string(write.Status)
			status.Reason = write.Reason
			status.Message = resourceutil.CutToRunes(write.Message, writeMessageMaxRunes)
		}
		statuses = append(statuses, status)
	}
	slices.SortFunc(statuses, func(a, b v1alpha1.WorkPlacementWriteStatus) int { return strings.Compare(a.Name, b.Name) })

	listChanged := !slices.Equal(work.Status.WorkPlacementStatuses, statuses)
	if listChanged {
		work.Status.WorkPlacementStatuses = statuses
	}
	conditionChanged := apiMeta.SetStatusCondition(&work.Status.Conditions, workWriteSucceededCondition(work, live, statuses))
	return listChanged || conditionChanged
}

func workWriteSucceededCondition(work *v1alpha1.Work, live []v1alpha1.WorkPlacement, statuses []v1alpha1.WorkPlacementWriteStatus) metav1.Condition {
	matching := func(match func(v1alpha1.WorkPlacementWriteStatus) bool) []v1alpha1.WorkPlacementWriteStatus {
		return slices.DeleteFunc(slices.Clone(statuses), func(s v1alpha1.WorkPlacementWriteStatus) bool { return !match(s) })
	}
	condition := func(status metav1.ConditionStatus, reason, message string) metav1.Condition {
		return metav1.Condition{
			Type:               v1alpha1.WriteSucceededConditionType,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: work.GetGeneration(),
		}
	}

	failing := matching(func(s v1alpha1.WorkPlacementWriteStatus) bool {
		return s.Status == string(metav1.ConditionFalse) && s.Reason != v1alpha1.WorkloadsRejectedReason
	})
	rejected := matching(func(s v1alpha1.WorkPlacementWriteStatus) bool { return s.Reason == v1alpha1.WorkloadsRejectedReason })
	awaiting := matching(func(s v1alpha1.WorkPlacementWriteStatus) bool {
		return s.Reason == v1alpha1.WorkloadsAwaitingDeliveryReason
	})

	switch {
	case len(failing) > 0:
		return condition(metav1.ConditionFalse, v1alpha1.WorkplacementsFailingReason, writeStateMessage(failing))
	case len(rejected) > 0:
		return condition(metav1.ConditionFalse, v1alpha1.WorkplacementsRejectedReason, writeStateMessage(rejected))
	case len(awaiting) > 0:
		return condition(metav1.ConditionUnknown, v1alpha1.WorkplacementsAwaitingDeliveryReason, writeStateMessage(awaiting))
	case allWorkloadGroupsWrittenAtCurrentGeneration(work, live):
		return condition(metav1.ConditionTrue, v1alpha1.WorkplacementsWrittenReason, "All workloads written")
	default:
		return condition(metav1.ConditionUnknown, v1alpha1.WaitingForWorkplacementsToBeReconciledReason, "Waiting for WorkPlacements to be reconciled")
	}
}

func removalIsUnresolved(wp v1alpha1.WorkPlacement) bool {
	write := apiMeta.FindStatusCondition(wp.Status.Conditions, v1alpha1.WriteSucceededConditionType)
	return write != nil && (write.Reason == v1alpha1.WorkloadsAwaitingDeliveryReason || write.Status == metav1.ConditionFalse)
}

// deliversThroughRegisteredMode is false for the built-in mode and for WorkPlacements Kratix does not write
func deliversThroughRegisteredMode(wp v1alpha1.WorkPlacement) bool {
	if wp.Status.DeliveryReceipt != "" {
		return true
	}
	if wp.Status.DeliveryMode == nil {
		return controllerutil.ContainsFinalizer(&wp, repoCleanupWorkPlacementFinalizer)
	}
	return *wp.Status.DeliveryMode != ""
}

func writeStateMessage(statuses []v1alpha1.WorkPlacementWriteStatus) string {
	parts := make([]string, 0, len(statuses))
	for _, s := range statuses {
		parts = append(parts, s.Destination+": "+s.Message)
	}
	return resourceutil.CutToRunes(strings.Join(parts, "; "), workWriteSucceededMessageMaxRunes)
}

func allWorkloadGroupsWrittenAtCurrentGeneration(work *v1alpha1.Work, workplacements []v1alpha1.WorkPlacement) bool {
	for _, workloadGroup := range work.Spec.WorkloadGroups {
		workloadGroupHasWorkPlacement := false
		for _, wp := range workplacements {
			if wp.Spec.ID != workloadGroup.ID {
				continue
			}
			workloadGroupHasWorkPlacement = true
			if !wp.IsWrittenAtCurrentGeneration(work.GetGeneration()) {
				return false
			}
		}
		if !workloadGroupHasWorkPlacement {
			return false
		}
	}
	return true
}

// deleteWork reports whether the Work must wait for a WorkPlacement still removing its workloads
func (r *WorkReconciler) deleteWork(ctx context.Context, logger logr.Logger, work *v1alpha1.Work) (bool, error) {
	workplacementGVK := schema.GroupVersionKind{
		Group:   v1alpha1.GroupVersion.Group,
		Version: v1alpha1.GroupVersion.Version,
		Kind:    "WorkPlacement",
	}

	logging.Debug(logger, "deleting workplacements")

	wpList := &unstructured.UnstructuredList{}
	wpList.SetGroupVersionKind(workplacementGVK)
	selector := labels.SelectorFromSet(map[string]string{workLabelKey: work.Name})
	if err := r.Client.List(ctx, wpList, &client.ListOptions{LabelSelector: selector}); err != nil {
		r.EventRecorder.Eventf(work, nil, v1.EventTypeWarning, "FailedDelete", "FailedDelete", "failed to list associated workplacements: %s", err.Error())
		return false, err
	}

	parentAnnotations := work.GetAnnotations()
	deleteErrors := []string{}
	for i := range wpList.Items {
		wp := &wpList.Items[i]
		if err := ensureTraceAnnotations(ctx, r.Client, wp, parentAnnotations); err != nil {
			logging.Debug(logger, "Failed to ensure trace annotations are propagated, ignoring the error...", "error", err)
		}
		if err := r.Client.Delete(ctx, wp, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			deleteErrors = append(deleteErrors, err.Error())
			r.EventRecorder.Eventf(work, nil, v1.EventTypeWarning, "FailedDelete", "FailedDelete", "deleting associated workplacement failed: %s", err.Error())
		}
	}

	if len(deleteErrors) > 0 {
		return false, fmt.Errorf("failed to delete workplacements: %s", strings.Join(deleteErrors, ","))
	}

	remaining, err := r.listWorkPlacements(ctx, r.APIReader, logger, work)
	if err != nil {
		return false, err
	}
	if slices.ContainsFunc(remaining, deliversThroughRegisteredMode) {
		logging.Debug(logger, "waiting for workplacements to remove their workloads")
		if setWorkPlacementWriteState(work, remaining) {
			if err := r.Client.Status().Update(ctx, work); err != nil && !apierrors.IsConflict(err) {
				return true, err
			}
		}
		return true, nil
	}

	if controllerutil.RemoveFinalizer(work, workCleanUpFinalizer) {
		if err := r.Client.Update(ctx, work); err != nil {
			logging.Debug(logger, "Failed to remove finalizer, requeuing...", "error", err)
			return false, err
		}
	}

	logging.Debug(logger, "workplacements deleted")
	return false, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Work{}).
		Owns(&v1alpha1.WorkPlacement{}, builder.WithPredicates(workPlacementStatusUpdatePredicate())).
		Watches(
			&v1alpha1.Destination{},
			handler.EnqueueRequestsFromMapFunc(r.requestReconciliationOfWorksOnDestination),
		).
		Complete(r)
}

func (r *WorkReconciler) requestReconciliationOfWorksOnDestination(ctx context.Context, obj client.Object) []reconcile.Request {
	dest := obj.(*v1alpha1.Destination)

	if dest.GetDeletionTimestamp() != nil {
		return nil
	}

	allWorks := &v1alpha1.WorkList{}
	err := r.Client.List(ctx, allWorks)
	if err != nil {
		r.Log.Error(err, "Error listing all Works")
		return nil
	}

	var requests []reconcile.Request
	for _, work := range allWorks.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&work)})
	}
	return requests
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

// buildWorkPlacementAnnotations merges the Work's Telemetry annotations with any existing WorkPlacement-specific trace metadata.
func buildWorkPlacementAnnotations(existing, work map[string]string) map[string]string {
	desired := cloneStringMap(work)
	if desired == nil {
		desired = map[string]string{}
	}

	if existing != nil {
		for _, key := range []string{
			telemetry.TraceParentAnnotation,
			telemetry.TraceStateAnnotation,
			telemetry.TraceTimestampAnnotation,
			telemetry.TraceGenerationAnnotation,
		} {
			if val := existing[key]; val != "" {
				desired[key] = val
			}
		}
	}

	if len(desired) == 0 {
		return nil
	}
	return desired
}

func workPlacementStatusUpdatePredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(event.CreateEvent) bool {
			return true
		},
		DeleteFunc: func(event.DeleteEvent) bool {
			return true
		},
		GenericFunc: func(event.GenericEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldWP, oldOK := e.ObjectOld.(*v1alpha1.WorkPlacement)
			newWP, newOK := e.ObjectNew.(*v1alpha1.WorkPlacement)
			if !oldOK || !newOK {
				return true
			}

			if oldWP.GetGeneration() != newWP.GetGeneration() {
				return true
			}

			if oldWP.GetDeletionTimestamp().IsZero() != newWP.GetDeletionTimestamp().IsZero() {
				return true
			}

			return summarizeWorkPlacementConditions(oldWP) != summarizeWorkPlacementConditions(newWP)
		},
	}
}

type workPlacementConditionSnapshot struct {
	readyStatus             metav1.ConditionStatus
	readyReason             string
	writeStatus             metav1.ConditionStatus
	writeReason             string
	writeObservedGeneration int64
	scheduleStatus          metav1.ConditionStatus
	scheduleReason          string
	deliveryURL             string
}

func summarizeWorkPlacementConditions(wp *v1alpha1.WorkPlacement) workPlacementConditionSnapshot {
	ready := apiMeta.FindStatusCondition(wp.Status.Conditions, "Ready")
	write := apiMeta.FindStatusCondition(wp.Status.Conditions, v1alpha1.WriteSucceededConditionType)
	schedule := apiMeta.FindStatusCondition(wp.Status.Conditions, v1alpha1.ScheduleSucceededConditionType)

	// A write of a new generation must wake the Work, even if the status looks the same.
	var writeObservedGeneration int64
	if write != nil {
		writeObservedGeneration = write.ObservedGeneration
	}

	return workPlacementConditionSnapshot{
		readyStatus:             conditionStatus(ready),
		readyReason:             conditionSummary(ready),
		writeStatus:             conditionStatus(write),
		writeReason:             conditionSummary(write),
		writeObservedGeneration: writeObservedGeneration,
		scheduleStatus:          conditionStatus(schedule),
		scheduleReason:          conditionSummary(schedule),
		deliveryURL:             wp.Status.DeliveryURL,
	}
}

func conditionStatus(cond *metav1.Condition) metav1.ConditionStatus {
	if cond == nil {
		return metav1.ConditionUnknown
	}
	return cond.Status
}

func conditionSummary(cond *metav1.Condition) string {
	if cond == nil {
		return ""
	}
	return cond.Reason + "|" + cond.Message
}

func addWorkSpanAttributes(traceCtx *reconcileTrace, promiseName string, work *v1alpha1.Work) {
	traceCtx.AddAttributes(
		attribute.String("kratix.promise.name", promiseName),
		attribute.String("kratix.work.name", work.GetName()),
		attribute.String("kratix.work.namespace", work.GetNamespace()),
		attribute.Bool("kratix.work.resource_request", work.IsResourceRequest()),
		attribute.String("kratix.action", traceCtx.Action()),
	)

	if work.IsResourceRequest() {
		traceCtx.AddAttributes(attribute.String("kratix.work.resource_request.name", work.Spec.ResourceName))
	}
}
