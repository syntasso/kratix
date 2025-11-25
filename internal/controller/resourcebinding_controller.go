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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
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
	)

	resourceBinding := &v1alpha1.ResourceBinding{}

	if err := r.Client.Get(ctx, req.NamespacedName, resourceBinding); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logging.Warn(logger, "failed to get resourceBinding; requeueing")
		return defaultRequeue, nil
	}

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

	rrPromiseVersion := resourceutil.GetStatus(rr, "promiseVersion")
	if rrPromiseVersion == "" || rrPromiseVersion == resourceBinding.Spec.Version {
		return ctrl.Result{}, nil
	}

	labels := rr.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels[resourceutil.ManualReconciliationLabel] = "true"
	rr.SetLabels(labels)
	if err := r.Client.Update(ctx, rr); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ResourceBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.ResourceBinding{}).
		Named("resourcebinding").
		Complete(r)
}
