package controllers

import (
	"context"
	"log"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
)

type FeatureFlagReconciler struct {
	Client client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

func (r *FeatureFlagReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var FeatureFlag v1alpha1.FeatureFlag
	if err := r.Client.Get(ctx, req.NamespacedName, &FeatureFlag); err != nil {
		log.Printf("unable to fetch FeatureFlag: %v", err)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// TODO: What do we actually want to reconcile here? Updating the status, maybe?

	return ctrl.Result{}, nil
}

func (r *FeatureFlagReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.FeatureFlag{}).
		Complete(r)
}
