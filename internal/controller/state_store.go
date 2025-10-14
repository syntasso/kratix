package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

func fetchObjectAndSecret(o opts, stateStoreRef client.ObjectKey, stateStore StateStore) (*v1.Secret, error) {
	if err := o.client.Get(o.ctx, stateStoreRef, stateStore); err != nil {
		logging.Error(o.logger, err, "unable to fetch resource", "resourceKind", stateStore.GetObjectKind(), "stateStoreRef", stateStoreRef)
		return nil, err
	}

	if stateStore.GetSecretRef() == nil {
		return nil, nil
	}

	namespace := stateStore.GetSecretRef().Namespace
	if namespace == "" {
		namespace = "default"
	}

	secret := &v1.Secret{}
	secretRef := types.NamespacedName{
		Name:      stateStore.GetSecretRef().Name,
		Namespace: namespace,
	}

	if err := o.client.Get(o.ctx, secretRef, secret); err != nil {
		logging.Error(o.logger, err, "unable to fetch resource", "resourceKind", stateStore.GetObjectKind(), "secretRef", secretRef)
		if errors.IsNotFound(err) {
			err = fmt.Errorf("secret %q not found in namespace %q", secretRef.Name, namespace)
		}
		return nil, err
	}

	return secret, nil
}

func secretRefIndexKey(secretName, secretNamespace string) string {
	return fmt.Sprintf("%s.%s", secretNamespace, secretName)
}

// reconcileStateStoreCommon contains the common logic for state store reconciliation.
func reconcileStateStoreCommon(
	o opts,
	stateStore StateStore,
	resourceType string,
	eventRecorder record.EventRecorder,
) (ctrl.Result, error) {
	writer, err := newWriter(o, stateStore.GetName(), resourceType, "")
	if err != nil {
		logging.Error(o.logger, err, "unable to create writer")
		if statusError := updateStateStoreReadyStatusAndCondition(o, eventRecorder, stateStore, StateStoreNotReadyErrorInitialisingWriterReason, StateStoreNotReadyErrorInitialisingWriterMessage, err); statusError != nil {
			logging.Error(o.logger, statusError, "error updating state store status")
		}
		return ctrl.Result{}, err
	}

	if err = writer.ValidatePermissions(); err != nil {
		logging.Warn(o.logger, "error validating state store permissions", "error", err)
		if err = updateStateStoreReadyStatusAndCondition(o, eventRecorder, stateStore, StateStoreNotReadyErrorValidatingPermissionsReason, StateStoreNotReadyErrorValidatingPermissionsMessage, err); err != nil {
			logging.Error(o.logger, err, "error updating state store status")
		}
		return defaultRequeue, nil
	}

	return ctrl.Result{}, updateStateStoreReadyStatusAndCondition(o, eventRecorder, stateStore, "", "", nil)
}

func updateStateStoreReadyStatusAndCondition(o opts, eventRecorder record.EventRecorder, stateStore StateStore, failureReason, failureMessage string, err error) error {
	stateStoreKind := stateStore.GetObjectKind().GroupVersionKind().Kind

	eventType := v1.EventTypeNormal
	eventReason := "Ready"
	eventMessage := fmt.Sprintf("%s %q is ready", stateStoreKind, stateStore.GetName())

	condition := metav1.Condition{
		Type:    StateStoreReadyConditionType,
		Reason:  StateStoreReadyConditionReason,
		Message: StateStoreReadyConditionMessage,
		Status:  metav1.ConditionTrue,
	}

	originalStatus := stateStore.GetStatus()
	stateStoreStatus := originalStatus.DeepCopy()
	stateStoreStatus.Status = StatusReady

	if failureReason != "" {
		stateStoreStatus.Status = StatusNotReady

		condition.Status = metav1.ConditionFalse
		condition.Reason = failureReason
		condition.Message = fmt.Sprintf("%s: %s", failureMessage, err)

		// Update event parameters for failure
		eventType = v1.EventTypeWarning
		eventReason = "NotReady"
		eventMessage = fmt.Sprintf("%s %q is not ready: %s: %s", stateStoreKind, stateStore.GetName(), failureMessage, err)
	}

	changed := meta.SetStatusCondition(&stateStoreStatus.Conditions, condition)
	if !changed {
		return nil
	}

	eventRecorder.Eventf(stateStore, eventType, eventReason, eventMessage)

	stateStore.SetStatus(*stateStoreStatus)

	return o.client.Status().Update(context.Background(), stateStore)
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
