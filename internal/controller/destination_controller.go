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
	"errors"
	"fmt"
	"path/filepath"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"sigs.k8s.io/yaml"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	canaryWorkload              = "kratix-canary"
	canaryNamespacePath         = "kratix-canary-namespace.yaml"
	canaryConfigMapPath         = "kratix-canary-configmap.yaml"
	destinationCleanupFinalizer = v1alpha1.KratixPrefix + "destination-cleanup"

	stateStoreReference = "stateStoreRef"
)

// DestinationReconciler reconciles a Destination object
type DestinationReconciler struct {
	Client          client.Client
	Log             logr.Logger
	Scheduler       *Scheduler
	EventRecorder   record.EventRecorder
	RepositoryCache RepositoryCache
}

type destinationReconcileContext struct {
	ctx        context.Context
	controller string

	logger        logr.Logger
	client        client.Client
	eventRecorder record.EventRecorder

	destination     *v1alpha1.Destination
	repositoryCache RepositoryCache
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=destinations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=bucketstatestores;gitstatestores,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=destinations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=destinations/finalizers,verbs=update

func (r *DestinationReconciler) newReconcileContext(ctx context.Context, logger logr.Logger, req ctrl.Request) (*destinationReconcileContext, error) {
	destination := &v1alpha1.Destination{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, destination); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &destinationReconcileContext{
		ctx:             ctx,
		controller:      "destination-controller",
		logger:          logger,
		client:          r.Client,
		eventRecorder:   r.EventRecorder,
		destination:     destination,
		repositoryCache: r.RepositoryCache,
	}, nil
}

func (r *DestinationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	logger := r.Log.WithValues(
		"controller", "destination",
		"name", req.Name,
	)
	return withTrace(logger, func() (ctrl.Result, error) {
		logging.Info(logger, "reconcile func started")
		destinationCtx, err := r.newReconcileContext(ctx, logger, req)
		if err != nil {
			logging.Error(logger, err, "unable to setup resources for reconciliation")
			return ctrl.Result{}, err
		}
		if destinationCtx == nil {
			return ctrl.Result{}, nil
		}

		logger = logger.WithValues("generation", destinationCtx.destination.Generation)

		return destinationCtx.Reconcile()
	})
}

func (d *destinationReconcileContext) Reconcile() (ctrl.Result, error) {
	logging.Info(d.logger, "ctx reconcile started")

	if d.needsFinalizerUpdate() {
		logging.Debug(d.logger, "updating destination finalizers")
		if err := d.client.Update(d.ctx, d.destination); err != nil {
			logging.Error(d.logger, err, "error adding finalizers to destination")
			return ctrl.Result{}, d.setNotReadyStatus("DestinationFinalizerUpdateFailed", err.Error())
		}
		return ctrl.Result{}, nil
	}

	if apiMeta.FindStatusCondition(d.destination.Status.Conditions, "Ready") == nil {
		return ctrl.Result{}, d.setNotReadyStatus(v1alpha1.DestinationNotReadyReason, "Destination is not ready")
	}

	repo, err := d.repositoryCache.GetRepositoryByTypeAndName(
		d.destination.Spec.StateStoreRef.Kind,
		d.destination.Spec.StateStoreRef.Name,
	)
	if err != nil {
		if errors.Is(err, ErrCacheMiss) {
			d.logAndRecordEvent(err, "State Store not ready")
			if err := d.setNotReadyStatus("StateStoreNotReady", "State Store not ready"); err != nil {
				return ctrl.Result{}, err
			}
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}

	repo.Lock()
	defer repo.Unlock()

	if err := repo.Writer.Reset(); err != nil {
		return defaultRequeue, nil
	}

	if !d.destination.DeletionTimestamp.IsZero() {
		return d.handleDeletion(repo)
	}

	if !d.destination.Spec.InitWorkloads.Enabled {
		if err := d.deleteCanaryFiles(repo); err != nil {
			return ctrl.Result{}, err
		}

		d.logAndRecordEvent(nil, "Destination is Ready")
		return ctrl.Result{}, d.setReadyStatus(v1alpha1.DestinationReadyReason, "Destination is ready")
	}

	d.logger = d.logger.WithValues("path", d.destination.Spec.Path)

	var writeErr error
	if writeErr = d.writeTestFiles(repo); writeErr != nil {
		d.logAndRecordEvent(writeErr, "Failed to write test documents to State Store")
	}

	if writeErr != nil {
		if err := d.setNotReadyStatus("StateStoreWriteFailed", fmt.Sprintf("Failed to write test documents to State Store: %s", writeErr.Error())); err != nil {
			return ctrl.Result{}, err
		}
		return defaultRequeue, nil
	}
	d.logAndRecordEvent(nil, "Destination is Ready")
	return ctrl.Result{}, d.setReadyStatus("TestDocumentsWritten", "Test documents written to State Store")
}

func (d *destinationReconcileContext) setReadyStatus(reason, message string) error {
	if changed := d.destination.SetReadyStatus(reason, message); changed {
		if err := d.client.Status().Update(d.ctx, d.destination); err != nil {
			return err
		}
	}
	return nil
}

func (d *destinationReconcileContext) setNotReadyStatus(reason, message string) error {
	if changed := d.destination.SetNotReadyStatus(reason, message); changed {
		if err := d.client.Status().Update(d.ctx, d.destination); err != nil {
			return err
		}
	}
	return nil
}

func (d *destinationReconcileContext) needsFinalizerUpdate() bool {
	hasFinalizer := controllerutil.ContainsFinalizer(d.destination, destinationCleanupFinalizer)
	switch d.destination.GetCleanup() {
	case v1alpha1.DestinationCleanupAll:
		if !hasFinalizer {
			controllerutil.AddFinalizer(d.destination, destinationCleanupFinalizer)
			return true
		}
	case v1alpha1.DestinationCleanupNone:
		if hasFinalizer {
			controllerutil.RemoveFinalizer(d.destination, destinationCleanupFinalizer)
			return true
		}
	}
	return false
}

func (d *destinationReconcileContext) writeTestFiles(repo *Repository) error {
	workloads := d.getCanaryWorkloads()
	_, err := repo.Writer.UpdateFiles("", canaryWorkload, workloads, nil)
	if err != nil {
		return err
	}
	return nil
}

func (r *DestinationReconciler) findDestinationsForStateStore(stateStoreType string) handler.MapFunc {
	return func(ctx context.Context, stateStore client.Object) []reconcile.Request {
		destinationList := &v1alpha1.DestinationList{}
		if err := r.Client.List(ctx, destinationList, client.MatchingFields{
			stateStoreReference: r.stateStoreRefKey(stateStoreType, stateStore.GetName()),
		}); err != nil {
			logging.Error(r.Log, err, "error listing destinations for state store")
			return nil
		}

		var requests []reconcile.Request
		for _, destination := range destinationList.Items {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: destination.Namespace,
					Name:      destination.Name,
				},
			})
		}
		return requests
	}
}

