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
	"github.com/syntasso/kratix/internal/logging"
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

// GitStateStoreReconciler reconciles a GitStateStore object
type GitStateStoreReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Log             logr.Logger
	EventRecorder   record.EventRecorder
	RepositoryCache RepositoryCache
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=gitstatestores,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=gitstatestores/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=gitstatestores/finalizers,verbs=update

// Reconcile reconciles a GitStateStore object.
func (r *GitStateStoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	logger := r.Log.WithValues(
		"controller", "gitStateStore",
		"name", req.Name,
	)

	return withTrace(logger, func() (ctrl.Result, error) {
		stateStoreCtx, err := r.newReconcileContext(ctx, logger, req)
		if err != nil {
			logging.Error(logger, err, "unable to setup resources for reconciliation")
			return ctrl.Result{}, err
		}
		if stateStoreCtx == nil {
			return ctrl.Result{}, nil
		}

		result, err := stateStoreCtx.Reconcile()
		if err != nil {
			return ctrl.Result{}, err
		}
		return result, nil
	})
}

func (r *GitStateStoreReconciler) newReconcileContext(ctx context.Context, logger logr.Logger, req ctrl.Request) (*stateStoreReconcileContext, error) {
	gitStateStore := &v1alpha1.GitStateStore{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, gitStateStore); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, NewInitialiseWriterError(err)
	}

	stateStoreCtx := &stateStoreReconcileContext{
		ctx:             ctx,
		controller:      "GitStateStore",
		logger:          logger.WithValues("generation", gitStateStore.GetGeneration()),
		client:          r.Client,
		stateStore:      gitStateStore,
		repositoryCache: r.RepositoryCache,
		eventRecorder:   r.EventRecorder,
	}

	secret, err := fetchSecret(ctx, logger, r.Client, gitStateStore)
	if err != nil {
		r.RepositoryCache.Cleanup(gitStateStore)
		return nil, stateStoreCtx.setNotReadyStatus(err)
	}

	stateStoreCtx.stateStoreSecret = secret

	return stateStoreCtx, nil
}

func (r *GitStateStoreReconciler) findStateStoresReferencingSecret() handler.MapFunc {
	return func(ctx context.Context, secret client.Object) []reconcile.Request {
		stateStoreList := &v1alpha1.GitStateStoreList{}
		return constructRequestsForStateStoresReferencingSecret(ctx, r.Client, r.Log, secret, stateStoreList)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *GitStateStoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create an index on the secret reference
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.GitStateStore{}, secretRefFieldName,
		func(rawObj client.Object) []string {
			stateStore := rawObj.(*v1alpha1.GitStateStore)
			return []string{secretRefIndexKey(stateStore.Spec.SecretRef.Name, stateStore.Spec.SecretRef.Namespace)}
		},
	)
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.GitStateStore{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findStateStoresReferencingSecret()),
		).
		Complete(r)
}
