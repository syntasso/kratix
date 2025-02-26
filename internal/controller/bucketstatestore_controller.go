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
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	secretRef                                        = "secretRef"
	StatusNotReady                                   = "NotReady"
	StatusReady                                      = "Ready"
	StateStoreReadyConditionType                     = "Ready"
	StateStoreReadyConditionReason                   = "StateStoreReady"
	StateStoreReadyConditionMessage                  = "State store is ready"
	StateStoreNotReadySecretNotFoundReason           = "SecretNotFound"
	StateStoreNotReadySecretNotFoundMessage          = "Secret not found"
	StateStoreNotReadyErrorInitialisingWriterReason  = "ErrorInitialisingWriter"
	StateStoreNotReadyErrorInitialisingWriterMessage = "Error initialising writer"
	StateStoreNotReadyErrorWritingTestFileReason     = "ErrorWritingTestFile"
	StateStoreNotReadyErrorWritingTestFileMessage    = "Error writing test file"
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
		"bucketstatestore", req.NamespacedName,
	)

	bucketStateStore := &v1alpha1.BucketStateStore{}
	logger.Info("Reconciling BucketStateStore", "requestName", req.Name)
	if err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, bucketStateStore); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	secret := &corev1.Secret{}
	objectKey := types.NamespacedName{
		Name:      bucketStateStore.Spec.SecretRef.Name,
		Namespace: bucketStateStore.Spec.SecretRef.Namespace,
	}
	if err := r.Client.Get(ctx, objectKey, secret); err != nil {
		if err := r.updateReadyStatusAndCondition(bucketStateStore, StateStoreNotReadySecretNotFoundReason, StateStoreNotReadySecretNotFoundMessage, err); err != nil {
			logger.Error(err, "error updating state store status")
		}

		return ctrl.Result{}, err
	}

	opts := opts{
		client: r.Client,
		ctx:    ctx,
		logger: logger,
	}

	writer, err := newWriter(opts, bucketStateStore.GetName(), "BucketStateStore", "")
	if err != nil {
		if err := r.updateReadyStatusAndCondition(bucketStateStore, StateStoreNotReadyErrorInitialisingWriterReason, StateStoreNotReadyErrorInitialisingWriterMessage, err); err != nil {
			logger.Error(err, "error updating state store status")
		}

		return ctrl.Result{}, err
	}

	if err := r.writeTestFile(writer); err != nil {
		if err := r.updateReadyStatusAndCondition(bucketStateStore, StateStoreNotReadyErrorWritingTestFileReason, StateStoreNotReadyErrorWritingTestFileMessage, err); err != nil {
			logger.Error(err, "error updating state store status")
		}

		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.updateReadyStatusAndCondition(bucketStateStore, "", "", nil)
}

func (r *BucketStateStoreReconciler) writeTestFile(writer writers.StateStoreWriter) error {
	content := fmt.Sprintf("This file tests that Kratix can write to this state store. Last write time: %s", time.Now().String())
	_, err := writer.UpdateFiles("", "kratix-write-probe", []v1alpha1.Workload{
		{
			Filepath: "kratix-write-probe.txt",
			Content:  content,
		},
	}, nil)

	return err
}

func (r *BucketStateStoreReconciler) updateReadyStatusAndCondition(bucketStateStore *v1alpha1.BucketStateStore, failureReason, failureMessage string, err error) error {
	eventType := v1.EventTypeNormal
	eventReason := "Ready"
	eventMessage := fmt.Sprintf("BucketStateStore %q is ready", bucketStateStore.Name)

	condition := metav1.Condition{
		Type:    StateStoreReadyConditionType,
		Reason:  StateStoreReadyConditionReason,
		Message: StateStoreReadyConditionMessage,
		Status:  metav1.ConditionTrue,
	}

	bucketStateStore.Status.Status = StatusReady

	if failureReason != "" {
		bucketStateStore.Status.Status = StatusNotReady

		condition.Status = metav1.ConditionFalse
		condition.Reason = failureReason
		condition.Message = fmt.Sprintf("%s: %s", failureMessage, err)

		// Update event parameters for failure
		eventType = v1.EventTypeWarning
		eventReason = "NotReady"
		eventMessage = fmt.Sprintf("BucketStateStore %q is not ready: %s: %s", bucketStateStore.Name, failureMessage, err)
	}

	changed := meta.SetStatusCondition(&bucketStateStore.Status.Conditions, condition)
	if !changed {
		return nil
	}

	r.EventRecorder.Eventf(bucketStateStore, eventType, eventReason, eventMessage)

	return r.Client.Status().Update(context.Background(), bucketStateStore)
}

func (r *BucketStateStoreReconciler) findStateStoresReferencingSecret() handler.MapFunc {
	return func(ctx context.Context, secret client.Object) []reconcile.Request {
		stateStoreList := &v1alpha1.BucketStateStoreList{}
		if err := r.Client.List(ctx, stateStoreList, client.MatchingFields{
			secretRef: r.secretRefKey(secret.GetName(), secret.GetNamespace()),
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

func (r *BucketStateStoreReconciler) secretRefKey(secretName, secretNamespace string) string {
	return fmt.Sprintf("%s.%s", secretNamespace, secretName)
}

// SetupWithManager sets up the controller with the Manager.
func (r *BucketStateStoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create an index on the secret reference
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.BucketStateStore{}, secretRef,
		func(rawObj client.Object) []string {
			stateStore := rawObj.(*v1alpha1.BucketStateStore)
			return []string{r.secretRefKey(stateStore.Spec.SecretRef.Name, stateStore.Spec.SecretRef.Namespace)}
		},
	)
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.BucketStateStore{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findStateStoresReferencingSecret()),
		).
		Complete(r)
}
