/*
Copyright 2025 Syntasso.

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
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/resourceutil"
	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// PromiseRevisionReconciler reconciles a PromiseRevision object
type PromiseRevisionReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Log           logr.Logger
	EventRecorder events.EventRecorder
}

// +kubebuilder:rbac:groups=platform.kratix.io,resources=promiserevisions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.kratix.io,resources=promiserevisions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.kratix.io,resources=promiserevisions/finalizers,verbs=update

func (r *PromiseRevisionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	logger := r.Log.WithValues(
		"controller", "promiseRevision",
		"name", req.Name,
	)

	logging.Info(logger, "reconciliation started")
	defer logReconcileDuration(logger, time.Now(), result, retErr)()

	// get current revision
	revision := &v1alpha1.PromiseRevision{}
	if err := r.Get(ctx, req.NamespacedName, revision); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logging.Warn(logger, "failed to get PromiseRevision; requeueing")
		return defaultRequeue, nil
	}

	if !revision.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, logger, revision)
	}

	if !revision.SkipResourceRequestCleanupOnDelete() && resourceutil.FinalizersAreMissing(revision, []string{resourceRequestCleanupFinalizer}) {
		controllerutil.AddFinalizer(revision, resourceRequestCleanupFinalizer)
		if err := r.Update(ctx, revision); err != nil {
			if apierrors.IsConflict(err) {
				return fastRequeue, nil
			}
			return ctrl.Result{}, err
		}
	}

	var promiseName string
	var ok bool
	if promiseName, ok = revision.GetLabels()[v1alpha1.PromiseNameLabel]; !ok {
		logging.Debug(logger, "promise name label not found, adding it", "promiseName", promiseName)
		if revision.GetLabels() == nil {
			revision.SetLabels(make(map[string]string))
		}
		revision.Labels[v1alpha1.PromiseNameLabel] = revision.GetPromiseName()
		return ctrl.Result{}, r.Update(ctx, revision)
	}

	/* Trace instrumentation */
	baseLogger := logger.WithValues(
		"generation", revision.GetGeneration(),
	)
	spanName := fmt.Sprintf("%s/PromiseRevisionReconcile", revision.GetName())
	ctx, logger, traceCtx := setupReconcileTrace(ctx, "promise-revision-controller", spanName, revision, baseLogger)
	defer finishReconcileTrace(traceCtx, &retErr)()

	addPromiseRevisionAttributes(traceCtx, revision)

	if err := persistReconcileTrace(traceCtx, r.Client, logger); err != nil {
		logging.Error(logger, err, "failed to persist trace annotations")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.syncLatestStatus(ctx, logger, revision)
}

func (r *PromiseRevisionReconciler) syncLatestStatus(ctx context.Context, logger logr.Logger, revision *v1alpha1.PromiseRevision) error {
	isLatest := revision.HasLatestRevisionLabel()
	if revision.Status.Latest == isLatest {
		return nil
	}

	revision.Status.Latest = isLatest
	if isLatest {
		if err := r.UnsetPreviousLatestRevision(ctx, logger, revision.GetPromiseName(), revision); err != nil {
			logging.Error(logger, err, "failed to unset previous latest revision")
			return err
		}
	}
	return r.Status().Update(ctx, revision)
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromiseRevisionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PromiseRevision{}).
		Named("promiserevision").
		Complete(r)
}

func addPromiseRevisionAttributes(traceCtx *reconcileTrace, revision *v1alpha1.PromiseRevision) {
	traceCtx.AddAttributes(
		attribute.String("kratix.promiserevision.name", revision.GetName()),
		attribute.String("kratix.promise.version", revision.Spec.Version),
		attribute.String("kratix.promise.name", revision.GetLabels()[v1alpha1.PromiseNameLabel]),
		attribute.Bool("kratix.promiserevision.latest", revision.Status.Latest),
	)
}

