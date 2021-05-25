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
	"fmt"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/synpl-platform/api/v1alpha1"
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
		r.Log.Error(err, "Failed creating CRD")
		//todo test for existance and handle gracefully.
		//return ctrl.Result{}, nil
	}

	gvk := schema.GroupVersionKind{
		Group:   crdToCreate.Spec.Group,
		Version: crdToCreate.Spec.Versions[0].Name,
		Kind:    crdToCreate.Spec.Names.Kind,
	}
	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(gvk)

	dynamicController := &dynamicController{
		client: r.Manager.GetClient(),
		scheme: r.Manager.GetScheme(),
		gvk:    &gvk,
	}

	ctrl.NewControllerManagedBy(r.Manager).
		For(unstructuredCRD).
		Complete(dynamicController)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromiseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Promise{}).
		Complete(r)
}

func (r *dynamicController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	fmt.Println("Dynamically Reconciling: " + req.Name)
	return ctrl.Result{}, nil
}
