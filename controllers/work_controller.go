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

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WorkReconciler reconciles a Work object
type WorkReconciler struct {
	Client    client.Client
	Log       logr.Logger
	Scheduler WorkScheduler
	Disabled  bool
}

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 . WorkScheduler
type WorkScheduler interface {
	ReconcileWork(work *platformv1alpha1.Work) ([]string, error)
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=works,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=works/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=works/finalizers,verbs=update
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=workplacements/status,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Work object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.7.2/pkg/reconcile
func (r *WorkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.Disabled {
		//TODO tech debt. We want this controller running *for some unit tests*, not
		//for all. So we do this to disable it
		return ctrl.Result{}, nil
	}
	logger := r.Log.WithValues("work", req.NamespacedName)
	logger.Info("Reconciling Work")

	work := &platformv1alpha1.Work{}
	err := r.Client.Get(context.Background(), req.NamespacedName, work)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Error getting Work")
		return ctrl.Result{Requeue: false}, err
	}

	logger.Info("Requesting scheduling for Work")
	unscheduledWorkloadGroupIDs, err := r.Scheduler.ReconcileWork(work)
	if err != nil {
		//TODO remove this error checking
		//temp fix until resolved: https://syntasso.slack.com/archives/C044T9ZFUMN/p1674058648965449
		logger.Error(err, "Error scheduling Work, will retry...")
		return defaultRequeue, err
	}

	if work.IsResourceRequest() && len(unscheduledWorkloadGroupIDs) > 0 {
		logger.Info("no available Destinations for some of the workload groups, trying again shortly", "workloadGroupIDs", unscheduledWorkloadGroupIDs)
		return slowRequeue, nil
	}

	return ctrl.Result{}, nil

}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Work{}).
		Owns(&platformv1alpha1.WorkPlacement{}).
		Complete(r)
}
