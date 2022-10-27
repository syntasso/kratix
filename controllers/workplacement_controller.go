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
	"time"

	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

// WorkPlacementReconciler reconciles a WorkPlacement object
type WorkPlacementReconciler struct {
	client.Client
	Log          logr.Logger
	BucketWriter *BucketWriter
}

const WorkPlacementFinalizer = "finalizers.workplacement.kratix.io/repo-cleanup"

type repoFilePaths struct {
	ResourcesBucket, ResourcesName string
	CRDsBucket, CRDsName           string
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the WorkPlacement object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
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
		logger.Error(err, "Error getting WorkPlacement: "+req.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	paths, err := r.getRepoFilePaths(workPlacement.Spec.TargetClusterName, workPlacement.GetNamespace(), workPlacement.Spec.WorkName, logger)
	if err != nil {
		logger.Error(err, "Error getting file paths for the repository")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if !workPlacement.DeletionTimestamp.IsZero() {
		return r.deleteWorkPlacement(ctx, workPlacement, paths, logger)
	}

	// Ensure the finalizer is present
	if !controllerutil.ContainsFinalizer(workPlacement, WorkPlacementFinalizer) {
		return r.addFinalizer(ctx, workPlacement, logger)
	}

	work := r.getWork(workPlacement.Spec.WorkName, logger)
	err = r.writeWorkToRepository(work, paths, logger)
	if err != nil {
		logger.Error(err, "Error writing to repository, will try again in 5 seconds")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) deleteWorkPlacement(ctx context.Context, workPlacement *platformv1alpha1.WorkPlacement, bucketPath repoFilePaths, logger logr.Logger) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(workPlacement, WorkPlacementFinalizer) {
		logger.Info("cleaning up files on repository for " + workPlacement.Name)
		err := r.removeWorkFromRepository(bucketPath, logger)
		if err != nil {
			logger.Error(err, "error removing work from repository, will try again in 5 seconds")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		controllerutil.RemoveFinalizer(workPlacement, WorkPlacementFinalizer)
		if err := r.Update(ctx, workPlacement); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) writeWorkToRepository(work *platformv1alpha1.Work, paths repoFilePaths, logger logr.Logger) error {
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

	err := r.BucketWriter.WriteObject(paths.CRDsBucket, paths.CRDsName, crdBuffer.Bytes())
	if err != nil {
		logger.Error(err, "Error Writing CRDS to repository")
		return err
	}

	err = r.BucketWriter.WriteObject(paths.ResourcesBucket, paths.ResourcesName, resourceBuffer.Bytes())
	if err != nil {
		logger.Error(err, "Error uploading resources to repository")
		return err
	}

	return nil
}

func (r *WorkPlacementReconciler) removeWorkFromRepository(paths repoFilePaths, logger logr.Logger) error {
	logger.Info("Removing objects from repository")
	if err := r.BucketWriter.RemoveObject(paths.ResourcesBucket, paths.ResourcesName); err != nil {
		logger.Error(err, "Error removing resources from repository", paths.ResourcesBucket)
		return err
	}

	if err := r.BucketWriter.RemoveObject(paths.CRDsBucket, paths.CRDsName); err != nil {
		logger.Error(err, "Error removing crds from repository", paths.CRDsBucket)
		return err
	}
	return nil
}

func (r *WorkPlacementReconciler) getRepoFilePaths(targetCluster, workNamespace, workName string, logger logr.Logger) (repoFilePaths, error) {
	scheduledWorkerCluster := &platformv1alpha1.Cluster{}
	clusterName := types.NamespacedName{
		Name:      targetCluster,
		Namespace: "default",
	}
	err := r.Client.Get(context.Background(), clusterName, scheduledWorkerCluster)
	if err != nil {
		logger.Error(err, "Error listing available clusters")
		return repoFilePaths{}, err
	}

	base := scheduledWorkerCluster.Spec.BucketPath
	return repoFilePaths{
		ResourcesBucket: base + "-kratix-resources",
		ResourcesName:   "01-" + workNamespace + "-" + workName + "-resources.yaml",
		CRDsBucket:      base + "-kratix-crds",
		CRDsName:        "00-" + workNamespace + "-" + workName + "-crds.yaml",
	}, nil
}

func (r *WorkPlacementReconciler) getWork(workName string, logger logr.Logger) *platformv1alpha1.Work {
	work := &platformv1alpha1.Work{}
	namespaceName := types.NamespacedName{
		Namespace: "default",
		Name:      workName,
	}
	r.Client.Get(context.Background(), namespaceName, work)
	return work
}

func (r *WorkPlacementReconciler) addFinalizer(ctx context.Context, workPlacement *platformv1alpha1.WorkPlacement, logger logr.Logger) (ctrl.Result, error) {
	controllerutil.AddFinalizer(workPlacement, WorkPlacementFinalizer)
	if err := r.Update(ctx, workPlacement); err != nil {
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
