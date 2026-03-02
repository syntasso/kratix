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
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/writers"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
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
	Scheme          *runtime.Scheme
	Log             logr.Logger
	EventRecorder   record.EventRecorder
	RepositoryCache *RepositoryCache
}

type Repository struct {
	sync.Mutex
	Path   string
	Branch string

	Writer writers.StateStoreWriter
}

type RepositoryCache struct {
	sync.Mutex
	cache map[string]*Repository
}

type stateStoreReconcileContext struct {
	ctx        context.Context
	controller string

	logger        logr.Logger
	trace         *reconcileTrace
	client        client.Client
	eventRecorder record.EventRecorder

	stateStore       *v1alpha1.GitStateStore
	stateStoreSecret *v1.Secret
	repositoryCache  *RepositoryCache
}

type ReconcileContext interface {
	Logger() logr.Logger
	Client() client.Client
}

func NewRepositoryCache() *RepositoryCache {
	return &RepositoryCache{
		cache: map[string]*Repository{},
	}
}

func (c *RepositoryCache) GetRepository(ctx *stateStoreReconcileContext) (*Repository, *StateStoreError) {
	c.Lock()
	defer c.Unlock()

	if repository, ok := c.cache[ctx.stateStore.GetName()]; ok {
		return repository, nil
	}

	gitWriter, err := writers.NewGitWriter(
		ctx.logger.WithName("writers").WithName("GitStateStoreWriter"),
		ctx.stateStore.Spec,
		"",
		ctx.stateStoreSecret.Data,
	)
	if err != nil {
		return nil, NewInitialiseWriterError(fmt.Errorf("unable to create git writer: %w", err))
	}

	repoDir, err := gitWriter.Init(ctx.stateStore.Spec.Branch)
	if err != nil {
		return nil, NewInitialiseWriterError(fmt.Errorf("unable to clone repository: %w", err))
	}

	repo := &Repository{
		Path:   repoDir,
		Branch: ctx.stateStore.Spec.Branch,
		Writer: gitWriter,
	}

	c.cache[ctx.stateStore.GetName()] = repo

	return repo, nil
}

func NewGitStateStoreController(client client.Client, scheme *runtime.Scheme, eventRecorder record.EventRecorder, cache *RepositoryCache) *GitStateStoreReconciler {
	return &GitStateStoreReconciler{
		Client:          client,
		Scheme:          scheme,
		Log:             ctrl.Log.WithName("controllers").WithName("GitStateStore"),
		EventRecorder:   eventRecorder,
		RepositoryCache: cache,
	}
}

func (r *GitStateStoreReconciler) newReconcileContext(ctx context.Context, logger logr.Logger, req ctrl.Request, cache *RepositoryCache) (*stateStoreReconcileContext, error) {
	gitStateStore := &v1alpha1.GitStateStore{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, gitStateStore); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, NewInitialiseWriterError(err)
	}

	secret := &v1.Secret{}
	secretRef := gitStateStore.GetSecretRef()
	secretName := types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: secretRef.Namespace,
	}

	if err := r.Client.Get(ctx, secretName, secret); err != nil {
		if kerrors.IsNotFound(err) {
			r.EventRecorder.Event(gitStateStore, v1.EventTypeWarning, "SecretNotFound",
				fmt.Sprintf("Secret %s not found in namespace %s", secretRef.Name, secretRef.Namespace))

			return nil, nil
		}

		return nil, NewInitialiseWriterError(fmt.Errorf("unable to fetch secret: %w", err))
	}

	return &stateStoreReconcileContext{
		ctx:              ctx,
		controller:       "GitStateStore",
		logger:           logger.WithValues("generation", gitStateStore.GetGeneration()),
		client:           r.Client,
		stateStore:       gitStateStore,
		stateStoreSecret: secret,
		repositoryCache:  cache,
		eventRecorder:    r.EventRecorder,
	}, nil
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
		stateStoreCtx, err := r.newReconcileContext(ctx, logger, req, r.RepositoryCache)
		if err != nil {
			logging.Error(logger, err, "unable to setup resources for reconciliation")
			return ctrl.Result{}, err
		}
		if stateStoreCtx == nil {
			return ctrl.Result{}, nil
		}

		return stateStoreCtx.Reconcile()
	})
}

func (reconcileCtx *stateStoreReconcileContext) Reconcile() (ctrl.Result, error) {
	repository, err := reconcileCtx.repositoryCache.GetRepository(reconcileCtx)
	if err != nil {
		logging.Error(reconcileCtx.logger, err, "unable to get repository")
		return reconcileCtx.setNotReadyStatus(err)
	}
	repository.Lock()
	defer repository.Unlock()

	if err := repository.Writer.ValidatePermissions(); err != nil {
		logging.Error(reconcileCtx.logger, err, "unable to validate permissions")
		return reconcileCtx.setNotReadyStatus(NewValidatePermissionsError(err))
	}

	return reconcileCtx.setReadyStatus()
}

func withTrace(logger logr.Logger, reconcileFunc func() (ctrl.Result, error)) (ctrl.Result, error) {
	logging.Info(logger, "reconciliation started")

	result, err := reconcileFunc()
	defer logReconcileDuration(logger, time.Now(), result, err)()

	return result, err
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

func (reconcileCtx *stateStoreReconcileContext) setNotReadyStatus(err *StateStoreError) (ctrl.Result, error) {
	return reconcileCtx.setStatus(StatusNotReady, metav1.Condition{
		Type:    StateStoreReadyConditionType,
		Reason:  err.Reason,
		Message: fmt.Sprintf("%s: %s", err.Message, err.Error()),
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
	eventMessage := fmt.Sprintf("%s %q is not ready: %s: %s",
		reconcileCtx.stateStore.GetObjectKind().GroupVersionKind().Kind,
		reconcileCtx.stateStore.GetName(), err.Message,
		err.Error(),
	)
	reconcileCtx.eventRecorder.Eventf(reconcileCtx.stateStore, v1.EventTypeWarning, "NotReady", eventMessage)
}

type StateStoreError struct {
	error
	Reason  string
	Message string
}

func (e *StateStoreError) Error() string {
	return fmt.Sprintf("%s: %s", e.Message, e.error.Error())
}

func NewInitialiseWriterError(err error) *StateStoreError {
	return &StateStoreError{
		error:   err,
		Reason:  StateStoreNotReadyErrorInitialisingWriterReason,
		Message: StateStoreNotReadyErrorInitialisingWriterMessage,
	}
}

func NewValidatePermissionsError(err error) *StateStoreError {
	return &StateStoreError{
		error:   err,
		Reason:  StateStoreNotReadyErrorValidatingPermissionsReason,
		Message: StateStoreNotReadyErrorValidatingPermissionsMessage,
	}
}
