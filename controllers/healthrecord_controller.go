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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// HealthRecordReconciler reconciles a HealthRecord object
type HealthRecordReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=healthrecords,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=healthrecords/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=healthrecords/finalizers,verbs=update

func (r *HealthRecordReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("healthRecord", req.Name, "namespace", req.Namespace)
	logger.Info("Reconciling HealthRecord")

	healthRecord := &platformv1alpha1.HealthRecord{}
	if err := r.Get(ctx, req.NamespacedName, healthRecord); err != nil {
		return r.ignoreNotFound(logger, err, "Failed getting healthRecord")
	}

	logger = logger.WithValues(
		"promiseRef", healthRecord.Data.PromiseRef,
		"resourceRef", healthRecord.Data.ResourceRef,
	)

	promise := &platformv1alpha1.Promise{}
	promiseName := client.ObjectKey{Name: healthRecord.Data.PromiseRef.Name}
	if err := r.Get(ctx, promiseName, promise); err != nil {
		return r.ignoreNotFound(logger, err, "Failed getting Promise")
	}

	promiseGVK, _, err := promise.GetAPI()
	if err != nil {
		logger.Error(err, "Failed getting promise gvk")
		return defaultRequeue, nil
	}

	resReq := &unstructured.Unstructured{}
	if err := r.getResourceRequest(ctx, promiseGVK, healthRecord, resReq); err != nil {
		logger.Error(err, "Failed getting resource")
		return defaultRequeue, nil
	}

	return ctrl.Result{}, r.updateResourceStatus(ctx, resReq, healthRecord)
}

// SetupWithManager sets up the controller with the Manager.
func (r *HealthRecordReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.HealthRecord{}).
		Complete(r)
}

func (r *HealthRecordReconciler) updateResourceStatus(ctx context.Context, resReq *unstructured.Unstructured, healthRecord *platformv1alpha1.HealthRecord) error {
	if resReq.Object["status"] == nil {
		if err := unstructured.SetNestedMap(resReq.Object, map[string]interface{}{}, "status"); err != nil {
			return err
		}
	}

	healthData := map[string]interface{}{
		"state": healthRecord.Data.State,
	}

	if err := unstructured.SetNestedMap(resReq.Object, healthData, "status", "healthRecord"); err != nil {
		return err
	}

	return r.Status().Update(ctx, resReq)
}

func (r *HealthRecordReconciler) ignoreNotFound(logger logr.Logger, err error, msg string) (ctrl.Result, error) {
	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	logger.Error(err, msg)
	return defaultRequeue, nil
}

func (r *HealthRecordReconciler) getResourceRequest(ctx context.Context, gvk *schema.GroupVersionKind, healthRecord *platformv1alpha1.HealthRecord, resReq *unstructured.Unstructured) error {
	resReq.SetGroupVersionKind(*gvk)
	resRef := types.NamespacedName{
		Name:      healthRecord.Data.ResourceRef.Name,
		Namespace: healthRecord.Data.ResourceRef.Namespace,
	}
	return r.Get(ctx, resRef, resReq)
}
