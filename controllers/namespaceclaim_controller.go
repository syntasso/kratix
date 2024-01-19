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

	"golang.org/x/exp/slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	kyaml "sigs.k8s.io/yaml"

	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

// NamespaceClaimReconciler reconciles a NamespaceClaim object
type NamespaceClaimReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=namespaceclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=namespaceclaims/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=namespaceclaims/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NamespaceClaim object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.4/pkg/reconcile
func (r *NamespaceClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// TODO(user): your logic here

	ncList := &platformv1alpha1.NamespaceClaimList{}
	r.Client.List(ctx, ncList)

	destShouldContainNamespaces := map[string][]string{}

	for _, nc := range ncList.Items {
		val, ok := destShouldContainNamespaces[nc.Spec.Destination]
		if !ok {
			destShouldContainNamespaces[nc.Spec.Destination] = []string{nc.Spec.Namespace}
		} else {
			if !slices.Contains(val, nc.Spec.Namespace) {
				val = append(val, nc.Spec.Namespace)
				destShouldContainNamespaces[nc.Spec.Destination] = val
			}
		}
	}

	for dest, namespaces := range destShouldContainNamespaces {
		workPlacement := &platformv1alpha1.WorkPlacement{}
		workPlacement.Namespace = "kratix-platform-system"
		workPlacement.Name = dest + "-namespaces"

		op, err := controllerutil.CreateOrUpdate(context.Background(), r.Client, workPlacement, func() error {
			var workloads []v1alpha1.Workload
			for _, namespace := range namespaces {
				content, err := kyaml.Marshal(&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: namespace,
					},
					TypeMeta: metav1.TypeMeta{
						Kind:       "Namespace",
						APIVersion: "v1",
					},
				})
				if err != nil {
					return err
				}
				workloads = append(workloads, v1alpha1.Workload{
					Content:  string(content),
					Filepath: namespace + ".yaml",
				})
			}

			workPlacement.Spec.Workloads = workloads
			workPlacement.Spec.TargetDestinationName = dest
			controllerutil.AddFinalizer(workPlacement, repoCleanupWorkPlacementFinalizer)
			return nil
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("workplacement reconciled", "operation", op, "namespace", workPlacement.GetNamespace(), "workplacement", workPlacement.GetName(), "work", workPlacement.GetName(), "destination", dest)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NamespaceClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.NamespaceClaim{}).
		Complete(r)
}
