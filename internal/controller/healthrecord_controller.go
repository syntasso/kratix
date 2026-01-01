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

package controller

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const healthRecordCleanupFinalizer = v1alpha1.KratixPrefix + "health-record-cleanup"

// HealthRecordReconciler reconciles a HealthRecord object.
type HealthRecordReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	Log           logr.Logger
	EventRecorder record.EventRecorder
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=healthrecords,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=healthrecords/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=healthrecords/finalizers,verbs=update

func (r *HealthRecordReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	logger := r.Log.WithValues(
		"controller", "healthRecord",
		"name", req.Name,
		"namespace", req.Namespace,
	)

	healthRecord := &platformv1alpha1.HealthRecord{}
	if err := r.Get(ctx, req.NamespacedName, healthRecord); err != nil {
		return r.ignoreNotFound(logger, err, "failed getting health record"), nil
	}

	logger = logger.WithValues(
		"promise", healthRecord.Data.PromiseRef.Name,
		"resourceName", healthRecord.Data.ResourceRef.Name,
		"resourceNamespace", healthRecord.Data.ResourceRef.Namespace,
		"generation", healthRecord.GetGeneration(),
	)
	start := time.Now()
	logging.Info(logger, "reconciliation started")
	defer logReconcileDuration(logger, start, result, retErr)()

	promise := &platformv1alpha1.Promise{}
	promiseName := client.ObjectKey{Name: healthRecord.Data.PromiseRef.Name}
	if err := r.Get(ctx, promiseName, promise); err != nil {
		return r.ignoreNotFound(logger, err, "failed getting promise"), nil
	}

	promiseGVK, _, err := promise.GetAPI()
	if err != nil {
		logging.Error(logger, err, "failed getting promise gvk")
		return defaultRequeue, nil
	}

	resReq := &unstructured.Unstructured{}

	healthRecordIsBeingDeleted := !healthRecord.DeletionTimestamp.IsZero()

	if err = r.getResourceRequest(ctx, promiseGVK, healthRecord, resReq); err != nil {
		if healthRecordIsBeingDeleted && (errors.IsNotFound(err) || errors.IsForbidden(err)) {
			logging.Debug(r.Log, "resource not found or inaccessible during deletion; removing finalizer", "healthRecord", req.Name)
			return ctrl.Result{}, r.removeFinalizer(ctx, healthRecord)
		}
		logging.Error(logger, err, "failed getting resource")
		return defaultRequeue, nil
	}

	if healthRecordIsBeingDeleted {
		return r.deleteHealthRecord(ctx, healthRecord, resReq)
	}

	if !controllerutil.ContainsFinalizer(healthRecord, healthRecordCleanupFinalizer) {
		o := opts{client: r.Client, logger: logger, ctx: ctx}
		if err := addFinalizers(o, healthRecord, []string{healthRecordCleanupFinalizer}); err != nil {
			if kerrors.IsConflict(err) {
				return fastRequeue, nil
			}
			return ctrl.Result{}, err
		}
	}

	if err = r.updateResourceStatus(ctx, resReq, healthRecord, logger); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *HealthRecordReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.HealthRecord{}).
		Complete(r)
}

func (r *HealthRecordReconciler) updateResourceStatus(
	ctx context.Context, resReq *unstructured.Unstructured, healthRecord *platformv1alpha1.HealthRecord, logger logr.Logger,
) error {
	if resReq.Object["status"] == nil {
		if err := unstructured.SetNestedMap(resReq.Object, map[string]interface{}{}, "status"); err != nil {
			return err
		}
	}
	// Get all associated healthrecords
	healthRecords := &platformv1alpha1.HealthRecordList{}

	err := r.List(ctx, healthRecords)
	if err != nil {
		logging.Error(logger, err, "error listing health records")
		return err
	}

	var resourceHealthRecords []platformv1alpha1.HealthRecord
	for _, record := range healthRecords.Items {
		if record.Data.ResourceRef.Name == healthRecord.Data.ResourceRef.Name &&
			record.Data.ResourceRef.Namespace == healthRecord.Data.ResourceRef.Namespace {
			resourceHealthRecords = append(resourceHealthRecords, record)
		}
	}

	healthData, state, err := getHealthDataAndStates(resourceHealthRecords)
	if err != nil {
		return err
	}

	initialHealthStatusState := r.getInitialHealthStatusState(resReq)

	healthStatus := map[string]any{
		"state":         state,
		"healthRecords": healthData,
	}

	if err = unstructured.SetNestedMap(resReq.Object, healthStatus, "status", "healthStatus"); err != nil {
		return err
	}

	if initialHealthStatusState != healthRecord.Data.State {
		r.fireEvent(healthRecord, resReq)
	}

	return r.Status().Update(ctx, resReq)
}

func (r *HealthRecordReconciler) ignoreNotFound(logger logr.Logger, err error, msg string) ctrl.Result {
	if errors.IsNotFound(err) {
		return ctrl.Result{}
	}
	logging.Error(logger, err, msg)
	return defaultRequeue
}

