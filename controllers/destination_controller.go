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

package controllers

import (
	"context"
	"path/filepath"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	canaryWorkload              = "kratix-canary"
	destinationCleanupFinalizer = v1alpha1.KratixPrefix + "destination-cleanup"
)

// DestinationReconciler reconciles a Destination object
type DestinationReconciler struct {
	Client    client.Client
	Log       logr.Logger
	Scheduler *Scheduler
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=destinations,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=bucketstatestores;gitstatestores,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=destinations/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=destinations/finalizers,verbs=update

func (r *DestinationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues(
		"destination", req.NamespacedName,
	)

	destination := &v1alpha1.Destination{}
	logger.Info("Registering Destination", "requestName", req.Name)
	if err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, destination); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	opts := opts{
		client: r.Client,
		ctx:    ctx,
		logger: logger,
	}

	if r.needsFinalizerUpdate(destination) {
		if err := r.Client.Update(ctx, destination); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	writer, err := newWriter(opts, *destination)
	if err != nil {
		if errors.IsNotFound(err) {
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}

	if !destination.DeletionTimestamp.IsZero() {
		return r.deleteDestination(opts, destination, writer)
	}

	//destination.Spec.Path is optional, may be empty
	path := filepath.Join(destination.Spec.Path, destination.Name)
	logger = logger.WithValues("path", path)
	filePathMode := destination.GetFilepathMode()

	if err = r.createDependenciesPathWithExample(writer, filePathMode); err != nil {
		logger.Error(err, "unable to write dependencies to state store")
		return defaultRequeue, nil
	}

	if err = r.createResourcePathWithExample(writer, filePathMode); err != nil {
		logger.Error(err, "unable to write dependencies to state store")
		return defaultRequeue, nil
	}

	return ctrl.Result{}, nil
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
			"canary": "the confirms your infrastructure is reading from Kratix state stores",
		},
	}
	nsBytes, _ := yaml.Marshal(kratixConfigMap)

	filePath := "kratix-canary-configmap.yaml"
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

	filePath := "kratix-canary-namespace.yaml"
	if filePathMode == v1alpha1.FilepathModeNestedByMetadata {
		filePath = filepath.Join(dependenciesDir, filePath)
	}

	_, err := writer.UpdateFiles("", canaryWorkload, []v1alpha1.Workload{{
		Filepath: filePath,
		Content:  string(nsBytes)}}, nil)
	return err
}

func (r *DestinationReconciler) deleteDestination(o opts, destination *v1alpha1.Destination, writer writers.StateStoreWriter) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(destination, destinationCleanupFinalizer) {
		if success, err := r.deleteDestinationWorkplacements(o, destination); !success || err != nil {
			return defaultRequeue, err
		}

		if err := r.deleteStateStoreContents(o, writer); err != nil {
			return defaultRequeue, err
		}

		controllerutil.RemoveFinalizer(destination, destinationCleanupFinalizer)
		if err := r.Client.Update(o.ctx, destination); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *DestinationReconciler) deleteStateStoreContents(o opts, writer writers.StateStoreWriter) error {
	o.logger.Info("removing dependencies dir from repository")
	if _, err := writer.UpdateFiles(dependenciesDir, canaryWorkload, nil, nil); err != nil {
		o.logger.Error(err, "error removing dependencies dir from repository")
		return err
	}

	o.logger.Info("removing resources dir from repository")
	if _, err := writer.UpdateFiles(resourcesDir, canaryWorkload, nil, nil); err != nil {
		o.logger.Error(err, "error removing resources dir from repository")
		return err
	}
	return nil
}

func (r *DestinationReconciler) deleteDestinationWorkplacements(o opts, destination *v1alpha1.Destination) (bool, error) {
	o.logger.Info("deleting destination workplacements")
	workPlacementGVK := schema.GroupVersionKind{
		Group:   v1alpha1.GroupVersion.Group,
		Version: v1alpha1.GroupVersion.Version,
		Kind:    "WorkPlacement",
	}

	labels := map[string]string{
		targetDestinationNameLabel: destination.Name,
	}
	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, &workPlacementGVK, labels)
	if err != nil {
		o.logger.Error(err, "error deleting workplacements")
		return false, err
	}

	if resourcesRemaining {
		o.logger.Info("couldn't remove workplacements, will try again")
		return false, nil
	}
	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DestinationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Destination{}).
		Complete(r)
}
