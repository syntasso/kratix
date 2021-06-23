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
	"k8s.io/apimachinery/pkg/util/uuid"
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
	client            client.Client
	gvk               *schema.GroupVersionKind
	scheme            *runtime.Scheme
	promiseIdentifier string
	requestPipeline   []string
}

//+kubebuilder:rbac:groups=platform.synpl.syntasso.io,resources=promises,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.synpl.syntasso.io,resources=promises/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.synpl.syntasso.io,resources=promises/finalizers,verbs=update

//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch;delete

//+kubebuilder:rbac:groups="",resources=pods,verbs=create
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create

//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=create;escalate;bind
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=create

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

	// We should only proceed once the new gvk has been created in the API server
	if r.gvkDoesNotExist(gvk) {
		fmt.Println("REQUEUE")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	promiseIdentifier := promise.Name + "-" + promise.Namespace
	/*
	   promise identifier = x
	   x={promise.meta-data.name}-{promise.metadata.namespace}
	   redis-promise in default
	   redis-promise-default-sa
	   x-sa
	   x-clusterrole
	   x-clusterrolebinding
	   x-pipelinepod-y
	*/

	// CONTROLLER RBAC
	cr := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: promiseIdentifier + "-promise-controller",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{gvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural},
				Verbs:     []string{"get", "list", "update", "create", "patch", "delete", "watch"},
			},
			{
				APIGroups: []string{gvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural + "/finalizers"},
				Verbs:     []string{"update"},
			},
			{
				APIGroups: []string{gvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural + "/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
		},
	}
	err = r.Client.Create(ctx, &cr)
	if err != nil {
		fmt.Println(err.Error())
	}

	crb := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: promiseIdentifier + "-promise-controller-binding",
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     cr.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: "synpl-platform-system",
				Name:      "synpl-platform-controller-manager",
			},
		},
	}
	err = r.Client.Create(ctx, &crb)
	if err != nil {
		fmt.Println(err.Error())
	}
	// END CONTROLLER RBAC

	// PIPELINE RBAC
	cr = rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: promiseIdentifier + "-promise-pipeline-reader",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{gvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural},
				Verbs:     []string{"get", "list", "update", "create", "patch"},
			},
		},
	}
	err = r.Client.Create(ctx, &cr)
	if err != nil {
		fmt.Println(err.Error())
	}

	crb = rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: promiseIdentifier + "-promise-pipeline-reader-binding",
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
				Name:      promiseIdentifier + "-sa",
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
			Name:      promiseIdentifier + "-sa",
			Namespace: "default",
		},
	}
	err = r.Client.Create(ctx, &sa)
	if err != nil {
		fmt.Println(err.Error())
	}
	fmt.Println("Created SA")
	// END PIPELINE RBAC

	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(gvk)

	dynamicController := &dynamicController{
		client:            r.Manager.GetClient(),
		scheme:            r.Manager.GetScheme(),
		gvk:               &gvk,
		promiseIdentifier: promiseIdentifier,
		requestPipeline:   promise.Spec.RequestPipeline,
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
		fmt.Print("Failed getting Promise CRD " + err.Error())
		return ctrl.Result{}, nil
	}

	//kubectl get redis.redis.redis.opstreelabs.in opstree-redis --namespace default -oyaml > /output/object.yaml
	resourceRequestCommand := fmt.Sprintf("kubectl get %s.%s %s --namespace %s -oyaml > /output/object.yaml", strings.ToLower(r.gvk.Kind), r.gvk.Group, req.Name, req.Namespace)

	pod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "request-pipeline-" + r.promiseIdentifier + "-" + string(uuid.NewUUID()[0:5]),
			Namespace: "default",
		},
		Spec: v1.PodSpec{
			RestartPolicy:      v1.RestartPolicyOnFailure,
			ServiceAccountName: r.promiseIdentifier + "-sa",
			Containers: []v1.Container{
				{
					Name:    "writer",
					Image:   "bitnami/kubectl",
					Command: []string{"sh", "-c", "kubectl apply -f /input/"},
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: "/input",
							Name:      "output",
						},
					},
				},
			},
			InitContainers: []v1.Container{
				{
					Name:    "reader",
					Image:   "bitnami/kubectl",
					Command: []string{"sh", "-c", resourceRequestCommand},
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: "/output",
							Name:      "input",
						},
					},
				},
				{
					Name:  "kustomize",
					Image: r.requestPipeline[0],
					//Command: Supplied by the image author via ENTRYPOINT/CMD
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: "/input",
							Name:      "input",
						},
						{
							MountPath: "/output",
							Name:      "output",
						},
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "input",
					VolumeSource: v1.VolumeSource{
						EmptyDir: &v1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: "output",
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
