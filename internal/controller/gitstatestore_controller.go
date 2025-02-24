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
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GitStateStoreReconciler reconciles a GitStateStore object
type GitStateStoreReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Log           logr.Logger
	EventRecorder record.EventRecorder
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=gitstatestores,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=gitstatestores/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=gitstatestores/finalizers,verbs=update

// Reconcile reconciles a GitStateStore object.
func (r *GitStateStoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// logger := r.Log.WithValues(
	// 	"gitstatestore", req.NamespacedName,
	// )

	// gitstatestore := &v1alpha1.GitStateStore{}
	// logger.Info("Reconciling GitStateStore", "requestName", req.Name)
	// if err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, gitstatestore); err != nil {
	// 	if errors.IsNotFound(err) {
	// 		return ctrl.Result{}, nil
	// 	}
	// 	return ctrl.Result{}, err
	// }

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GitStateStoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.GitStateStore{}).
		Complete(r)
}
