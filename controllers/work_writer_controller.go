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

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

// WorkReconciler reconciles a Work object
type WorkWriterReconciler struct {
	client.Client
	Log          logr.Logger
	Scheme       *runtime.Scheme
	BucketWriter BucketWriter
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=works,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=works/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=works/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Work object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.7.2/pkg/reconcile
func (r *WorkWriterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = r.Log.WithValues("work-writer", req.NamespacedName)
	// get the workload
	work := &platformv1alpha1.Work{}
	err := r.Client.Get(context.Background(), req.NamespacedName, work)
	if err != nil {
		r.Log.Error(err, "Error getting Work: "+req.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	//	see if the workload has the necessary label
	if metav1.HasLabel(work.ObjectMeta, "cluster") {
		err = r.writeToMinio(work)
		if err != nil {
			r.Log.Error(err, "Minio error, will try again in 5 seconds")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *WorkWriterReconciler) writeToMinio(work *platformv1alpha1.Work) error {
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
	err := r.writeWorkerClusterCRDs(crdObjectName, crdBuffer.Bytes())
	if err != nil {
		r.Log.Error(err, "Error Writing CRDS to Worker Clusters")
		return err
	}

	resourcesObjectName := "01-" + work.GetNamespace() + "-" + work.GetName() + "-resources.yaml"
	err = r.writeWorkerClusterResources(resourcesObjectName, resourceBuffer.Bytes())
	if err != nil {
		r.Log.Error(err, "Error uploading resources to Minio")
		return err
	}

	return nil
}

func (r *WorkWriterReconciler) writeWorkerClusterResources(objectName string, fluxYaml []byte) error {
	var err error
	for _, workerClustersBucketPath := range r.getWorkerClustersBucketPaths() {
		bucketName := workerClustersBucketPath + "-kratix-resources"
		err = r.BucketWriter.WriteObject(bucketName, objectName, fluxYaml)
	}
	return err
}

func (r *WorkWriterReconciler) writeWorkerClusterCRDs(objectName string, fluxYaml []byte) error {
	var err error
	for _, workerClustersBucketPath := range r.getWorkerClustersBucketPaths() {
		err = r.BucketWriter.WriteObject(workerClustersBucketPath+"-kratix-crds", objectName, fluxYaml)
	}
	return err
}

func (r *WorkWriterReconciler) getWorkerClustersBucketPaths() []string {
	workerClusters := &platformv1alpha1.ClusterList{}
	err := r.Client.List(context.Background(), workerClusters, &client.ListOptions{})
	workerClustersBucketPaths := make([]string, len(workerClusters.Items))
	if err == nil {
		for x, cluster := range workerClusters.Items {
			workerClustersBucketPaths[x] = cluster.Spec.BucketPath
		}
	}
	return workerClustersBucketPaths
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkWriterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Work{}).
		Named("work-writer").
		Complete(r)
}
