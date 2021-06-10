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
	"encoding/json"
	"time"

	"fmt"
	"log"
	"strings"

	"github.com/go-logr/logr"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/syntasso/synpl-platform/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PromiseReconciler reconciles a Promise object
type PromiseReconciler struct {
	client.Client
	ApiextensionsClient *clientset.Clientset
	Log                 logr.Logger
	Scheme              *runtime.Scheme
	Manager             ctrl.Manager
}

type dynamicController struct {
	client client.Client
	gvk    *schema.GroupVersionKind
	scheme *runtime.Scheme
}

const (
	ServiceAccountName string = "redis-promise-sa"
)

//+kubebuilder:rbac:groups=platform.synpl.syntasso.io,resources=promises,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.synpl.syntasso.io,resources=promises/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.synpl.syntasso.io,resources=promises/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Promise object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.7.2/pkg/reconcile
func (r *PromiseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = r.Log.WithValues("promise", req.NamespacedName)

	promise := &v1alpha1.Promise{}
	err := r.Client.Get(ctx, req.NamespacedName, promise)
	if err != nil {
		r.Log.Error(err, "Failed getting Promise")
		fmt.Println(err.Error())
		return ctrl.Result{}, nil
	}

	crdToCreate := &apiextensionsv1.CustomResourceDefinition{}

	err = json.Unmarshal(promise.Spec.CRD.Raw, crdToCreate)
	if err != nil {
		r.Log.Error(err, "Failed unmarshalling CRD")
		return ctrl.Result{}, nil
	}

	_, err = r.ApiextensionsClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Create(ctx, crdToCreate, metav1.CreateOptions{})
	if err != nil {
		r.Log.Error(err, "CRD already exists. todo: handle this gracefully")
		//todo test for existance and handle gracefully.
		//return ctrl.Result{}, nil
	}

	gvk := schema.GroupVersionKind{
		Group:   crdToCreate.Spec.Group,
		Version: crdToCreate.Spec.Versions[0].Name,
		Kind:    crdToCreate.Spec.Names.Kind,
	}

	cr := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "redis-promise-reader",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{gvk.Group},
				Resources: []string{strings.ToLower(gvk.Kind)},
				Verbs:     []string{"get", "list", "update", "create", "patch"},
			},
		},
	}
	err = r.Client.Create(ctx, &cr)
	if err != nil {
		fmt.Println(err.Error())
	}

	crb := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "redis-promise-reader-binding",
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     cr.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: "default",
				Name:      ServiceAccountName,
			},
		},
	}
	err = r.Client.Create(ctx, &crb)
	if err != nil {
		fmt.Println(err.Error())
	}

	fmt.Println("Creating SA")
	sa := v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName,
			Namespace: "default",
		},
	}
	err = r.Client.Create(ctx, &sa)
	if err != nil {
		fmt.Println(err.Error())
	}
	fmt.Println("Created SA")

	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(gvk)

	dynamicController := &dynamicController{
		client: r.Manager.GetClient(),
		scheme: r.Manager.GetScheme(),
		gvk:    &gvk,
	}

	// We can only create the dynamicController once the creation of the Dynamic CRD has complete,
	// else k8s has no gvk to attatch the controller to.
	if r.gvkDoesNotExist(gvk) {
		fmt.Println("REQUEUE")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	ctrl.NewControllerManagedBy(r.Manager).
		For(unstructuredCRD).
		Complete(dynamicController)

	return ctrl.Result{}, nil
}

func (r *PromiseReconciler) gvkDoesNotExist(gvk schema.GroupVersionKind) bool {
	_, err := r.Manager.GetRESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	return err != nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromiseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Promise{}).
		Complete(r)
}

func (r *dynamicController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fmt.Println("Dynamically Reconciling: " + req.Name)

	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(*r.gvk)

	err := r.client.Get(ctx, req.NamespacedName, unstructuredCRD)

	if err != nil {
		fmt.Print("Failed getting Promise " + err.Error())
		return ctrl.Result{}, nil
	}

	//POD!! AHHHH
	pod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pipeline",
			Namespace: "default",
		},
		Spec: v1.PodSpec{
			RestartPolicy:      v1.RestartPolicyOnFailure,
			ServiceAccountName: ServiceAccountName,
			Containers: []v1.Container{
				{
					Name:    "writer",
					Image:   "bitnami/kubectl",
					Command: []string{"sh", "-c", "kubectl apply -f /test/output.yaml && sleep 100000"},
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: "/test",
							Name:      "volume",
						},
					},
				},
			},
			InitContainers: []v1.Container{
				{
					Name:    "reader",
					Image:   "bitnami/kubectl",
					Command: []string{"sh", "-c", "kubectl get redis.redis.redis.opstreelabs.in opstree-redis -oyaml > /test/object.yaml"},
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: "/test",
							Name:      "volume",
						},
					},
				},
				{
					Name:    "kustomize",
					Image:   "syntasso/kustomize-tester",
					Command: []string{"sh", "-c", "cp /transfer/* /test/; kustomize build /test/ > /test/output.yaml"},
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: "/test",
							Name:      "volume",
						},
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "volume",
					VolumeSource: v1.VolumeSource{
						EmptyDir: &v1.EmptyDirVolumeSource{},
					},
				},
			},
		},
	}

	err = r.client.Create(ctx, &pod)
	if err != nil {
		fmt.Println(err.Error())
	}

	// toPrint, _ := yaml.Marshal(unstructuredCRD.Object)
	// //todo - this minio object name should have GVK + namespace + resource name to be per-cluster unique
	// err = yamlUploader(req.Name+".yaml", toPrint)
	// if err != nil {
	// 	fmt.Print("Failed uploading to Minio" + err.Error())
	// 	return ctrl.Result{}, nil
	// }

	return ctrl.Result{}, nil
}

func yamlUploader(objectName string, fluxYaml []byte) error {
	ctx := context.Background()
	endpoint := "minio.synpl-system.svc.cluster.local"
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

	// Make a new bucket called mymusic.
	bucketName := "snypl"
	location := "local-minio"

	err = minioClient.MakeBucket(ctx, bucketName, minio.MakeBucketOptions{Region: location})
	if err != nil {
		// Check to see if we already own this bucket (which happens if you run this twice)
		exists, errBucketExists := minioClient.BucketExists(ctx, bucketName)
		if errBucketExists == nil && exists {
			log.Printf("We already own %s\n", bucketName)
		} else {
			log.Fatalln(err)
		}
	} else {
		log.Printf("Successfully created %s\n", bucketName)
	}

	//objectName := "namespace.yaml"
	contentType := "text/x-yaml"
	reader := bytes.NewReader(fluxYaml)

	_, err = minioClient.PutObject(ctx, bucketName, objectName, reader, -1, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		log.Fatalln(err)
	}
	return err
}
