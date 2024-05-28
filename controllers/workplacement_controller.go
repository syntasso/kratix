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
	"github.com/go-logr/logr"
	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/writers"
)

const (
	resourcesDir    = "resources"
	dependenciesDir = "dependencies"
)

type StateFile struct {
	Files []string `json:"files"`
}

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

	workPlacement := &v1alpha1.WorkPlacement{}
	err := r.Client.Get(context.Background(), req.NamespacedName, workPlacement)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error getting WorkPlacement", "workPlacement", req.Name)
		return defaultRequeue, nil
	}

	destination := &v1alpha1.Destination{}
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

	//Mock this out
	writer, err := newWriter(opts, *destination)
	if err != nil {
		if errors.IsNotFound(err) {
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}

	if !workPlacement.DeletionTimestamp.IsZero() {
		return r.deleteWorkPlacement(ctx, writer, workPlacement, destination.GetFilepathMode(), logger)
	}

	if resourceutil.FinalizersAreMissing(workPlacement, workPlacementFinalizers) {
		return addFinalizers(opts, workPlacement, workPlacementFinalizers)
	}

	err = r.writeWorkloadsToStateStore(writer, *workPlacement, *destination, logger)
	if err != nil {
		logger.Error(err, "Error writing to repository, will try again in 5 seconds")
		return defaultRequeue, err
	}

	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) deleteWorkPlacement(ctx context.Context, writer writers.StateStoreWriter, workPlacement *v1alpha1.WorkPlacement, filePathMode string, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer) {
		return ctrl.Result{}, nil
	}

	logger.Info("cleaning up files on repository", "repository", workPlacement.Name)

	var err error
	if filePathMode == v1alpha1.FilepathModeNone {
		var kratixFile []byte
		if kratixFile, err = writer.ReadFile(fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name)); err != nil {
			return defaultRequeue, err
		}
		stateFile := StateFile{}
		if err = yaml.Unmarshal(kratixFile, &stateFile); err != nil {
			return defaultRequeue, err
		}
		err = writer.UpdateFiles(workPlacement.Name, nil, stateFile.Files)
	} else {
		err = writer.UpdateInDir(getDir(*workPlacement)+"/", workPlacement.Name, nil)
	}

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

func (r *WorkPlacementReconciler) writeWorkloadsToStateStore(writer writers.StateStoreWriter, workPlacement v1alpha1.WorkPlacement, destination v1alpha1.Destination, logger logr.Logger) error {
	var err error
	if destination.GetFilepathMode() == v1alpha1.FilepathModeNone {
		var kratixFile []byte
		if kratixFile, err = writer.ReadFile(fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name)); err != nil {
			return err
		}
		oldStateFile := StateFile{}
		if err = yaml.Unmarshal(kratixFile, &oldStateFile); err != nil {
			return err
		}

		newStateFile := StateFile{
			Files: workLoadsFilenames(workPlacement.Spec.Workloads),
		}
		stateFileContent, err := yaml.Marshal(newStateFile)
		if err != nil {
			return err
		}

		stateFileWorkload := v1alpha1.Workload{
			Filepath: fmt.Sprintf(".kratix/%s-%s.yaml", workPlacement.Namespace, workPlacement.Name),
			Content:  string(stateFileContent),
		}

		err = writer.UpdateFiles(workPlacement.Name, append(workPlacement.Spec.Workloads, stateFileWorkload), cleanupWorkloads(oldStateFile.Files, workPlacement.Spec.Workloads))
	} else {
		err = writer.UpdateInDir(getDir(workPlacement), workPlacement.Name, workPlacement.Spec.Workloads)
	}

	if err != nil {
		logger.Error(err, "Error writing resources to repository")
		return err
	}
	return nil
}

func workLoadsFilenames(works []v1alpha1.Workload) []string {
	var result []string
	for _, w := range works {
		result = append(result, w.Filepath)
	}
	return result
}

func cleanupWorkloads(old []string, new []v1alpha1.Workload) []string {
	works := make(map[string]bool)
	for _, w := range new {
		works[w.Filepath] = true
	}
	var result []string
	for _, w := range old {
		if _, ok := works[w]; !ok {
			result = append(result, w)
		}
	}
	return result
}

func getDir(workPlacement v1alpha1.WorkPlacement) string {
	if workPlacement.Spec.ResourceName == "" {
		//dependencies/<promise-name>/<pipeline-name>/<dir-sha>/
		return filepath.Join(dependenciesDir, workPlacement.Spec.PromiseName, workPlacement.PipelineName(), shortID(workPlacement.Spec.ID))
	} else {
		//resources/<rr-namespace>/<promise-name>/<rr-name>/<pipeline-name>/<dir-sha>/
		return filepath.Join(resourcesDir, workPlacement.GetNamespace(), workPlacement.Spec.PromiseName, workPlacement.Spec.ResourceName, workPlacement.PipelineName(), shortID(workPlacement.Spec.ID))
	}
}

func (r *WorkPlacementReconciler) getWork(workName, workNamespace string, logger logr.Logger) *v1alpha1.Work {
	work := &v1alpha1.Work{}
	namespaceName := types.NamespacedName{
		Namespace: workNamespace,
		Name:      workName,
	}
	r.Client.Get(context.Background(), namespaceName, work)
	return work
}

func (r *WorkPlacementReconciler) addFinalizer(ctx context.Context, workPlacement *v1alpha1.WorkPlacement, logger logr.Logger) (ctrl.Result, error) {
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
		For(&v1alpha1.WorkPlacement{}).
		Complete(r)
}
