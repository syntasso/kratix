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
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cluster-api/util/annotations"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
)

const (
	statusInstalled       = "Installed"
	statusErrorInstalling = "Error installing"

	conditionMessageInstalled = "Installed successfully"
	conditionReasonInstalled  = "InstalledSuccessfully"
)

// PromiseReleaseReconciler reconciles a PromiseRelease object
type PromiseReleaseReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Log            logr.Logger
	PromiseFetcher v1alpha1.PromiseFetcher
}

const promiseCleanupFinalizer = kratixPrefix + "promise-cleanup"

//+kubebuilder:rbac:groups=platform.kratix.io,resources=promisereleases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=promisereleases/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=promisereleases/finalizers,verbs=update

func (r *PromiseReleaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	promiseRelease := &v1alpha1.PromiseRelease{}
	err := r.Get(ctx, req.NamespacedName, promiseRelease)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		r.Log.Error(err, "Failed getting PromiseRelease", "namespacedName", req.NamespacedName)
		return defaultRequeue, nil
	}

	logger := r.Log.WithValues("identifier", promiseRelease.GetName())
	logger.Info("Reconciling PromiseRelease")

	opts := opts{
		client: r.Client,
		ctx:    ctx,
		logger: logger,
	}

	if !promiseRelease.DeletionTimestamp.IsZero() {
		return r.delete(opts, promiseRelease)
	}

	if resourceutil.DoesNotContainFinalizer(promiseRelease, promiseCleanupFinalizer) {
		return addFinalizers(opts, promiseRelease, []string{promiseCleanupFinalizer})
	}

	exists, err := r.promiseExistsAtDesiredVersion(opts, promiseRelease)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check if promise exists: %w", err)
	}

	if exists {
		logger.Info("Promise exists, skipping install")
		r.updateStatus(opts, promiseRelease, statusInstalled, conditionMessageInstalled, conditionReasonInstalled)
		return ctrl.Result{}, nil
	}

	logger.Info("Promise does not exist, installing")

	var promise *v1alpha1.Promise

	switch sourceRefType := promiseRelease.Spec.SourceRef.Type; sourceRefType {
	case v1alpha1.TypeHTTP:
		promise, err = r.PromiseFetcher.FromURL(promiseRelease.Spec.SourceRef.URL)
		if err != nil {
			r.updateStatus(opts, promiseRelease, statusErrorInstalling, "Failed to fetch Promise from URL", "FailedToFetchPromise")
			return ctrl.Result{}, fmt.Errorf("failed to fetch promise from url: %w", err)
		}
		updated, err := r.validateVersion(opts, promiseRelease, promise)
		if err != nil || updated {
			return ctrl.Result{}, err
		}
	default:
		logger.Error(fmt.Errorf("unknown sourceRef type: %s", sourceRefType), "not requeueing")
		return ctrl.Result{}, nil
	}

	if err := r.installPromise(opts, promiseRelease, promise); err != nil {
		r.updateStatus(opts, promiseRelease, statusErrorInstalling, "Failed to create or update Promise", "FailedToCreateOrUpdatePromise")
		return ctrl.Result{}, fmt.Errorf("failed to create or update promise: %w", err)
	}

	promiseRelease.Status.Status = statusInstalled
	r.updateStatus(opts, promiseRelease, statusInstalled, conditionMessageInstalled, conditionReasonInstalled)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromiseReleaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PromiseRelease{}).
		Owns(&v1alpha1.Promise{}).
		Complete(r)
}

func (r *PromiseReleaseReconciler) installPromise(o opts, promiseRelease *v1alpha1.PromiseRelease, promise *v1alpha1.Promise) error {
	existingPromise := v1alpha1.Promise{
		ObjectMeta: v1.ObjectMeta{
			Name: promise.GetName(),
		},
	}

	// this will trigger the Promise Controller Reconciliation loop
	op, err := controllerutil.CreateOrUpdate(o.ctx, o.client, &existingPromise, func() error {
		// If promise already exists the existingPromise object has all the fields set.
		// Otherwise, it's an empty struct. Either way, we want to override the spec.
		existingPromise.Spec = promise.Spec

		// Copy labels and annotations from the PromiseRelease's Promise over to the
		// existing Promise, prioritising the PromiseRelease Promise's labels and
		// annotations.
		existingPromise.SetLabels(labels.Merge(existingPromise.Labels, promise.Labels))
		existingPromise.Labels[promiseReleaseNameLabel] = promiseRelease.GetName()

		annotations.AddAnnotations(&existingPromise.ObjectMeta, promise.Annotations)

		return ctrl.SetControllerReference(promiseRelease, &existingPromise, r.Scheme)
	})

	if err != nil {
		return nil
	}

	o.logger.Info("Promise reconciled during PromiseRelease reconciliation",
		"operation", op,
		"promiseName", promise.GetName(),
		"promiseReleaseName", promiseRelease.GetName(),
	)

	return nil
}

