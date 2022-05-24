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
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

// WorkPlacementReconciler reconciles a WorkPlacement object
type WorkPlacementReconciler struct {
	client.Client
	Scheme       *runtime.Scheme
	Log          logr.Logger
	BucketWriter BucketWriter
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
	_ = r.Log.WithValues("work-placement-controller", req.NamespacedName)
	workPlacement := &platformv1alpha1.WorkPlacement{}
	err := r.Client.Get(context.Background(), req.NamespacedName, workPlacement)
	if err != nil {
		r.Log.Error(err, "Error getting WorkPlacement: "+req.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if workPlacement != nil {
		work := r.getWork(workPlacement.Spec.WorkName)
		workerClusterBucketPath, _ := r.getWorkerClusterBucketPath(workPlacement.Spec.TargetClusterName)

		err = r.writeWorkToMinioBucket(work, workerClusterBucketPath)
		if err != nil {
			r.Log.Error(err, "Minio error, will try again in 5 seconds")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *WorkPlacementReconciler) writeWorkToMinioBucket(work *platformv1alpha1.Work, workerClusterBucketPath string) error {
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

	// Upload CRDs to Minio in separate files to other resources. The 00-crds files are applied before 01-resources by the Kustomise controller when it autogenerates its manifest. This is to ensure the APIs for resources exist before the resources are applied.
	// See https://github.com/fluxcd/kustomize-controller/blob/main/docs/spec/v1beta1/kustomization.md#generate-kustomizationyaml .

	crdObjectName := "00-" + work.GetNamespace() + "-" + work.GetName() + "-crds.yaml"
	err := r.writeWorkerClusterCRDs(workerClusterBucketPath, crdObjectName, crdBuffer.Bytes())
	if err != nil {
		r.Log.Error(err, "Error Writing CRDS to Worker Clusters")
		return err
	}

	resourcesObjectName := "01-" + work.GetNamespace() + "-" + work.GetName() + "-resources.yaml"
	err = r.writeWorkerClusterResources(workerClusterBucketPath, resourcesObjectName, resourceBuffer.Bytes())
	if err != nil {
		r.Log.Error(err, "Error uploading resources to Minio")
		return err
	}

	return nil
}

func (r *WorkPlacementReconciler) writeWorkerClusterResources(workerClustersBucketPath string, objectName string, fluxYaml []byte) error {
	bucketName := workerClustersBucketPath + "-kratix-resources"
	return r.BucketWriter.WriteObject(bucketName, objectName, fluxYaml)
}

func (r *WorkPlacementReconciler) writeWorkerClusterCRDs(workerClustersBucketPath, objectName string, fluxYaml []byte) error {
	bucketName := workerClustersBucketPath + "-kratix-crds"
	return r.BucketWriter.WriteObject(bucketName, objectName, fluxYaml)
}

func (r *WorkPlacementReconciler) getWorkerClusterBucketPath(targetCluster string) (string, error) {
	scheduledWorkerCluster := &platformv1alpha1.Cluster{}
	clusterName := types.NamespacedName{
		Name:      targetCluster,
		Namespace: "default",
	}
	err := r.Client.Get(context.Background(), clusterName, scheduledWorkerCluster)
	if err != nil {
		r.Log.Error(err, "Error listing available clusters")
		return "", err
	}
	return scheduledWorkerCluster.Spec.BucketPath, nil
}

func (r *WorkPlacementReconciler) getWork(workName string) *platformv1alpha1.Work {
	work := &platformv1alpha1.Work{}
	namespaceName := types.NamespacedName{
		Namespace: "default",
		Name:      workName,
	}
	r.Client.Get(context.Background(), namespaceName, work)
	return work
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkPlacementReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.WorkPlacement{}).
		Complete(r)
}
