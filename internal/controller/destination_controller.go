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
	"path/filepath"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"sigs.k8s.io/yaml"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/writers"
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

	stateStoreReference       = "stateStoreRef"
	destinationNotReadyReason = "DestinationNotReady"
	destinationReadyReason    = "DestinationReady"
)

// DestinationReconciler reconciles a Destination object
type DestinationReconciler struct {
	Client        client.Client
	Log           logr.Logger
	Scheduler     *Scheduler
	EventRecorder record.EventRecorder
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=destinations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=bucketstatestores;gitstatestores,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=destinations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=destinations/finalizers,verbs=update

func (r *DestinationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	destination := &v1alpha1.Destination{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, destination); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	logger := r.Log.WithValues(
		"controller", "destination",
		"name", req.Name,
		"generation", destination.GetGeneration(),
	)
	logging.Info(logger, "reconciliation started")
	defer logReconcileDuration(logger, time.Now(), result, retErr)()

	opts := opts{
		client: r.Client,
		ctx:    ctx,
		logger: logger,
	}

	if r.needsFinalizerUpdate(destination) {
		logging.Debug(logger, "updating destination finalizers", "requestName", req.Name)
		if err := r.Client.Update(ctx, destination); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	writer, err := newWriter(opts, destination.Spec.StateStoreRef.Name, destination.Spec.StateStoreRef.Kind, destination.Spec.Path)
	if err != nil {
		if condErr := r.updateReadyCondition(destination, err); condErr != nil {
			return ctrl.Result{}, condErr
		}
		return ctrl.Result{}, err
	}

	if !destination.DeletionTimestamp.IsZero() {
		return r.deleteDestination(opts, destination, writer)
	}

	if !destination.Spec.InitWorkloads.Enabled {
		if err = r.deleteInitWorkloads(writer); err != nil {
			return ctrl.Result{}, err
		}

		if err = r.setConditionReadyInitWorkloads(destination); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("path", destination.Spec.Path)
	filePathMode := destination.GetFilepathMode()

	var writeErr error
	if writeErr = r.writeTestFiles(writer, filePathMode); writeErr != nil {
		result = defaultRequeue
		logging.Error(logger, writeErr, "unable to write dependencies to state store")
	}

	if condErr := r.updateReadyCondition(destination, writeErr); condErr != nil {
		return result, condErr
	}

	return result, nil
}

func (r *DestinationReconciler) needsFinalizerUpdate(destination *v1alpha1.Destination) bool {
	hasFinalizer := controllerutil.ContainsFinalizer(destination, destinationCleanupFinalizer)
	switch destination.GetCleanup() {
	case v1alpha1.DestinationCleanupAll:
		if !hasFinalizer {
			controllerutil.AddFinalizer(destination, destinationCleanupFinalizer)
			return true
		}
	case v1alpha1.DestinationCleanupNone:
		if hasFinalizer {
			controllerutil.RemoveFinalizer(destination, destinationCleanupFinalizer)
			return true
		}
	}
	return false
}

func (r *DestinationReconciler) writeTestFiles(writer writers.StateStoreWriter, filePathMode string) error {
	if err := r.createDependenciesPathWithExample(writer, filePathMode); err != nil {
		return err
	}

	if err := r.createResourcePathWithExample(writer, filePathMode); err != nil {
		return err
	}
	return nil
}

func (r *DestinationReconciler) createResourcePathWithExample(writer writers.StateStoreWriter, filePathMode string) error {
	kratixConfigMap := &v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kratix-info",
			Namespace: "kratix-worker-system",
		},
		Data: map[string]string{
			"canary": "this confirms your infrastructure is reading from Kratix state stores",
		},
	}
	nsBytes, _ := yaml.Marshal(kratixConfigMap)

	filePath := canaryConfigMapPath
	if filePathMode == v1alpha1.FilepathModeNestedByMetadata {
		filePath = filepath.Join(resourcesDir, filePath)
	}

	_, err := writer.UpdateFiles("", canaryWorkload, []v1alpha1.Workload{{
		Filepath: filePath,
		Content:  string(nsBytes)}}, nil)
	return err
}

func (r *DestinationReconciler) createDependenciesPathWithExample(writer writers.StateStoreWriter, filePathMode string) error {
	kratixNamespace := &v1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "kratix-worker-system"},
	}
	nsBytes, _ := yaml.Marshal(kratixNamespace)

	filePath := canaryNamespacePath
	if filePathMode == v1alpha1.FilepathModeNestedByMetadata {
		filePath = filepath.Join(dependenciesDir, filePath)
	}

	_, err := writer.UpdateFiles("", canaryWorkload, []v1alpha1.Workload{{
		Filepath: filePath,
		Content:  string(nsBytes)}}, nil)
	return err
}

