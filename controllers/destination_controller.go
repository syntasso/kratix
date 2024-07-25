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
	"fmt"
	"path/filepath"

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

const canaryWorkload = "kratix-canary"

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

	writer, err := newWriter(opts, *destination)
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
		logger.Error(err, "unable to write dependencies to state store")
		return defaultRequeue, nil
	}

	if err := r.createResourcePathWithExample(writer); err != nil {
		logger.Error(err, "unable to write dependencies to state store")
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

	_, err := writer.UpdateFiles("", canaryWorkload, []v1alpha1.Workload{{
		Filepath: fmt.Sprintf("%s/kratix-canary-configmap.yaml", resourcesDir),
		Content:  string(nsBytes)}}, nil)
	return err
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

	_, err := writer.UpdateFiles("", canaryWorkload, []v1alpha1.Workload{{
		Filepath: fmt.Sprintf("%s/kratix-canary-namespace.yaml", dependenciesDir),
		Content:  string(nsBytes)}}, nil)
	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *DestinationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Destination{}).
		Complete(r)
}
