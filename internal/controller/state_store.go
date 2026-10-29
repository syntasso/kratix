package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/delivery"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
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
	GetDelivery() *v1alpha1.Delivery
	GetGeneration() int64
	GetObservedGeneration() int64
	SetObservedGeneration(generation int64) bool
	Ready() bool
}

func fetchSecret(ctx context.Context, logger logr.Logger, client client.Client, stateStore StateStore) (v1.Secret, *StateStoreError) {
	secretRef := stateStore.GetSecretRef()

	if secretRef == nil {
		return v1.Secret{}, nil
	}
	secret := v1.Secret{}
	if secretRef.Namespace == "" {
		secretRef.Namespace = "default"
	}

	secretName := types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: secretRef.Namespace,
	}

	if err := client.Get(ctx, secretName, &secret); err != nil {
		if kerrors.IsNotFound(err) {
			logging.Error(
				logger, err, "secret not found",
				"secretName", secretRef.Name,
				"secretNamespace", secretRef.Namespace,
			)
			return v1.Secret{}, NewSecretNotFoundError(secretRef)
		}
		return v1.Secret{}, NewStateStoreError(err)
	}
	return secret, nil
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
	client        client.Client
	apiReader     client.Reader
	eventRecorder events.EventRecorder

	stateStore       StateStore
	stateStoreSecret v1.Secret
	repositoryCache  RepositoryCache

	deliveryRetired bool
}