func (r *DestinationReconciler) deleteInitWorkloads(writer writers.StateStoreWriter) error {
	filesToDelete := []string{canaryNamespacePath, canaryConfigMapPath}
	if _, err := writer.UpdateFiles("", canaryWorkload, nil, filesToDelete); err != nil {
		return fmt.Errorf("error deleting canary resources %v: %w", filesToDelete, err)
	}
	return nil
}

func (r *DestinationReconciler) deleteDestination(o opts, destination *v1alpha1.Destination, writer writers.StateStoreWriter) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(destination, destinationCleanupFinalizer) {
		if success, err := r.deleteDestinationWorkplacements(o, destination); !success || err != nil {
			return defaultRequeue, nil //nolint:nilerr // requeue rather than exponential backoff
		}

		if err := r.deleteStateStoreContents(o, writer); err != nil {
			return defaultRequeue, nil //nolint:nilerr // requeue rather than exponential backoff
		}

		controllerutil.RemoveFinalizer(destination, destinationCleanupFinalizer)
		if err := r.Client.Update(o.ctx, destination); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *DestinationReconciler) deleteStateStoreContents(o opts, writer writers.StateStoreWriter) error {
	logging.Debug(o.logger, "removing dependencies dir from repository")
	if _, err := writer.UpdateFiles(dependenciesDir, canaryWorkload, nil, nil); err != nil {
		logging.Error(o.logger, err, "error removing dependencies dir from repository")
		return err
	}

	logging.Debug(o.logger, "removing resources dir from repository")
	if _, err := writer.UpdateFiles(resourcesDir, canaryWorkload, nil, nil); err != nil {
		logging.Error(o.logger, err, "error removing resources dir from repository")
		return err
	}
	return nil
}

func (r *DestinationReconciler) deleteDestinationWorkplacements(o opts, destination *v1alpha1.Destination) (bool, error) {
	logging.Debug(o.logger, "deleting destination workplacements")
	workPlacementGVK := schema.GroupVersionKind{
		Group:   v1alpha1.GroupVersion.Group,
		Version: v1alpha1.GroupVersion.Version,
		Kind:    "WorkPlacement",
	}

	labels := map[string]string{
		TargetDestinationNameLabel: destination.Name,
	}
	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, &workPlacementGVK, labels)
	if err != nil {
		logging.Error(o.logger, err, "error deleting workplacements")
		return false, err
	}

	if resourcesRemaining {
		logging.Warn(o.logger, "couldn't remove workplacements, will try again")
		return false, nil
	}
	return true, nil
}

func (r *DestinationReconciler) updateReadyCondition(destination *v1alpha1.Destination, err error) error {
	eventType := v1.EventTypeNormal
	eventReason := destinationReadyReason
	eventMessage := fmt.Sprintf("Destination %q is ready", destination.Name)

	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "TestDocumentsWritten",
		Message:            "Test documents written to State Store",
		LastTransitionTime: metav1.Now(),
	}

	if err != nil {
		msg := fmt.Sprintf("Failed to write test documents to State Store: %s", err)
		condition.Status = metav1.ConditionFalse
		condition.Reason = "StateStoreWriteFailed"
		condition.Message = msg

		// Update event parameters for failure
		eventType = v1.EventTypeWarning
		eventReason = destinationNotReadyReason
		eventMessage = msg
	}

	return r.updateStatus(destination, condition, eventType, eventReason, eventMessage)
}

func (r *DestinationReconciler) setConditionReadyInitWorkloads(destination *v1alpha1.Destination) error {
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ReconciledSuccessfully",
		Message:            "Reconciled successfully, no init workloads were written",
		LastTransitionTime: metav1.Now(),
	}

	return r.updateStatus(destination, condition, v1.EventTypeNormal, destinationReadyReason, fmt.Sprintf("Destination %q is ready, skipped writing of the init workloads", destination.Name))
}

func (r *DestinationReconciler) updateStatus(destination *v1alpha1.Destination, condition metav1.Condition, eventType, eventReason, eventMessage string) error {
	changed := meta.SetStatusCondition(&destination.Status.Conditions, condition)
	if !changed {
		return nil
	}

	r.EventRecorder.Eventf(destination, eventType, eventReason, eventMessage)

	return r.Client.Status().Update(context.Background(), destination)
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
