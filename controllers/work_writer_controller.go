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
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
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
	Log    logr.Logger
	Scheme *runtime.Scheme
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
	err := r.writeCrdsToMinio(crdObjectName, crdBuffer.Bytes())
	if err != nil {
		r.Log.Error(err, "Error uploadding CRDs to Minio")
		return err
	}

	resourcesObjectName := "01-" + work.GetNamespace() + "-" + work.GetName() + "-resources.yaml"
	err = r.writeResourcesToMinio(resourcesObjectName, resourceBuffer.Bytes())
	if err != nil {
		r.Log.Error(err, "Error uploadding resources to Minio")
		return err
	}

	return nil
}

func (r *WorkWriterReconciler) writeResourcesToMinio(objectName string, fluxYaml []byte) error {
	bucketName := "kratix-resources"
	return r.yamlUploader(bucketName, objectName, fluxYaml)
}

func (r *WorkWriterReconciler) writeCrdsToMinio(objectName string, fluxYaml []byte) error {
	bucketName := "kratix-crds"
	return r.yamlUploader(bucketName, objectName, fluxYaml)
}

func (r *WorkWriterReconciler) yamlUploader(bucketName string, objectName string, fluxYaml []byte) error {

	if len(fluxYaml) == 0 {
		r.Log.Info("Empty byte[]. Nothing to write to Minio for " + objectName)
		return nil
	}

	ctx := context.Background()
	endpoint := "minio.kratix-platform-system.svc.cluster.local"
	accessKeyID := "minioadmin"
	secretAccessKey := "minioadmin"
	useSSL := false

	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})

	if err != nil {
		r.Log.Error(err, "Error initalising Minio client")
		return err
	}

	location := "local-minio"

	err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: location})
	if err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, errBucketExists := minioClient.BucketExists(ctx, bucketName)
		if errBucketExists == nil && exists {
			r.Log.Info("Minio Bucket " + bucketName + "already exists, will not recreate\n")
		} else {
			r.Log.Error(err, "Error connecting to Minio")
			return errBucketExists
		}
	} else {
		r.Log.Info("Successfully created Minio Bucket " + bucketName)
	}

	contentType := "text/x-yaml"
	reader := bytes.NewReader(fluxYaml)

	r.Log.Info("Creating Minio object " + objectName)
	_, err = minioClient.PutObject(ctx, bucketName, objectName, reader, reader.Size(), minio.PutObjectOptions{ContentType: contentType})
	r.Log.Info("Minio object " + objectName + " written")
	if err != nil {
		r.Log.Error(err, "Minio Error")
		return err
	}

	r.Log.Info(objectName + ". Check worker for next action...")
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkWriterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Work{}).
		Named("work-writer").
		Complete(r)
}
