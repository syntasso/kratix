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
	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	Scheme        *runtime.Scheme
	Log           logr.Logger
	EventRecorder record.EventRecorder
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=gitstatestores,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=gitstatestores/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=gitstatestores/finalizers,verbs=update

// Reconcile reconciles a GitStateStore object.
func (r *GitStateStoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues(
		"gitStateStore", req.NamespacedName,
	)

	gitStateStore := &v1alpha1.GitStateStore{}
	logger.Info("Reconciling GitStateStore", "requestName", req.Name)

	if err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, gitStateStore); err != nil {
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

	return validateStateStoreCommon(
		o,
		gitStateStore,
		gitStateStore.Spec.SecretRef,
		"GitStateStore",
		r.updateReadyStatusAndCondition,
	)
}

func (r *GitStateStoreReconciler) updateReadyStatusAndCondition(gitStateStore *v1alpha1.GitStateStore, failureReason, failureMessage string, err error) error {
	eventType := v1.EventTypeNormal
	eventReason := "Ready"
	eventMessage := fmt.Sprintf("GitStateStore %q is ready", gitStateStore.Name)

	condition := metav1.Condition{
		Type:    StateStoreReadyConditionType,
		Reason:  StateStoreReadyConditionReason,
		Message: StateStoreReadyConditionMessage,
		Status:  metav1.ConditionTrue,
	}

	gitStateStore.Status.Status = StatusReady

	if failureReason != "" {
		gitStateStore.Status.Status = StatusNotReady

		condition.Status = metav1.ConditionFalse
		condition.Reason = failureReason
		condition.Message = fmt.Sprintf("%s: %s", failureMessage, err)

		// Update event parameters for failure
		eventType = v1.EventTypeWarning
		eventReason = "NotReady"
		eventMessage = fmt.Sprintf("GitStateStore %q is not ready: %s: %s", gitStateStore.Name, failureMessage, err)
	}

	changed := meta.SetStatusCondition(&gitStateStore.Status.Conditions, condition)
	if !changed {
		return nil
	}

	r.EventRecorder.Eventf(gitStateStore, eventType, eventReason, eventMessage)

	return r.Client.Status().Update(context.Background(), gitStateStore)
}

func (r *GitStateStoreReconciler) findStateStoresReferencingSecret() handler.MapFunc {
	return func(ctx context.Context, secret client.Object) []reconcile.Request {
		stateStoreList := &v1alpha1.GitStateStoreList{}
		if err := r.Client.List(ctx, stateStoreList, client.MatchingFields{
			secretRefFieldName: secretRefIndexKey(secret.GetName(), secret.GetNamespace()),
		}); err != nil {
			r.Log.Error(err, "error listing bucket state stores for secret")
			return nil
		}

		var requests []reconcile.Request
		for _, stateStore := range stateStoreList.Items {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: stateStore.Namespace,
					Name:      stateStore.Name,
				},
			})
		}
		return requests
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
