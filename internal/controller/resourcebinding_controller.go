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

	"k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/resourceutil"
)

// ResourceBindingReconciler reconciles a ResourceBinding object
type ResourceBindingReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=platform.kratix.io,resources=resourcebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.kratix.io,resources=resourcebindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.kratix.io,resources=resourcebindings/finalizers,verbs=update

func (r *ResourceBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues(
		"controller", "resourceBinding",
		"name", req.Name,
		"namespace", req.Namespace,
	)

	resourceBinding := &v1alpha1.ResourceBinding{}

	if err := r.Client.Get(ctx, req.NamespacedName, resourceBinding); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logging.Warn(logger, "failed to get resourceBinding; requeueing")
		return defaultRequeue, nil
	}

	logger = withPromiseAndResourceRequest(
		logger,
		resourceBinding.Spec.PromiseRef.Name,
		resourceBinding.Spec.ResourceRef.Namespace,
		resourceBinding.Spec.ResourceRef.Name,
	)

	promiseNamespacedName := types.NamespacedName{
		Name: resourceBinding.Spec.PromiseRef.Name,
	}
	promise := &v1alpha1.Promise{}

	if err := r.Client.Get(ctx, promiseNamespacedName, promise); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get promise %s", promiseNamespacedName.Name)
		}
		logging.Warn(logger, "failed to get promise; requeueing")
		return defaultRequeue, nil
	}

	rr := &unstructured.Unstructured{}
	_, gvk, err := generateCRDAndGVK(promise, logger)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get gvk for promise %s", promiseNamespacedName.Name)
	}
	rr.SetGroupVersionKind(*gvk)

	rrNamespacedName := types.NamespacedName{
		Name:      resourceBinding.Spec.ResourceRef.Name,
		Namespace: resourceBinding.Spec.ResourceRef.Namespace,
	}

	if err := r.Client.Get(ctx, rrNamespacedName, rr); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get resource request %s", rrNamespacedName.Name)
		}
		logging.Warn(logger, "failed to get resource request; requeueing")
		return defaultRequeue, nil
	}

	rrPromiseVersion := resourceutil.GetStatus(rr, resourcePromiseVersionStatus)
	// Unversioned promises have no upgrade lifecycle; manual reconciliation via the
	// binding label is also unsupported until a promise version is recorded on the resource.
	if rrPromiseVersion == "" || rrPromiseVersion == UnversionedPromiseVersion {
		logging.Info(logger, "promise has no version; skipping version check")
		return ctrl.Result{}, nil
	}

	desiredVersion := resourceBinding.Spec.Version
	if desiredVersion == LatestVersion {
		provisionRevisionMarkedWithLatest, err := latestRevision(ctx, r.Client, promise)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get latest provision revision for promise %s: %w", promiseNamespacedName.Name, err)
		}
		desiredVersion = provisionRevisionMarkedWithLatest.Spec.Version
	}

	manualReconciliationRequested := resourceBinding.GetLabels()[resourceutil.ManualReconciliationLabel] == "true"

	var needsStatusUpdate bool
	if manualReconciliationRequested {
		logging.Info(logger, "manual reconciliation label is set, forcing resource reconciliation")
		needsStatusUpdate = true
	} else {
		if rrPromiseVersion == desiredVersion {
			logging.Debug(logger, "resource request version is equal to the resource binding desired version", "resource binding version", resourceBinding.Spec.Version)
			return ctrl.Result{}, nil
		}

		logging.Info(
			logger,
			"resource request version mismatch to the resource binding desired version, triggering manual reconciliation",
			"resource request version", rrPromiseVersion,
			"resource binding version", resourceBinding.Spec.Version,
		)

		if lastUpgradeAttemptFailedForVersion(resourceBinding, desiredVersion) {
			return ctrl.Result{}, nil
		}

		needsStatusUpdate = !bindingAlreadyAdvertisesUpgradeInProgress(resourceBinding)
	}

	if resourceBinding.InFlightVersion() != desiredVersion {
		labels := rr.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[resourceutil.ManualReconciliationLabel] = "true"
		rr.SetLabels(labels)
		if err := r.Client.Update(ctx, rr); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		logging.Debug(
			logger,
			"in-flight workflow already targets desired version; skipping applying manual reconciliation label to resource request",
			"version", desiredVersion,
		)
	}

	if manualReconciliationRequested {
		delete(resourceBinding.Labels, resourceutil.ManualReconciliationLabel)
		if err := r.Client.Update(ctx, resourceBinding); err != nil {
			return ctrl.Result{}, err
		}

		//refetch so that we can update the status successfully afterwards without hitting a resource version conflict
		if err := r.Client.Get(ctx, req.NamespacedName, resourceBinding); err != nil {
			return ctrl.Result{}, err
		}
	}

	if needsStatusUpdate {
		statusMessage := fmt.Sprintf("Upgrade to version %s is in progress", desiredVersion)
		if manualReconciliationRequested {
			statusMessage = fmt.Sprintf("Reconciliation requested for version %s", desiredVersion)
		}
		apiMeta.SetStatusCondition(&resourceBinding.Status.Conditions, metav1.Condition{
			Type:               v1alpha1.UpgradeSucceededCondition,
			Status:             metav1.ConditionUnknown,
			Reason:             v1alpha1.UpgradeInProgressReason,
			Message:            statusMessage,
			LastTransitionTime: metav1.Now(),
		})
		resourceBinding.Status.FailedVersion = ""
		if err := r.Client.Status().Update(ctx, resourceBinding); err != nil {
			return ctrl.Result{}, err
		}

	}
	return ctrl.Result{}, nil
}

// lastUpgradeAttemptFailedForVersion reports whether the binding's last
// recorded upgrade attempt failed against this exact version, so we should
// stop hammering it until something changes.
func lastUpgradeAttemptFailedForVersion(binding *v1alpha1.ResourceBinding, version string) bool {
	cond := apiMeta.FindStatusCondition(binding.Status.Conditions, v1alpha1.UpgradeSucceededCondition)
	if cond == nil {
		return false
	}
	return cond.Status == metav1.ConditionFalse && binding.Status.FailedVersion == version
}

// bindingAlreadyAdvertisesUpgradeInProgress reports whether the binding's
// status already reflects an upgrade-in-progress with a clean slate (no stale
// FailedVersion left over from a previous attempt).
func bindingAlreadyAdvertisesUpgradeInProgress(binding *v1alpha1.ResourceBinding) bool {
	if binding.Status.FailedVersion != "" {
		return false
	}
	cond := apiMeta.FindStatusCondition(binding.Status.Conditions, v1alpha1.UpgradeSucceededCondition)
	if cond == nil {
		return false
	}
	return cond.Status == metav1.ConditionUnknown && cond.Reason == v1alpha1.UpgradeInProgressReason
}

// SetupWithManager sets up the controller with the Manager.
func (r *ResourceBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ResourceBinding{}).
		Named("resourcebinding").
		Complete(r)
}
