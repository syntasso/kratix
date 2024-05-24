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
	"k8s.io/apimachinery/pkg/types"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
)

const (
	featureFlagWriteExampleManifests = "write-example-manifests-to-destinations"
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

	destination := &v1alpha1.Destination{}
	logger.Info("Registering Destination", "requestName", req.Name)
	// TODO: WHY IS THIS NOT WORKING?! :(
	// For some reason, I can't see this log, and none of the changes are building/loading
	// - I thought it was an image versioning issue, but build-and-load-kratix seems to be
	// happening correctly... so what's going on?
	logger.Info("TESTING")
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

	// TODO: Instead of this function call, we could watch for changes to the Destination
	// CRD and triggering all Works to reconcile on every change.
	if err := r.Scheduler.ReconcileAllDependencyWorks(); err != nil {
		logger.Error(err, "unable to schedule destination resources")
		return defaultRequeue, nil
	}

	// Get the feature flag to figure out if we should write the example files
	featureFlag := &v1alpha1.FeatureFlag{}
	featureFlagNamespacedName := types.NamespacedName{
		Namespace: destination.Namespace,
		Name:      featureFlagWriteExampleManifests,
	}
	if err := r.Client.Get(ctx, featureFlagNamespacedName, featureFlag); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Feature flag not found, skipping writing example manifests")
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}

	// Check if the feature flag's DestinationSelectors match this Destination's labels
	enabled := featureFlag.Spec.DefaultEnabled
	if featureFlag.Spec.DestinationSelectors != nil {
		for _, selector := range featureFlag.Spec.DestinationSelectors {
			if matchLabels(selector.MatchLabels, destination.Labels) {
				enabled = selector.Enabled
				break
			}
		}
	}

	if !enabled {
		logger.Info("Feature flag is disabled for this destination, skipping writing example manifests")
		return ctrl.Result{}, nil
	}

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

	return writer.WriteDirWithObjects(writers.PreserveExistingContentsInDir, resourcesDir, v1alpha1.Workload{
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

	return writer.WriteDirWithObjects(writers.PreserveExistingContentsInDir, dependenciesDir, v1alpha1.Workload{
		Filepath: "kratix-canary-namespace.yaml",
		Content:  string(nsBytes),
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *DestinationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Destination{}).
		Complete(r)
}

// TODO: There must be a common fn for this!
func matchLabels(selectorLabels, resourceLabels map[string]string) bool {
	for key, value := range selectorLabels {
		if resourceLabels[key] != value {
			return false
		}
	}
	return true
}
