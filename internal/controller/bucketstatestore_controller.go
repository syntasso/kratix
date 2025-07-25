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

//nolint:dupl
package controller

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// BucketStateStoreReconciler reconciles a BucketStateStore object
type BucketStateStoreReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Log           logr.Logger
	EventRecorder record.EventRecorder
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=bucketstatestores,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=bucketstatestores/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=bucketstatestores/finalizers,verbs=update

// Reconcile reconciles a BucketStateStore object.
func (r *BucketStateStoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues(
		"bucketStateStore", req.NamespacedName,
	)

	bucketStateStore := &v1alpha1.BucketStateStore{}
	logger.Info("Reconciling BucketStateStore", "requestName", req.Name)
	if err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, bucketStateStore); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	o := opts{
		client: r.Client,
		ctx:    ctx,
		logger: logger,
	}

	return reconcileStateStoreCommon(
		o,
		bucketStateStore,
		"BucketStateStore",
		r.EventRecorder,
	)
}

func (r *BucketStateStoreReconciler) findStateStoresReferencingSecret() handler.MapFunc {
	return func(ctx context.Context, secret client.Object) []reconcile.Request {
		stateStoreList := &v1alpha1.BucketStateStoreList{}
		return constructRequestsForStateStoresReferencingSecret(ctx, r.Client, r.Log, secret, stateStoreList)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *BucketStateStoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create an index on the secret reference if auth method is access key
	ctx := context.Background()
	err := mgr.GetFieldIndexer().IndexField(ctx, &v1alpha1.BucketStateStore{}, secretRefFieldName,
		func(rawObj client.Object) []string {
			stateStore := rawObj.(*v1alpha1.BucketStateStore)
			if stateStore.Spec.AuthMethod == v1alpha1.AuthMethodAccessKey {
				if validateErr := stateStore.ValidateSecretRef(); validateErr != nil {
					return nil
				}
				return []string{secretRefIndexKey(stateStore.Spec.SecretRef.Name, stateStore.Spec.SecretRef.Namespace)}
			}
			return nil
		},
	)
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.BucketStateStore{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findStateStoresReferencingSecret()),
		).
		Complete(r)
}