func (r *PromiseReleaseReconciler) delete(o opts, promiseRelease *v1alpha1.PromiseRelease) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(promiseRelease, promiseCleanupFinalizer) {
		return ctrl.Result{}, nil
	}

	promises := &v1alpha1.PromiseList{}
	err := o.client.List(o.ctx, promises, client.MatchingLabels{
		promiseReleaseNameLabel: promiseRelease.GetName(),
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list promises: %w", err)
	}

	if len(promises.Items) == 0 {
		controllerutil.RemoveFinalizer(promiseRelease, promiseCleanupFinalizer)
		err = o.client.Update(o.ctx, promiseRelease)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	for _, promise := range promises.Items {
		r.Log.Info("Deleting Promise", "promiseName", promise.GetName())
		if promise.GetDeletionTimestamp().IsZero() {
			err = o.client.Delete(o.ctx, &promise)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to delete Promise: %w", err)
			}
		}
	}

	return defaultRequeue, nil
}

func (r *PromiseReleaseReconciler) promiseExistsAtDesiredVersion(o opts, promiseRelease *v1alpha1.PromiseRelease) (bool, error) {
	promises := &v1alpha1.PromiseList{}
	err := o.client.List(o.ctx, promises, client.MatchingLabels{
		promiseReleaseNameLabel: promiseRelease.GetName(),
	})

	if err != nil {
		return false, fmt.Errorf("failed to list promises: %w", err)
	}

	switch len(promises.Items) {
	case 1:
		if promises.Items[0].Labels[promiseVersionLabel] == promiseRelease.Spec.Version {
			return true, nil
		}
		fallthrough
	case 0:
		return false, nil
	default:
		return false, fmt.Errorf("expected 0 or 1 promises, got %d", len(promises.Items))
	}
}

func (r *PromiseReleaseReconciler) updateStatus(o opts, pr *v1alpha1.PromiseRelease,
	message string, conditionMessage, conditionReason string) {
	pr.Status.Status = message
	existingConditionIndex := -1
	for i, condition := range pr.Status.Conditions {
		if condition.Type == "Installed" {
			existingConditionIndex = i
			if condition.Message == conditionMessage &&
				condition.Reason == conditionReason {
				//don't update the status if its already correct
				return
			}
		}
	}

	conditionStatus := v1.ConditionFalse
	if conditionMessage == conditionMessageInstalled {
		conditionStatus = v1.ConditionTrue
	}

	condition := v1.Condition{
		Type:    "Installed",
		Message: conditionMessage,
		Reason:  conditionReason,
		Status:  conditionStatus,
	}

	if existingConditionIndex == -1 {
		pr.Status.Conditions = append(pr.Status.Conditions, condition)
	} else {
		pr.Status.Conditions[existingConditionIndex] = condition
	}

	err := o.client.Status().Update(o.ctx, pr)
	if err != nil {
		o.logger.Error(err, "Failed to update PromiseRelease status", "promiseReleaseName", pr.GetName(), "message", message)
	}
}

func (r *PromiseReleaseReconciler) validateVersion(o opts, promiseRelease *v1alpha1.PromiseRelease, promise *v1alpha1.Promise) (updated bool, err error) {
	promiseVersion, found := promise.GetLabels()[promiseVersionLabel]
	if !found {
		r.updateStatus(o, promiseRelease, statusErrorInstalling, "Version label not found on Promise", "VersionLabelNotFound")
		return false, fmt.Errorf("version label (%s) not found on promise; refusing to install", promiseVersionLabel)
	}

	if promiseRelease.Spec.Version == "" {
		promiseRelease.Spec.Version = promiseVersion
		err := o.client.Update(o.ctx, promiseRelease)
		if err != nil {
			return false, fmt.Errorf("failed to set promise release version: %w", err)
		}
		return true, nil
	}

	if promiseVersion != promiseRelease.Spec.Version {
		msg := fmt.Sprintf("Version labels do not match, found: %s, expected: %s", promiseVersion, promiseRelease.Spec.Version)
		r.updateStatus(o, promiseRelease, statusErrorInstalling, msg, "VersionNotMatching")
		return false, fmt.Errorf(
			"version label on promise (%s) does not match version on promise release (%s); refusing to install",
			promiseVersion,
			promiseRelease.Spec.Version,
		)
	}

	return false, nil
}
