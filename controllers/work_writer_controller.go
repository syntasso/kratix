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
	"fmt"
	"log"

	"github.com/go-logr/logr"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/syntasso/synpl-platform/api/v1alpha1"
)

// WorkReconciler reconciles a Work object
type WorkWriterReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=platform.synpl.syntasso.io,resources=works,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.synpl.syntasso.io,resources=works/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.synpl.syntasso.io,resources=works/finalizers,verbs=update

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
		fmt.Println(err.Error())
	}

	//	see if the workload has the necessary label
	if metav1.HasLabel(work.ObjectMeta, "cluster") {
		writeToMinio(work)
	}

	return ctrl.Result{}, nil

}

func writeToMinio(work *platformv1alpha1.Work) error {
	objectName := work.GetNamespace() + "-" + work.GetName() + ".yaml"

	serializer := json.NewSerializerWithOptions(
		json.DefaultMetaFactory, nil, nil,
		json.SerializerOptions{
			Yaml:   true,
			Pretty: true,
			Strict: true,
		},
	)

	buffer := bytes.NewBuffer([]byte{})
	writer := json.YAMLFramer.NewFrameWriter(buffer)

	for _, manifest := range work.Spec.Workload.Manifests {
		serializer.Encode(&manifest, writer)
	}

	fmt.Println("Our bytes are " + buffer.String())
	return yamlUploader(objectName, buffer.Bytes())
}

func yamlUploader(objectName string, fluxYaml []byte) error {
	ctx := context.Background()
	endpoint := "minio.synpl-platform-system.svc.cluster.local"
	accessKeyID := "minioadmin"
	secretAccessKey := "minioadmin"
	useSSL := false

	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKeyID, secretAccessKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		log.Fatalln(err)
	}

	bucketName := "synpl"
	location := "local-minio"

	err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: location})
	if err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, errBucketExists := minioClient.BucketExists(ctx, bucketName)
		if errBucketExists == nil && exists {
			log.Printf("Minio Bucket %s already exists\n", bucketName)
		} else {
			fmt.Println("AHAHAHHHHHHHHH")
			log.Fatalln(err)
		}
	} else {
		log.Printf("Successfully created Minio Bucket %s\n", bucketName)
	}

	contentType := "text/x-yaml"
	reader := bytes.NewReader(fluxYaml)

	_, err = minioClient.PutObject(ctx, bucketName, objectName, reader, -1, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		log.Printf("Minio Error: %s", err.Error())
	}
	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkWriterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Work{}).
		Named("work-writer").
		Complete(r)
}