func (r *DestinationReconciler) stateStoreRefKey(stateStoreKind, stateStoreName string) string {
	return fmt.Sprintf("%s.%s", stateStoreKind, stateStoreName)
}

// SetupWithManager sets up the controller with the Manager.
func (r *DestinationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create an index on the state store reference
	err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.Destination{}, stateStoreReference,
		func(rawObj client.Object) []string {
			destination := rawObj.(*v1alpha1.Destination)
			return []string{r.stateStoreRefKey(destination.Spec.StateStoreRef.Kind, destination.Spec.StateStoreRef.Name)}
		},
	)
	if err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Destination{}).
		Watches(
			&v1alpha1.BucketStateStore{},
			handler.EnqueueRequestsFromMapFunc(r.findDestinationsForStateStore("BucketStateStore")),
		).
		Watches(
			&v1alpha1.GitStateStore{},
			handler.EnqueueRequestsFromMapFunc(r.findDestinationsForStateStore("GitStateStore")),
		).
		Complete(r)
}

func (d *destinationReconcileContext) handleDeletion(repo *Repository) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(d.destination, destinationCleanupFinalizer) {
		if success, err := d.deleteDestinationWorkplacements(); !success || err != nil {
			logging.Error(d.logger, err, "error deleting destination workplacements")
			return defaultRequeue, nil //nolint:nilerr // requeue rather than exponential backoff
		}

		if err := d.deleteCanaryFiles(repo); err != nil {
			logging.Error(d.logger, err, "error deleting state store contents")
			return defaultRequeue, nil //nolint:nilerr // requeue rather than exponential backoff
		}

		controllerutil.RemoveFinalizer(d.destination, destinationCleanupFinalizer)
		if err := d.client.Update(d.ctx, d.destination); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (d *destinationReconcileContext) deleteDestinationWorkplacements() (bool, error) {
	logging.Debug(d.logger, "deleting destination workplacements")

	workPlacementGVK := schema.GroupVersionKind{
		Group:   v1alpha1.GroupVersion.Group,
		Version: v1alpha1.GroupVersion.Version,
		Kind:    "WorkPlacement",
	}

	labels := map[string]string{
		TargetDestinationNameLabel: d.destination.Name,
	}

	opts := opts{ctx: d.ctx, client: d.client, logger: d.logger}

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(opts, &workPlacementGVK, labels)
	if err != nil {
		logging.Error(d.logger, err, "error deleting workplacements")
		return false, err
	}

	if resourcesRemaining {
		logging.Warn(d.logger, "couldn't remove workplacements, will try again")
		return false, nil
	}
	return true, nil
}

func (d *destinationReconcileContext) deleteCanaryFiles(repo *Repository) error {
	logging.Debug(d.logger, "removing dependencies dir from repository")
	workloadsToDelete := d.getCanaryWorkloads()

	filePaths := []string{}
	for _, w := range workloadsToDelete {
		filePaths = append(filePaths, w.Filepath)
	}

	return repo.Writer.DeleteFiles(canaryWorkload, filePaths)
}

func (d *destinationReconcileContext) getCanaryWorkloads() []v1alpha1.Workload {
	filePathMode := d.destination.GetFilepathMode()

	kratixNamespace := &v1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "kratix-worker-system"},
	}
	nsBytes, _ := yaml.Marshal(kratixNamespace)

	configMap := &v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "kratix-info", Namespace: "kratix-worker-system"},
		Data: map[string]string{
			"canary": "this confirms your infrastructure is reading from Kratix state stores",
		},
	}
	configMapBytes, _ := yaml.Marshal(configMap)

	configMapFilePath := canaryConfigMapPath
	namespaceFilePath := canaryNamespacePath
	if filePathMode == v1alpha1.FilepathModeNestedByMetadata {
		configMapFilePath = filepath.Join(resourcesDir, configMapFilePath)
		namespaceFilePath = filepath.Join(dependenciesDir, namespaceFilePath)
	}

	return []v1alpha1.Workload{{
		Filepath: filepath.Join(d.destination.Spec.Path, namespaceFilePath),
		Content:  string(nsBytes),
	}, {
		Filepath: filepath.Join(d.destination.Spec.Path, configMapFilePath),
		Content:  string(configMapBytes),
	}}

}

func (d *destinationReconcileContext) logAndRecordEvent(err error, message string) {
	if err != nil {
		logging.Error(d.logger, err, message)
		d.eventRecorder.Eventf(
			d.destination,
			v1.EventTypeWarning,
			v1alpha1.DestinationNotReadyReason,
			fmt.Sprintf("%s: %s", message, err.Error()),
		)
	} else {
		logging.Info(d.logger, message)
		d.eventRecorder.Eventf(
			d.destination,
			v1.EventTypeNormal,
			v1alpha1.DestinationReadyReason,
			message,
		)
	}
}
