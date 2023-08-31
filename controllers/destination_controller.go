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

	"k8s.io/apimachinery/pkg/api/errors"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the destination closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *DestinationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues(
		"destination", req.NamespacedName,
	)

	destination := &platformv1alpha1.Destination{}
	logger.Info("Registering Destination", "requestName", req.Name)
	if err := r.Client.Get(ctx, client.ObjectKey{Name: req.Name}, destination); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	writer, err := newWriter(ctx, r.Client, *destination, logger)
	if err != nil {
		if errors.IsNotFound(err) {
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}

	//destination.Spec.Path is optional, may be empty
	path := filepath.Join(destination.Spec.Path, destination.Name)
	logger = logger.WithValues("path", path)

	if err := r.createDependenciesPathWithExample(writer); err != nil {
		logger.Info("unable to write dependencies to state store", "error", err)
		return defaultRequeue, nil
	}

	if err := r.createResourcePathWithExample(writer); err != nil {
		logger.Info("unable to write resources to state store", "error", err)
		return defaultRequeue, nil
	}

	if err := r.Scheduler.ReconcileDestination(); err != nil {
		logger.Info("unable to schedule destination resources", "error", err)
		return defaultRequeue, nil
	}
	return ctrl.Result{}, nil
}

func (r *DestinationReconciler) createResourcePathWithExample(writer writers.StateStoreWriter) error {
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

	return writer.WriteDirWithObjects(writers.PreserveExistingContentsInDir, resourcesDir, platformv1alpha1.Workload{
		Filepath: "kratix-canary-configmap.yaml",
		Content:  string(nsBytes),
	})
}

func (r *DestinationReconciler) createDependenciesPathWithExample(writer writers.StateStoreWriter) error {
	kratixNamespace := &v1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "kratix-worker-system"},
	}
	nsBytes, _ := yaml.Marshal(kratixNamespace)

	return writer.WriteDirWithObjects(writers.PreserveExistingContentsInDir, dependenciesDir, platformv1alpha1.Workload{
		Filepath: "kratix-canary-namespace.yaml",
		Content:  string(nsBytes),
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *DestinationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Destination{}).
		Complete(r)
}
