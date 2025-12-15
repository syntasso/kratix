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
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/resourceutil"
	"go.opentelemetry.io/otel/attribute"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// PromiseRevisionReconciler reconciles a PromiseRevision object
type PromiseRevisionReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Log            logr.Logger
	EventRecorder  record.EventRecorder
	PromiseUpgrade bool
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
	revision := &platformv1alpha1.PromiseRevision{}
	if err := r.Get(ctx, req.NamespacedName, revision); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logging.Warn(logger, "failed to get PromiseRevision; requeueing")
		return defaultRequeue, nil
	}

	if !revision.DeletionTimestamp.IsZero() {
		return r.deleteResourceRequests(ctx, *revision)
	}

	if resourceutil.FinalizersAreMissing(revision, []string{resourceRequestCleanupFinalizer}) {
		controllerutil.AddFinalizer(revision, resourceRequestCleanupFinalizer)
		if err := r.Client.Update(ctx, revision); err != nil {
			if kerrors.IsConflict(err) {
				return fastRequeue, nil
			}
			return ctrl.Result{}, err
		}
	}

	var promiseName string
	var ok bool
	if promiseName, ok = revision.GetLabels()[platformv1alpha1.PromiseNameLabel]; !ok {
		logging.Debug(logger, "promise name label not found, adding it", "promiseName", promiseName)
		if revision.GetLabels() == nil {
			revision.SetLabels(make(map[string]string))
		}
		revision.Labels[platformv1alpha1.PromiseNameLabel] = revision.GetPromiseName()
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

	isLatest := revision.GetLabels()["kratix.io/latest-revision"] == "true"
	if !isLatest {
		if revision.Status.Latest {
			revision.Status.Latest = false
			return ctrl.Result{}, r.Status().Update(ctx, revision)
		}
		// If we already updated the Status, there's nothing else to do here.
		return ctrl.Result{}, nil
	}

	if revision.Status.Latest {
		return ctrl.Result{}, nil
	}

	revision.Status.Latest = true
	if err := r.UnsetPreviousLatestRevision(ctx, logger, promiseName, revision); err != nil {
		logging.Error(logger, err, "failed to unset previous latest revision")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.Status().Update(ctx, revision)
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromiseRevisionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Index the version field for Resource Bindings to facilitate lookups
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.ResourceBinding{}, "spec.version",
		func(rawObj client.Object) []string {
			rb := rawObj.(*v1alpha1.ResourceBinding)
			if rb.Spec.Version == "" {
				return nil
			}
			return []string{rb.Spec.Version}
		},
	)
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.PromiseRevision{}).
		Named("promiserevision").
		Complete(r)
}

func addPromiseRevisionAttributes(traceCtx *reconcileTrace, revision *platformv1alpha1.PromiseRevision) {
	traceCtx.AddAttributes(
		attribute.String("kratix.promiserevision.name", revision.GetName()),
		attribute.String("kratix.promise.version", revision.Spec.Version),
		attribute.String("kratix.promise.name", revision.GetLabels()[platformv1alpha1.PromiseNameLabel]),
		attribute.Bool("kratix.promiserevision.latest", revision.Status.Latest),
	)
}

func (r *PromiseRevisionReconciler) UnsetPreviousLatestRevision(
	ctx context.Context, logger logr.Logger, promiseName string, revision *platformv1alpha1.PromiseRevision) error {
	logging.Debug(logger, "unsetting previous latest revision", "promiseName", promiseName)
	revisionList := &platformv1alpha1.PromiseRevisionList{}
	if err := r.List(ctx, revisionList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{
			platformv1alpha1.PromiseNameLabel: promiseName,
		}),
	}); err != nil {
		logging.Error(logger, err, "failed to list promise revisions")
		return err
	}
	logging.Debug(logger, "found previous latest revisions", "count", len(revisionList.Items))
	for _, previousRevision := range revisionList.Items {
		if previousRevision.Name != revision.Name {
			l := previousRevision.GetLabels()
			delete(l, "kratix.io/latest-revision")
			previousRevision.SetLabels(l)
			logging.Debug(logger, "removing latest revision label from previous revision", "previousRevision", previousRevision.Name)
			if err := r.Update(ctx, &previousRevision); err != nil {
				logging.Error(logger, err, "failed to update promise revision status")
				return err
			}
		}
	}
	return nil
}

func (r *PromiseRevisionReconciler) deleteResourceRequests(ctx context.Context, revision v1alpha1.PromiseRevision) (ctrl.Result, error) {
	promise := &v1alpha1.Promise{}
	if err := r.Client.Get(
		ctx,
		types.NamespacedName{
			Name: revision.Spec.PromiseRef.Name,
		},
		promise,
	); err != nil {
		return ctrl.Result{}, err
	}
	_, gvk, err := generateCRDAndGVK(promise, r.Log)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get gvk for promise %s", promise.Name)
	}

	bindingsForPromiseRevision, err := r.getResourceBindings(ctx, revision)
	if err != nil {
		return ctrl.Result{}, err
	}

	requeue, err := r.ensureResourceRequestsAreDeleted(ctx, bindingsForPromiseRevision, gvk)
	if err != nil {
		return ctrl.Result{}, err
	}

	if requeue {
		return fastRequeue, nil
	}

	if len(bindingsForPromiseRevision) == 0 {
		controllerutil.RemoveFinalizer(&revision, resourceRequestCleanupFinalizer)

		if err := r.Client.Update(ctx, &revision); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *PromiseRevisionReconciler) ensureResourceRequestsAreDeleted(ctx context.Context, bindingsForPromise []v1alpha1.ResourceBinding, gvk *schema.GroupVersionKind) (requeue bool, err error) {
	var fastRequeue bool
	for _, binding := range bindingsForPromise {
		rr := &unstructured.Unstructured{}
		rr.SetGroupVersionKind(*gvk)

		if err := r.Client.Get(ctx,
			types.NamespacedName{
				Name:      binding.Spec.ResourceRef.Name,
				Namespace: binding.Spec.ResourceRef.Namespace,
			},
			rr,
		); err != nil {
			r.Log.Info("error getting resource request", "error", err.Error())
			if apierrors.IsNotFound(err) {
				continue
			}

			return fastRequeue, err
		}

		if err := r.Client.Delete(ctx, rr); err != nil {
			r.Log.Info("error deleting resource request", "error", err.Error())

			return fastRequeue, err
		}
		fastRequeue = true
	}

	if fastRequeue {
		return fastRequeue, nil
	}

	return false, nil
}

func (r *PromiseRevisionReconciler) getResourceBindings(ctx context.Context, promiseRevision v1alpha1.PromiseRevision) ([]v1alpha1.ResourceBinding, error) {
	bindingsForPromise := &v1alpha1.ResourceBindingList{}

	err := r.Client.List(ctx, bindingsForPromise,
		client.MatchingLabels{v1alpha1.PromiseNameLabel: promiseRevision.Spec.PromiseRef.Name},
		client.MatchingFields{"spec.version": promiseRevision.Spec.Version},
	)

	if err != nil {
		return bindingsForPromise.Items, err
	}

	return bindingsForPromise.Items, nil
}
