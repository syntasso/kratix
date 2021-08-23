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
	"encoding/json"
	"time"

	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
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
	log               logr.Logger
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=promises,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=promises/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=promises/finalizers,verbs=update

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
		return ctrl.Result{}, nil
	}

	promiseIdentifier := promise.Name + "-" + promise.Namespace

	//Cluster-Level Reconciliation
	workToCreate := &v1alpha1.Work{}
	workToCreate.Name = promiseIdentifier
	workToCreate.Namespace = "default"
	for _, u := range promise.Spec.WorkerResources {
		workToCreate.Spec.Workload.Manifests = append(workToCreate.Spec.Workload.Manifests, v1alpha1.Manifest{Unstructured: u.Unstructured})
	}

	r.Log.Info("Creating Work resource for promise: " + promiseIdentifier)

	err = r.Client.Create(ctx, workToCreate)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			//todo test for existence and handle gracefully.
			r.Log.Info("Works " + promiseIdentifier + "already exists")
		} else {
			r.Log.Error(err, "Error creating Works "+promiseIdentifier)
			return ctrl.Result{}, nil
		}
	}

	//Instance-Level Reconciliation
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
		if errors.IsAlreadyExists(err) {
			//todo test for existence and handle gracefully.
			r.Log.Info("CRD " + req.Name + "already exists")
			//return ctrl.Result{}, nil
		} else {
			r.Log.Error(err, "Error creating crd")
		}
	}

	crdToCreateGvk := schema.GroupVersionKind{
		Group:   crdToCreate.Spec.Group,
		Version: crdToCreate.Spec.Versions[0].Name,
		Kind:    crdToCreate.Spec.Names.Kind,
	}

	// We should only proceed once the new gvk has been created in the API server
	if r.gvkDoesNotExist(crdToCreateGvk) {
		r.Log.Info("Requeue:" + crdToCreate.Name + " is not ready on the API server yet.")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// CONTROLLER RBAC
	cr := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: promiseIdentifier + "-promise-controller",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{crdToCreateGvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural},
				Verbs:     []string{"get", "list", "update", "create", "patch", "delete", "watch"},
			},
			{
				APIGroups: []string{crdToCreateGvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural + "/finalizers"},
				Verbs:     []string{"update"},
			},
			{
				APIGroups: []string{crdToCreateGvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural + "/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
		},
	}
	err = r.Client.Create(ctx, &cr)
	if err != nil {
		r.Log.Error(err, "Error creating ClusterRole")
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
				Namespace: "kratix-platform-system",
				Name:      "kratix-platform-controller-manager",
			},
		},
	}
	err = r.Client.Create(ctx, &crb)
	if err != nil {
		r.Log.Error(err, "Error creating ClusterRoleBinding")
	}
	// END CONTROLLER RBAC

	// PIPELINE RBAC
	cr = rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: promiseIdentifier + "-promise-pipeline",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{crdToCreateGvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural},
				Verbs:     []string{"get", "list", "update", "create", "patch"},
			},
			{
				APIGroups: []string{"platform.kratix.io"},
				Resources: []string{"works"},
				Verbs:     []string{"get", "update", "create", "patch"},
			},
		},
	}
	err = r.Client.Create(ctx, &cr)
	if err != nil {
		r.Log.Error(err, "Error creating ClusterRole")
	}

	crb = rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: promiseIdentifier + "-promise-pipeline-binding",
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
		r.Log.Error(err, "Error creating ClusterRoleBinding")
	}

	r.Log.Info("Creating Service Account for " + promiseIdentifier)
	sa := v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      promiseIdentifier + "-sa",
			Namespace: "default",
		},
	}
	err = r.Client.Create(ctx, &sa)
	if err != nil {
		r.Log.Error(err, "Error creating Service Account for Promise "+promiseIdentifier)
	} else {
		r.Log.Info("Created ServiceAccount for Promise " + promiseIdentifier)
	}

	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(crdToCreateGvk)

	dynamicController := &dynamicController{
		client:            r.Manager.GetClient(),
		scheme:            r.Manager.GetScheme(),
		gvk:               &crdToCreateGvk,
		promiseIdentifier: promiseIdentifier,
		requestPipeline:   promise.Spec.RequestPipeline,
		log:               r.Log,
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
	r.log.Info("Dynamically Reconciling: " + req.Name)

	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(*r.gvk)

	err := r.client.Get(ctx, req.NamespacedName, unstructuredCRD)
	if err != nil {
		r.log.Error(err, "Failed getting Promise CRD")
		return ctrl.Result{}, nil
	}

	//kubectl get redis.redis.redis.opstreelabs.in opstree-redis --namespace default -oyaml > /output/object.yaml
	resourceRequestCommand := fmt.Sprintf("kubectl get %s.%s %s --namespace %s -oyaml > /output/object.yaml", strings.ToLower(r.gvk.Kind), r.gvk.Group, req.Name, req.Namespace)

	//promise-targetnamespace-mydatabase
	identifier := fmt.Sprintf("%s-%s-%s", r.promiseIdentifier, req.Namespace, req.Name)
	workCreatorCommand := fmt.Sprintf("./work-creator -identifier %s -input-directory /input", identifier)

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
					Image:   "syntasso/kratix-platform-work-creator:dev",
					Command: []string{"sh", "-c", workCreatorCommand},
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
					Name:  "resource-request-pipeline-stage-1",
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

	r.log.Info("Creating Pipeline for Promise resource request: " + identifier + ". The pipeline will now execute...")
	err = r.client.Create(ctx, &pod)
	if err != nil {
		r.log.Error(err, "Error creating Pod")
	}

	return ctrl.Result{}, nil
}