func (r *PromiseRevisionReconciler) UnsetPreviousLatestRevision(
	ctx context.Context, logger logr.Logger, promiseName string, revision *v1alpha1.PromiseRevision) error {
	logging.Debug(logger, "unsetting previous latest revision", "promiseName", promiseName)
	revisionList := &v1alpha1.PromiseRevisionList{}
	if err := r.List(ctx, revisionList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			v1alpha1.PromiseNameLabel: promiseName,
		}),
	}); err != nil {
		logging.Error(logger, err, "failed to list promise revisions")
		return err
	}
	logging.Debug(logger, "found previous latest revisions", "count", len(revisionList.Items))
	for i := range revisionList.Items {
		previousRevision := &revisionList.Items[i]
		if previousRevision.Name == revision.Name {
			continue
		}
		previousRevision.ClearLatestRevisionLabel()
		logging.Debug(logger, "removing latest revision label from previous revision", "previousRevision", previousRevision.Name)
		if err := r.Update(ctx, previousRevision); err != nil {
			logging.Error(logger, err, "failed to update promise revision status")
			return err
		}
	}
	return nil
}

// reconcileDelete keeps the revision until no resource needs it: resources pinned to its version
// look it up to reconcile and to run their delete pipelines.
func (r *PromiseRevisionReconciler) reconcileDelete(ctx context.Context, logger logr.Logger, revision *v1alpha1.PromiseRevision) (ctrl.Result, error) {
	if revision.SkipResourceRequestCleanupOnDelete() {
		return r.removeCleanupFinalizer(ctx, revision)
	}

	if err := r.syncLatestStatus(ctx, logger, revision); err != nil {
		return ctrl.Result{}, err
	}

	if revision.HasLatestRevisionLabel() {
		promiseInstalled, err := r.promiseInstalled(ctx, revision)
		if err != nil {
			return ctrl.Result{}, err
		}
		if promiseInstalled {
			logging.Info(logger, "revision is the latest for its Promise; waiting for a newer version before removing it")
			return slowRequeue, nil
		}
	}

	bindings, err := v1alpha1.ResourceBindingsUsingVersion(ctx, r.Client, revision.GetPromiseName(), revision.Spec.Version)
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(bindings) > 0 {
		logging.Info(logger, "resources still use this revision's version; waiting for them to move or be deleted",
			"version", revision.Spec.Version, "resourceBindings", len(bindings))
		return slowRequeue, nil
	}

	if err := r.deletePipelineResources(ctx, *revision); err != nil {
		return ctrl.Result{}, err
	}
	return r.removeCleanupFinalizer(ctx, revision)
}

func (r *PromiseRevisionReconciler) removeCleanupFinalizer(ctx context.Context, revision *v1alpha1.PromiseRevision) (ctrl.Result, error) {
	controllerutil.RemoveFinalizer(revision, resourceRequestCleanupFinalizer)
	if err := r.Update(ctx, revision); err != nil {
		if apierrors.IsConflict(err) {
			return fastRequeue, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *PromiseRevisionReconciler) promiseInstalled(ctx context.Context, revision *v1alpha1.PromiseRevision) (bool, error) {
	promise := &v1alpha1.Promise{}
	if err := r.Get(ctx, types.NamespacedName{Name: revision.GetPromiseName()}, promise); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return promise.DeletionTimestamp.IsZero(), nil
}

// deletePipelineResources removes the RBAC and destination selectors ConfigMaps that resource
// pipelines of this revision's version ran with. It must only run once no requests remain on the
// version, because their delete pipelines need the RBAC.
func (r *PromiseRevisionReconciler) deletePipelineResources(ctx context.Context, revision v1alpha1.PromiseRevision) error {
	versionLabels := map[string]string{
		v1alpha1.PromiseNameLabel:    revision.Spec.PromiseRef.Name,
		v1alpha1.PromiseVersionLabel: revision.Spec.Version,
	}
	o := opts{ctx: ctx, client: r.Client, logger: r.Log}
	for _, gvk := range []schema.GroupVersionKind{
		rbacv1.SchemeGroupVersion.WithKind("ClusterRoleBinding"),
		rbacv1.SchemeGroupVersion.WithKind("ClusterRole"),
		rbacv1.SchemeGroupVersion.WithKind("RoleBinding"),
		rbacv1.SchemeGroupVersion.WithKind("Role"),
		corev1.SchemeGroupVersion.WithKind("ServiceAccount"),
		corev1.SchemeGroupVersion.WithKind("ConfigMap"),
	} {
		if _, err := deleteAllResourcesWithKindMatchingLabel(o, &gvk, versionLabels); err != nil {
			return err
		}
	}
	return nil
}
