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

	"go.opentelemetry.io/otel/attribute"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
)

// PromiseRevisionReconciler reconciles a PromiseRevision object
type PromiseRevisionReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Log           logr.Logger
	EventRecorder record.EventRecorder
}

// +kubebuilder:rbac:groups=platform.kratix.io,resources=promiserevisions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.kratix.io,resources=promiserevisions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.kratix.io,resources=promiserevisions/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the PromiseRevision object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
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
		} else {
			// If we already updated the Status, there's nothing else to do here.
			return ctrl.Result{}, nil
		}
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
