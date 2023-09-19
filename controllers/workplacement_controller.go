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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
)

const (
	resourcesDir    = "resources"
	dependenciesDir = "dependencies"
)

// WorkPlacementReconciler reconciles a WorkPlacement object
type WorkPlacementReconciler struct {
	Client client.Client
	Log    logr.Logger
}

const repoCleanupWorkPlacementFinalizer = "finalizers.workplacement.kratix.io/repo-cleanup"

var workPlacementFinalizers = []string{repoCleanupWorkPlacementFinalizer}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the destination closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *WorkPlacementReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("work-placement-controller", req.NamespacedName)

	workPlacement := &platformv1alpha1.WorkPlacement{}
	err := r.Client.Get(context.Background(), req.NamespacedName, workPlacement)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error getting WorkPlacement", "workPlacement", req.Name)
		return defaultRequeue, nil
	}

	destination := &platformv1alpha1.Destination{}
	destinationName := client.ObjectKey{
		Name: workPlacement.Spec.TargetDestinationName,
	}
	err = r.Client.Get(context.Background(), destinationName, destination)
	if err != nil {
		logger.Error(err, "Error listing available destinations")
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

	if !workPlacement.DeletionTimestamp.IsZero() {
		return r.deleteWorkPlacement(ctx, writer, workPlacement, logger)
	}

	if finalizersAreMissing(workPlacement, workPlacementFinalizers) {
		return addFinalizers(opts, workPlacement, workPlacementFinalizers)
	}

	err = r.writeWorkloadsToStateStore(writer, *workPlacement, logger)
	if err != nil {
		logger.Error(err, "Error writing to repository, will try again in 5 seconds")
		return defaultRequeue, err
	}

	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) deleteWorkPlacement(ctx context.Context, writer writers.StateStoreWriter, workPlacement *platformv1alpha1.WorkPlacement, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer) {
		return ctrl.Result{}, nil
	}

	logger.Info("cleaning up files on repository", "repository", workPlacement.Name)
	err := r.removeWorkFromRepository(writer, *workPlacement, logger)
	if err != nil {
		logger.Error(err, "error removing work from repository, will try again in 5 seconds")
		return defaultRequeue, err
	}

	controllerutil.RemoveFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer)
	err = r.Client.Update(ctx, workPlacement)
	if err != nil {
		return defaultRequeue, err
	}
	return fastRequeue, nil
}

func (r *WorkPlacementReconciler) writeWorkloadsToStateStore(writer writers.StateStoreWriter, workPlacement v1alpha1.WorkPlacement, logger logr.Logger) error {
	err := writer.WriteDirWithObjects(writers.DeleteExistingContentsInDir, getDir(workPlacement), workPlacement.Spec.Workloads...)
	if err != nil {
		logger.Error(err, "Error writing resources to repository")
		return err
	}

	return nil
}

func (r *WorkPlacementReconciler) removeWorkFromRepository(writer writers.StateStoreWriter, workPlacement v1alpha1.WorkPlacement, logger logr.Logger) error {
	//MinIO needs a trailing slash to delete a directory
	dir := getDir(workPlacement) + "/"
	if err := writer.RemoveObject(dir); err != nil {
		logger.Error(err, "Error removing workloads from repository", "dir", dir)
		return err
	}
	return nil
}

func getDir(workPlacement v1alpha1.WorkPlacement) string {
	if workPlacement.Spec.ResourceName == "" {
		//dependencies/<promise-name>
		return filepath.Join(dependenciesDir, workPlacement.Spec.PromiseName)
	} else {
		//resources/<rr-namespace>/<promise-name>/<rr-name>
		return filepath.Join(resourcesDir, workPlacement.GetNamespace(), workPlacement.Spec.PromiseName, workPlacement.Spec.ResourceName)
	}
}

func (r *WorkPlacementReconciler) getWork(workName, workNamespace string, logger logr.Logger) *platformv1alpha1.Work {
	work := &platformv1alpha1.Work{}
	namespaceName := types.NamespacedName{
		Namespace: workNamespace,
		Name:      workName,
	}
	r.Client.Get(context.Background(), namespaceName, work)
	return work
}

func (r *WorkPlacementReconciler) addFinalizer(ctx context.Context, workPlacement *platformv1alpha1.WorkPlacement, logger logr.Logger) (ctrl.Result, error) {
	controllerutil.AddFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer)
	if err := r.Client.Update(ctx, workPlacement); err != nil {
		logger.Error(err, "failed to add finalizer to WorkPlacement")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkPlacementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.WorkPlacement{}).
		Complete(r)
}