func (r *HealthRecordReconciler) getResourceRequest(
	ctx context.Context,
	gvk *schema.GroupVersionKind,
	healthRecord *platformv1alpha1.HealthRecord,
	resReq *unstructured.Unstructured,
) error {
	resReq.SetGroupVersionKind(*gvk)
	resRef := types.NamespacedName{
		Name:      healthRecord.Data.ResourceRef.Name,
		Namespace: healthRecord.Data.ResourceRef.Namespace,
	}
	return r.Get(ctx, resRef, resReq)
}

func (r *HealthRecordReconciler) fireEvent(
	healthRecord *platformv1alpha1.HealthRecord,
	resReq *unstructured.Unstructured,
) {
	if healthRecord.Data.State != "healthy" && healthRecord.Data.State != "ready" {
		r.EventRecorder.Eventf(resReq, "Warning", "HealthRecord", "Health state is %s", healthRecord.Data.State)
	} else {
		r.EventRecorder.Eventf(resReq, "Normal", "HealthRecord", "Health state is %s", healthRecord.Data.State)
	}
}

func (r *HealthRecordReconciler) getInitialHealthStatusState(resReq *unstructured.Unstructured) string {
	status := resReq.Object["status"]
	var initialHealthStatusState = ""

	statusMap := status.(map[string]any)

	initialHealthData, ok := statusMap["healthStatus"]
	if ok {
		initialHealthStatusState, _ = initialHealthData.(map[string]any)["state"].(string)
	}

	return initialHealthStatusState
}

func getHealthDataAndStates(healthRecords []platformv1alpha1.HealthRecord) ([]any, string, error) {
	var healthData []any
	var statePriority = map[string]int{
		"unhealthy": 1,
		"degraded":  2,
		"unknown":   3,
		"healthy":   4,
		"ready":     5,
	}

	var state = "ready"
	for _, hr := range healthRecords {
		record := map[string]any{
			"state":   hr.Data.State,
			"lastRun": hr.Data.LastRun,
			"source": map[string]any{
				"name":      hr.GetName(),
				"namespace": hr.GetNamespace(),
			},
		}
		if hr.Data.Details != nil {
			var details interface{}
			if err := json.Unmarshal(hr.Data.Details.Raw, &details); err != nil {
				return nil, "", err
			}
			record["details"] = details
		}

		if statePriority[hr.Data.State] < statePriority[state] {
			state = hr.Data.State
		}
		healthData = append(healthData, record)
	}

	return healthData, state, nil
}

func (r *HealthRecordReconciler) deleteHealthRecord(
	ctx context.Context, healthRecord *platformv1alpha1.HealthRecord, resReq *unstructured.Unstructured,
) (ctrl.Result, error) {
	resourceHealthRecords := r.getResourceHealthRecords(resReq)
	var recordInResourceHealthRecords bool

	for _, record := range resourceHealthRecords {
		recordMap, hasRecordMap := record.(map[string]any)
		if !hasRecordMap {
			continue
		}

		source, hasSource := recordMap["source"].(map[string]any)
		if hasSource && source["name"] == healthRecord.GetName() {
			recordInResourceHealthRecords = true
		}
	}

	if !recordInResourceHealthRecords {
		return ctrl.Result{}, r.removeFinalizer(ctx, healthRecord)
	}

	var updatedHealthRecords []platformv1alpha1.HealthRecord

	healthRecords := &platformv1alpha1.HealthRecordList{}
	err := r.List(ctx, healthRecords)
	if err != nil {
		logging.Error(r.Log, err, "error listing health records")
		return defaultRequeue, err
	}

	for _, record := range healthRecords.Items {
		if record.GetName() != healthRecord.GetName() {
			logging.Debug(r.Log, "updating health records list", "item", record.GetName())

			updatedHealthRecords = append(updatedHealthRecords, record)
		}
	}

	healthData, state, err := getHealthDataAndStates(updatedHealthRecords)
	if err != nil {
		return defaultRequeue, err
	}

	healthStatus := map[string]any{
		"state":         state,
		"healthRecords": healthData,
	}

	if err := unstructured.SetNestedMap(resReq.Object, healthStatus, "status", "healthStatus"); err != nil {
		return defaultRequeue, err
	}

	err = r.Status().Update(ctx, resReq)
	if err != nil {
		return defaultRequeue, err
	}

	return defaultRequeue, nil
}

func (r *HealthRecordReconciler) removeFinalizer(ctx context.Context, healthRecord *platformv1alpha1.HealthRecord) error {
	controllerutil.RemoveFinalizer(healthRecord, healthRecordCleanupFinalizer)
	return r.Client.Update(ctx, healthRecord)
}

func (r *HealthRecordReconciler) getResourceHealthRecords(resReq *unstructured.Unstructured) []any {
	status := resReq.Object["status"]
	if status == nil {
		return nil
	}

	statusMap := status.(map[string]any)
	currentHealthStatus, hasHealthStatus := statusMap["healthStatus"].(map[string]any)
	if !hasHealthStatus {
		return nil
	}

	healthRecords, hasHealthRecords := currentHealthStatus["healthRecords"].([]any)
	if !hasHealthRecords {
		return nil
	}
	return healthRecords
}
