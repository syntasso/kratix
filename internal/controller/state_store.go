package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type StateStore interface {
	client.Object
	GetName() string
	GetStatus() *v1alpha1.StateStoreStatus
	SetStatus(status v1alpha1.StateStoreStatus)
	GetSecretRef() *v1.SecretReference
}

func fetchSecret(ctx context.Context, logger logr.Logger, client client.Client, eventRecorder record.EventRecorder, stateStore StateStore) *v1.Secret {
	secret := &v1.Secret{}
	secretRef := stateStore.GetSecretRef()
	secretName := types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: secretRef.Namespace,
	}

	if err := client.Get(ctx, secretName, secret); err != nil {
		if kerrors.IsNotFound(err) {
			eventRecorder.Event(stateStore, v1.EventTypeWarning, "SecretNotFound",
				fmt.Sprintf("Secret %s not found in namespace %s", secretRef.Name, secretRef.Namespace))

			logging.Error(
				logger, err, "secret not found",
				"secretName", secretRef.Name,
				"secretNamespace", secretRef.Namespace,
			)
			return nil
		}
	}
	return secret
}

func secretRefIndexKey(secretName, secretNamespace string) string {
	return fmt.Sprintf("%s.%s", secretNamespace, secretName)
}

func constructRequestsForStateStoresReferencingSecret(ctx context.Context, k8sclient client.Client, logger logr.Logger, secret client.Object, stateStoreList client.ObjectList) []reconcile.Request {
	if err := k8sclient.List(ctx, stateStoreList, client.MatchingFields{
		secretRefFieldName: secretRefIndexKey(secret.GetName(), secret.GetNamespace()),
	}); err != nil {
		logging.Error(logger, err, "error listing bucket state stores for secret")
		return nil
	}

	items, err := meta.ExtractList(stateStoreList)
	if err != nil {
		logging.Error(logger, err, "error extracting list items")
		return nil
	}

	var requests []reconcile.Request
	for _, stateStore := range items {
		stateStore := stateStore.(StateStore)
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: stateStore.GetNamespace(),
				Name:      stateStore.GetName(),
			},
		})
	}
	return requests
}

type stateStoreReconcileContext struct {
	ctx        context.Context
	controller string

	logger        logr.Logger
	trace         *reconcileTrace
	client        client.Client
	eventRecorder record.EventRecorder

	stateStore       StateStore
	stateStoreSecret *v1.Secret
	repositoryCache  *RepositoryCache
}

func (reconcileCtx *stateStoreReconcileContext) Reconcile() (ctrl.Result, error) {
	repository, err := reconcileCtx.repositoryCache.InitRepository(reconcileCtx)
	if err != nil {
		logging.Error(reconcileCtx.logger, err, "unable to get repository")
		return reconcileCtx.setNotReadyStatus(err)
	}
	repository.Lock()
	defer repository.Unlock()

	if err := repository.Writer.ValidatePermissions(); err != nil {
		logging.Error(reconcileCtx.logger, err, "unable to validate permissions")
		_, err := reconcileCtx.setNotReadyStatus(NewValidatePermissionsError(err))
		if err != nil {
			return ctrl.Result{}, err
		}
		return defaultRequeue, nil
	}
	return reconcileCtx.setReadyStatus()
}

func (reconcileCtx *stateStoreReconcileContext) setNotReadyStatus(err *StateStoreError) (ctrl.Result, error) {
	return reconcileCtx.setStatus(StatusNotReady, metav1.Condition{
		Type:    StateStoreReadyConditionType,
		Reason:  err.Reason,
		Message: err.Message,
		Status:  metav1.ConditionFalse,
	}, func() { reconcileCtx.recordNotReadyEvent(err) })
}

func (reconcileCtx *stateStoreReconcileContext) setReadyStatus() (ctrl.Result, error) {
	return reconcileCtx.setStatus(StatusReady, metav1.Condition{
		Type:    StateStoreReadyConditionType,
		Reason:  StateStoreReadyConditionReason,
		Message: StateStoreReadyConditionMessage,
		Status:  metav1.ConditionTrue,
	}, reconcileCtx.recordReadyEvent)
}

func (reconcileCtx *stateStoreReconcileContext) setStatus(status string, condition metav1.Condition, recordEvent func()) (ctrl.Result, error) {
	stateStoreStatus := reconcileCtx.stateStore.GetStatus().DeepCopy()
	stateStoreStatus.Status = status

	if !meta.SetStatusCondition(&stateStoreStatus.Conditions, condition) {
		return ctrl.Result{}, nil
	}

	reconcileCtx.stateStore.SetStatus(*stateStoreStatus)
	recordEvent()
	if err := reconcileCtx.client.Status().Update(reconcileCtx.ctx, reconcileCtx.stateStore); err != nil {
		if kerrors.IsConflict(err) {
			return fastRequeue, nil
		}
		logging.Error(reconcileCtx.logger, err, "error updating state store status")
		return defaultRequeue, nil
	}
	return ctrl.Result{}, nil
}

func (reconcileCtx *stateStoreReconcileContext) recordReadyEvent() {
	eventMessage := fmt.Sprintf("%s %q is ready",
		reconcileCtx.stateStore.GetObjectKind().GroupVersionKind().Kind,
		reconcileCtx.stateStore.GetName(),
	)
	reconcileCtx.eventRecorder.Eventf(reconcileCtx.stateStore, v1.EventTypeNormal, "Ready", eventMessage)
}

func (reconcileCtx *stateStoreReconcileContext) recordNotReadyEvent(err *StateStoreError) {
	reconcileCtx.eventRecorder.Eventf(
		reconcileCtx.stateStore,
		v1.EventTypeWarning,
		"NotReady",
		err.Message,
	)
}
