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
	"strings"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/telemetry"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"go.opentelemetry.io/otel/attribute"
)

const workCleanUpFinalizer = v1alpha1.KratixPrefix + "work-cleanup"

// WorkReconciler reconciles a Work object.
type WorkReconciler struct {
	Client        client.Client
	Log           logr.Logger
	Scheduler     WorkScheduler
	EventRecorder record.EventRecorder
}

//counterfeiter:generate . WorkScheduler
type WorkScheduler interface {
	ReconcileWork(work *v1alpha1.Work) ([]string, error)
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=works,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=works/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=works/finalizers,verbs=update

func (r *WorkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	work := &v1alpha1.Work{}
	err := r.Client.Get(ctx, req.NamespacedName, work)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		r.Log.WithValues("work", req.NamespacedName).Error(err, "Error getting Work")
		return ctrl.Result{}, err
	}

	promiseName := work.Spec.PromiseName
	baseLogger := r.Log.WithValues("work", req.NamespacedName, "promise", promiseName)
	spanName := fmt.Sprintf("%s/WorkReconcile", promiseName)
	resourceName := work.Spec.ResourceName
	if resourceName != "" {
		spanName = fmt.Sprintf("%s/%s", resourceName, spanName)
	}
	ctx, logger, traceCtx := setupReconcileTrace(ctx, "work-controller", spanName, work, baseLogger)
	defer finishReconcileTrace(traceCtx, &retErr)()

	traceCtx.AddAttributes(
		attribute.String("kratix.promise.name", promiseName),
		attribute.String("kratix.work.name", work.GetName()),
		attribute.String("kratix.work.namespace", work.GetNamespace()),
		attribute.Bool("kratix.work.resource_request", work.IsResourceRequest()),
	)

	if err := persistReconcileTrace(traceCtx, r.Client, logger); err != nil {
		logger.Error(err, "failed to persist trace annotations")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling Work")

	if !work.DeletionTimestamp.IsZero() {
		return r.deleteWork(ctx, work)
	}

	if !controllerutil.ContainsFinalizer(work, workCleanUpFinalizer) {
		return addFinalizers(opts{
			client: r.Client,
			logger: logger,
			ctx:    ctx}, work, []string{workFinalizer})
	}

	originalAnnotations := cloneStringMap(work.GetAnnotations())
	if traceCtx.HasTrace() {
		enriched := cloneStringMap(work.GetAnnotations())
		enriched = traceCtx.InjectTrace(enriched)
		work.SetAnnotations(enriched)
		defer work.SetAnnotations(originalAnnotations)
	}

	logger.Info("Requesting scheduling for Work")

	unscheduledWorkloadGroupIDs, err := r.Scheduler.ReconcileWork(work)
	if err != nil {
		if errors.IsConflict(err) {
			logger.Info("failed to schedule Work due to update conflict, requeue...")
			return fastRequeue, nil
		}
		logger.Error(err, "error scheduling Work, will retry...")
		return ctrl.Result{}, err
	}

	if work.IsResourceRequest() && len(unscheduledWorkloadGroupIDs) > 0 {
		logger.Info("no available Destinations for some of the workload groups, trying again shortly", "workloadGroupIDs", unscheduledWorkloadGroupIDs)
		r.EventRecorder.Eventf(
			work,
			v1.EventTypeNormal,
			"WaitingDestination",
			"waiting for destination for workload group: [%s]",
			strings.Join(unscheduledWorkloadGroupIDs, ","),
		)
		return slowRequeue, nil
	}

	if err := r.updateWorkStatus(ctx, logger, work); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *WorkReconciler) updateWorkStatus(ctx context.Context, logger logr.Logger, work *v1alpha1.Work) error {
	workplacements, err := listWorkplacementWithLabels(r.Client, logger, work.GetNamespace(), map[string]string{
		workLabelKey: work.Name,
	})
	if err != nil {
		logger.Info("failed to list associated WorkPlacements")
		return err
	}

	var failedWorkPlacements []string
	for _, wp := range workplacements {
		if apiMeta.IsStatusConditionFalse(wp.Status.Conditions, writeSucceededConditionType) {
			failedWorkPlacements = append(failedWorkPlacements, wp.GetName())
		}
	}

	if len(failedWorkPlacements) > 0 {
		readyCond := metav1.Condition{
			Type:    "Ready",
			Status:  metav1.ConditionFalse,
			Message: "Failing",
			Reason:  "WorkplacementsFailing",
		}
		scheduleCond := metav1.Condition{
			Type:   scheduleSucceededConditionType,
			Status: metav1.ConditionFalse,
			Message: fmt.Sprintf(
				"Workplacements failed to write: [%s]",
				strings.Join(failedWorkPlacements, ","),
			),
			Reason: "WorkplacementsFailing",
		}
		if apiMeta.SetStatusCondition(&work.Status.Conditions, scheduleCond) {
			apiMeta.SetStatusCondition(&work.Status.Conditions, readyCond)
			r.EventRecorder.Eventf(work, v1.EventTypeWarning, "WorkplacementsFailing", "Workplacements failed to write: [%s]", strings.Join(failedWorkPlacements, ","))
			return r.Client.Status().Update(ctx, work)
		}
	}

	return nil
}

func (r *WorkReconciler) deleteWork(ctx context.Context, work *v1alpha1.Work) (ctrl.Result, error) {
	workplacementGVK := schema.GroupVersionKind{
		Group:   v1alpha1.GroupVersion.Group,
		Version: v1alpha1.GroupVersion.Version,
		Kind:    "WorkPlacement",
	}

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(opts{client: r.Client, logger: r.Log, ctx: ctx},
		&workplacementGVK, map[string]string{workLabelKey: work.Name})
	if err != nil {
		r.EventRecorder.Eventf(work, v1.EventTypeWarning, "FailedDelete",
			"deleting work failed: %s", err.Error())
		return defaultRequeue, err
	}

	if !resourcesRemaining {
		controllerutil.RemoveFinalizer(work, workCleanUpFinalizer)
		err = r.Client.Update(ctx, work)
		if err != nil {
			return defaultRequeue, err
		}
	}
	return defaultRequeue, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Work{}).
		Owns(&v1alpha1.WorkPlacement{}).
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
