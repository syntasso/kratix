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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
)

// ResourceMetadataReconciler reconciles a ResourceMetadata object
type ResourceMetadataReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.kratix.io,resources=resourcemetadata,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.kratix.io,resources=resourcemetadata/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.kratix.io,resources=resourcemetadata/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ResourceMetadataReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = logf.FromContext(ctx)

	resourceMetadata := &platformv1alpha1.ResourceMetadata{}
	if err := r.Client.Get(ctx, req.NamespacedName, resourceMetadata); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	resourceRequest := &unstructured.Unstructured{}
	resourceRequest.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   resourceMetadata.Spec.GVK.Group,
		Version: resourceMetadata.Spec.GVK.Version,
		Kind:    resourceMetadata.Spec.GVK.Kind,
	})
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      resourceMetadata.Spec.ResourceRef.Name,
		Namespace: resourceMetadata.Spec.ResourceRef.Namespace,
	}, resourceRequest); err != nil {
		return ctrl.Result{}, err
	}

	resourceRevision := resourceutil.GetStatus(resourceRequest, "revision")
	if resourceRevision == resourceMetadata.Spec.Version || resourceRevision == "" {
		return ctrl.Result{}, nil
	}

	resourceRequest.SetLabels(
		labels.Merge(
			resourceRequest.GetLabels(), map[string]string{
				resourceutil.ManualReconciliationLabel: "true",
			},
		),
	)
	if err := r.Client.Update(ctx, resourceRequest); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ResourceMetadataReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.ResourceMetadata{}).
		Named("resourcemetadata").
		Complete(r)
}
