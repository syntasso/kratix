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
	"bytes"
	"context"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
)

// WorkPlacementReconciler reconciles a WorkPlacement object
type WorkPlacementReconciler struct {
	Client client.Client
	Log    logr.Logger
}

const repoCleanupWorkPlacementFinalizer = "finalizers.workplacement.kratix.io/repo-cleanup"

var workPlacementFinalizers = []string{repoCleanupWorkPlacementFinalizer}

type repoFilePaths struct {
	Resources string
	CRDs      string
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
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

	cluster := &platformv1alpha1.Cluster{}
	clusterName := types.NamespacedName{
		Name: workPlacement.Spec.TargetClusterName,
	}
	err = r.Client.Get(context.Background(), clusterName, cluster)
	if err != nil {
		logger.Error(err, "Error listing available clusters")
		return ctrl.Result{}, err
	}

	work := r.getWork(workPlacement.Spec.WorkName, workPlacement.GetNamespace(), logger)
	workNamespacedName := workPlacement.Namespace + "-" + workPlacement.Spec.WorkName

	paths := repoFilePaths{
		Resources: "resources/01-" + workNamespacedName + "-resources.yaml",
		CRDs:      "crds/00-" + workNamespacedName + "-crds.yaml",
	}

	writer, err := newWriter(ctx, r.Client, *cluster, logger)
	if err != nil {
		if errors.IsNotFound(err) {
			return defaultRequeue, nil
		}
		return ctrl.Result{}, err
	}

	if !workPlacement.DeletionTimestamp.IsZero() {
		return r.deleteWorkPlacement(ctx, writer, workPlacement, paths, logger)
	}

	if finalizersAreMissing(workPlacement, workPlacementFinalizers) {
		return addFinalizers(ctx, r.Client, workPlacement, workPlacementFinalizers, logger)
	}

	err = r.writeWorkToRepository(writer, work, paths, logger)
	if err != nil {
		logger.Error(err, "Error writing to repository, will try again in 5 seconds")
		return defaultRequeue, err
	}

	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) deleteWorkPlacement(ctx context.Context, writer writers.StateStoreWriter, workPlacement *platformv1alpha1.WorkPlacement, paths repoFilePaths, logger logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer) {
		return ctrl.Result{}, nil
	}

	logger.Info("cleaning up files on repository", "repository", workPlacement.Name)
	err := r.removeWorkFromRepository(writer, paths, logger)
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

func (r *WorkPlacementReconciler) writeWorkToRepository(writer writers.StateStoreWriter, work *platformv1alpha1.Work, paths repoFilePaths, logger logr.Logger) error {
	serializer := json.NewSerializerWithOptions(
		json.DefaultMetaFactory, nil, nil,
		json.SerializerOptions{
			Yaml:   true,
			Pretty: true,
			Strict: true,
		},
	)

	crdBuffer := bytes.NewBuffer([]byte{})
	crdWriter := json.YAMLFramer.NewFrameWriter(crdBuffer)
	resourceBuffer := bytes.NewBuffer([]byte{})
	resourceWriter := json.YAMLFramer.NewFrameWriter(resourceBuffer)

	for _, manifest := range work.Spec.Workload.Manifests {
		if manifest.GetKind() == "CustomResourceDefinition" {
			serializer.Encode(&manifest, crdWriter)
		} else {
			serializer.Encode(&manifest, resourceWriter)
		}
	}

	// Upload CRDs to repository in separate files to other resources to ensure
	// the APIs for resources exist before the resources are applied. The
	// 00-crds files are applied before 01-resources by the Kustomise controller
	// when it autogenerates its manifest.
	// https://github.com/fluxcd/kustomize-controller/blob/main/docs/spec/v1beta1/kustomization.md#generate-kustomizationyaml

	err := writer.WriteObject(paths.CRDs, crdBuffer.Bytes())
	if err != nil {
		logger.Error(err, "Error writing CRDS to repository")
		return err
	}

	err = writer.WriteObject(paths.Resources, resourceBuffer.Bytes())
	if err != nil {
		logger.Error(err, "Error writing resources to repository")
		return err
	}

	return nil
}

func (r *WorkPlacementReconciler) removeWorkFromRepository(writer writers.StateStoreWriter, paths repoFilePaths, logger logr.Logger) error {
	if err := writer.RemoveObject(paths.Resources); err != nil {
		logger.Error(err, "Error removing resources from repository", "resourcePath", paths.Resources)
		return err
	}

	if err := writer.RemoveObject(paths.CRDs); err != nil {
		logger.Error(err, "Error removing crds from repository", "resourcePath", paths.CRDs)
		return err
	}
	return nil
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
