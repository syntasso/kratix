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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/syntasso/kratix/api/v1alpha1"
)

const (
	kratixConfigMapName      = "kratix"
	kratixConfigMapNamespace = v1alpha1.SystemNamespace
)

// KratixConfigReconciler watches the Kratix configuration ConfigMap and calls
// OnConfigChange whenever it is created or updated.
type KratixConfigReconciler struct {
	Client         client.Client
	Log            logr.Logger
	OnConfigChange func(data map[string]string) error
}

//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *KratixConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("configmap", req.NamespacedName)

	cm := &corev1.ConfigMap{}
	if err := r.Client.Get(ctx, req.NamespacedName, cm); err != nil {
		if k8serrors.IsNotFound(err) {
			logger.Info("kratix config ConfigMap not found; retaining current workflow defaults")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger.Info("kratix config ConfigMap changed; applying new configuration")
	return ctrl.Result{}, r.OnConfigChange(cm.Data)
}

// SetupWithManager sets up the controller with the Manager.
func (r *KratixConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	isKratixConfigMap := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetName() == kratixConfigMapName && obj.GetNamespace() == kratixConfigMapNamespace
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("kratix-config").
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, _ client.Object) []reconcile.Request {
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Name:      kratixConfigMapName,
						Namespace: kratixConfigMapNamespace,
					},
				}}
			}),
			builder.WithPredicates(isKratixConfigMap),
		).
		Complete(r)
}