func (reconcileCtx *stateStoreReconcileContext) Reconcile() (ctrl.Result, error) {
	if reconcileCtx.stateStore.GetGeneration() != reconcileCtx.stateStore.GetObservedGeneration() {
		if err := reconcileCtx.repositoryCache.Cleanup(reconcileCtx.stateStore); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := reconcileCtx.retireOldDelivery(); err != nil {
		logging.Error(reconcileCtx.logger, err, "unable to retire the old delivery mode")
		if err := reconcileCtx.setNotReadyStatus(err); err != nil {
			return ctrl.Result{}, err
		}
		return defaultRequeue, nil
	}

	repository, err := reconcileCtx.repositoryCache.InitRepository(
		reconcileCtx.logger,
		reconcileCtx.stateStore,
		reconcileCtx.stateStoreSecret,
	)
	if err != nil {
		logging.Error(reconcileCtx.logger, err, "unable to get repository")
		if err := reconcileCtx.setNotReadyStatus(err); err != nil {
			return ctrl.Result{}, err
		}
		return defaultRequeue, nil
	}
	repository.Lock()
	validateErr := repository.Writer.ValidatePermissions()
	repository.Unlock()

	if validateErr != nil {
		logging.Error(reconcileCtx.logger, validateErr, "unable to validate permissions")
		_ = reconcileCtx.repositoryCache.Cleanup(reconcileCtx.stateStore)
		if err := reconcileCtx.setNotReadyStatus(NewValidatePermissionsError(validateErr)); err != nil {
			return ctrl.Result{}, err
		}
		return defaultRequeue, nil
	}

	if err := reconcileCtx.setReadyStatus(); err != nil {
		if kerrors.IsConflict(err) {
			return fastRequeue, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// retireOldDelivery hands the deliveries still outstanding on this store to the writer of the
// delivery mode recorded in status, when spec.delivery has moved to another mode.
func (reconcileCtx *stateStoreReconcileContext) retireOldDelivery() *StateStoreError {
	saved := reconcileCtx.stateStore.GetStatus().Delivery
	if !retirePending(saved, reconcileCtx.stateStore.GetDelivery()) {
		return nil
	}

	factory, err := delivery.For(saved.Mode)
	if err != nil {
		reconcileCtx.eventRecorder.Eventf(reconcileCtx.stateStore, nil, v1.EventTypeWarning,
			StateStoreDeliveryModeNotRegisteredReason, StateStoreDeliveryModeNotRegisteredReason,
			"%s; outstanding deliveries of the old mode were left as they are", err)
		reconcileCtx.deliveryRetired = true
		return nil
	}

	creds := reconcileCtx.stateStoreSecret.Data
	base, err := newBaseWriter(reconcileCtx.logger, reconcileCtx.stateStore, creds)
	if err != nil {
		return NewRetireFailedError(saved.Mode, err)
	}
	writer, err := factory(base, saved.Config, creds)
	if err != nil {
		return NewRetireFailedError(saved.Mode, err)
	}

	if retirer, ok := writer.(delivery.Retirer); ok {
		outstanding, err := reconcileCtx.outstandingDeliveries()
		if err != nil {
			return NewRetireFailedError(saved.Mode, err)
		}
		if err := retirer.Retire(reconcileCtx.ctx, outstanding); err != nil {
			return NewRetireFailedError(saved.Mode, err)
		}
	}
	reconcileCtx.deliveryRetired = true
	return nil
}

// retirePending is true when the recorded mode is not built in and spec.delivery names another mode.
func retirePending(saved, spec *v1alpha1.Delivery) bool {
	savedMode := deliveryMode(saved)
	return savedMode != delivery.BuiltIn && savedMode != deliveryMode(spec)
}

func deliveryMode(d *v1alpha1.Delivery) string {
	if d == nil {
		return delivery.BuiltIn
	}
	return d.Mode
}

// outstandingDeliveries reads from the API server: the cache can miss a receipt written a moment ago.
func (reconcileCtx *stateStoreReconcileContext) outstandingDeliveries() ([]delivery.Outstanding, error) {
	destinations := &v1alpha1.DestinationList{}
	if err := reconcileCtx.apiReader.List(reconcileCtx.ctx, destinations); err != nil {
		return nil, err
	}

	kind := reconcileCtx.stateStore.GetObjectKind().GroupVersionKind().Kind
	onThisStore := map[string]bool{}
	for _, destination := range destinations.Items {
		ref := destination.Spec.StateStoreRef
		if ref != nil && !ref.IsManagedByCustomController() &&
			ref.Kind == kind && ref.Name == reconcileCtx.stateStore.GetName() {
			onThisStore[destination.Name] = true
		}
	}
	if len(onThisStore) == 0 {
		return nil, nil
	}

	workPlacements := &v1alpha1.WorkPlacementList{}
	if err := reconcileCtx.apiReader.List(reconcileCtx.ctx, workPlacements); err != nil {
		return nil, err
	}

	var outstanding []delivery.Outstanding
	for _, workPlacement := range workPlacements.Items {
		receipt := workPlacement.Status.DeliveryReceipt
		if receipt != "" && onThisStore[workPlacement.Spec.TargetDestinationName] {
			outstanding = append(outstanding, delivery.Outstanding{Receipt: receipt, WorkPlacement: workPlacement.Name})
		}
	}
	return outstanding, nil
}

// deliveryToRecord keeps the old block while its mode still needs retiring, so the next reconcile retries it.
func (reconcileCtx *stateStoreReconcileContext) deliveryToRecord() *v1alpha1.Delivery {
	saved := reconcileCtx.stateStore.GetStatus().Delivery
	spec := reconcileCtx.stateStore.GetDelivery()
	if !reconcileCtx.deliveryRetired && retirePending(saved, spec) {
		return saved
	}
	return spec.DeepCopy()
}

func (reconcileCtx *stateStoreReconcileContext) setNotReadyStatus(err *StateStoreError) error {
	return reconcileCtx.setStatus(StatusNotReady, metav1.Condition{
		Type:    StateStoreReadyConditionType,
		Reason:  err.Reason,
		Message: err.Message,
		Status:  metav1.ConditionFalse,
	}, func() { reconcileCtx.recordNotReadyEvent(err) })
}

func (reconcileCtx *stateStoreReconcileContext) setReadyStatus() error {
	return reconcileCtx.setStatus(StatusReady, metav1.Condition{
		Type:    StateStoreReadyConditionType,
		Reason:  StateStoreReadyConditionReason,
		Message: StateStoreReadyConditionMessage,
		Status:  metav1.ConditionTrue,
	}, reconcileCtx.recordReadyEvent)
}

func (reconcileCtx *stateStoreReconcileContext) setStatus(status string, condition metav1.Condition, recordEvent func()) error {
	genChanged := reconcileCtx.stateStore.SetObservedGeneration(reconcileCtx.stateStore.GetGeneration())
	stateStoreStatus := reconcileCtx.stateStore.GetStatus().DeepCopy()
	stateStoreStatus.Status = status
	conditionChanged := meta.SetStatusCondition(&stateStoreStatus.Conditions, condition)

	deliveryToRecord := reconcileCtx.deliveryToRecord()
	deliveryChanged := !equality.Semantic.DeepEqual(stateStoreStatus.Delivery, deliveryToRecord)
	stateStoreStatus.Delivery = deliveryToRecord

	if !conditionChanged && !genChanged && !deliveryChanged {
		return nil
	}

	reconcileCtx.stateStore.SetStatus(*stateStoreStatus)
	if conditionChanged || genChanged {
		recordEvent()
	}
	return reconcileCtx.client.Status().Update(reconcileCtx.ctx, reconcileCtx.stateStore)
}

func (reconcileCtx *stateStoreReconcileContext) recordReadyEvent() {
	eventMessage := fmt.Sprintf("%s %q is ready",
		reconcileCtx.stateStore.GetObjectKind().GroupVersionKind().Kind,
		reconcileCtx.stateStore.GetName(),
	)
	reconcileCtx.eventRecorder.Eventf(reconcileCtx.stateStore, nil, v1.EventTypeNormal, "Ready", "Ready", "%s", eventMessage)
}

func (reconcileCtx *stateStoreReconcileContext) recordNotReadyEvent(err *StateStoreError) {
	reconcileCtx.eventRecorder.Eventf(reconcileCtx.stateStore, nil, v1.EventTypeWarning, "NotReady", "NotReady", "%s", err.Message)
}

type StateStoreError struct {
	error
	Reason  string
	Message string
}

func (e *StateStoreError) Error() string {
	return e.error.Error()
}

func NewInitialiseWriterError(err error) *StateStoreError {
	return &StateStoreError{
		error:   err,
		Reason:  StateStoreNotReadyErrorInitialisingWriterReason,
		Message: err.Error(),
	}
}

func NewValidatePermissionsError(err error) *StateStoreError {
	return &StateStoreError{
		error:   err,
		Reason:  StateStoreNotReadyErrorValidatingPermissionsReason,
		Message: fmt.Sprintf("%s: %s", StateStoreNotReadyErrorValidatingPermissionsMessage, err.Error()),
	}
}

func NewRetireFailedError(mode string, err error) *StateStoreError {
	return &StateStoreError{
		error:   err,
		Reason:  StateStoreNotReadyRetireFailedReason,
		Message: fmt.Sprintf("could not retire delivery mode %q: %s", mode, err.Error()),
	}
}

func NewSecretNotFoundError(secretRef *v1.SecretReference) *StateStoreError {
	message := fmt.Sprintf("Secret %s not found in namespace %s", secretRef.Name, secretRef.Namespace)
	return &StateStoreError{
		error:   errors.New(message),
		Reason:  "SecretNotFound",
		Message: message,
	}
}

func NewStateStoreError(err error) *StateStoreError {
	return &StateStoreError{
		error:   err,
		Reason:  "StateStoreError",
		Message: err.Error(),
	}
}
