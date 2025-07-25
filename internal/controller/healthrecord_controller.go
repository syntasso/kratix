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
	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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

func (r *HealthRecordReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	tracer := otel.Tracer("kratix")
	ctx, span := tracer.Start(ctx, "Reconcile/HealthRecord")
	defer span.End()
	span.SetAttributes(
		attribute.String("req.name", req.Name),
		attribute.String("req.namespace", req.Namespace),
	)

	logger := r.Log.WithValues("healthRecord", req.Name, "namespace", req.Namespace)
	logger.Info("Reconciling HealthRecord")

	healthRecord := &platformv1alpha1.HealthRecord{}
	if err := r.Get(ctx, req.NamespacedName, healthRecord); err != nil {
		return r.ignoreNotFound(logger, err, "Failed getting healthRecord")
	}
	span.AddEvent("fetched HealthRecord")

	logger = logger.WithValues(
		"promiseRef", healthRecord.Data.PromiseRef,
		"resourceRef", healthRecord.Data.ResourceRef,
	)

	promise := &platformv1alpha1.Promise{}
	promiseName := client.ObjectKey{Name: healthRecord.Data.PromiseRef.Name}
	if err := r.Get(ctx, promiseName, promise); err != nil {
		return r.ignoreNotFound(logger, err, "Failed getting Promise")
	}
	span.AddEvent("fetched Promise")

	promiseGVK, _, err := promise.GetAPI()
	if err != nil {
		logger.Error(err, "Failed getting promise gvk")
		return defaultRequeue, nil
	}

	resReq := &unstructured.Unstructured{}
	if err = r.getResourceRequest(ctx, promiseGVK, healthRecord, resReq); err != nil {
		logger.Error(err, "Failed getting resource")
		return defaultRequeue, nil
	}

	if !healthRecord.DeletionTimestamp.IsZero() {
		return r.deleteHealthRecord(ctx, healthRecord, resReq)
	}

	if !controllerutil.ContainsFinalizer(healthRecord, healthRecordCleanupFinalizer) {
		return addFinalizers(opts{
			client: r.Client,
			logger: logger,
			ctx:    ctx,
		}, healthRecord, []string{healthRecordCleanupFinalizer})
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
		logger.Error(err, "error listing healthRecords")
		return err
	}

	healthData, state, err := getHealthDataAndStates(healthRecords.Items)
	if err != nil {
		return err
	}

	initialHealthStatusState := r.getInitialHealthStatusState(resReq)

	healthStatus := map[string]any{
		"state":         state,
		"healthRecords": healthData,
	}

	if err := unstructured.SetNestedMap(resReq.Object, healthStatus, "status", "healthStatus"); err != nil {
		return err
	}

	if initialHealthStatusState != healthRecord.Data.State {
		r.fireEvent(healthRecord, resReq)
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

	statusMap, ok := status.(map[string]interface{})
	if !ok {
		r.Log.Info(
			"error getting status of resource request", "name", resReq.GetName(), "namespace", resReq.GetNamespace(),
		)
	}

	initialHealthData, ok := statusMap["healthStatus"]
	if ok {
		initialHealthStatusState, _ = initialHealthData.(map[string]interface{})["state"].(string)
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

func (r *HealthRecordReconciler) deleteHealthRecord(ctx context.Context, healthRecord *platformv1alpha1.HealthRecord, resReq *unstructured.Unstructured) (ctrl.Result, error) {
	resourceHealthRecords := r.getResourceHealthRecords(resReq)
	var recordInResourceHealthRecords bool

	for _, record := range resourceHealthRecords {
		recordMap, ok := record.(map[string]any)
		if !ok {
			r.Log.Info(
				"error parsing health record for resource request", "name", resReq.GetName(), "namespace", resReq.GetNamespace(),
			)
		}

		r.Log.Info(fmt.Sprintf("checking recordMap: %+v", recordMap))
		source, ok := recordMap["source"].(map[string]any)
		if !ok {
			r.Log.Info(
				"error parsing source in health record for resource request", "name",
				resReq.GetName(),
				"namespace", resReq.GetNamespace(),
			)
		}

		if source["name"] == healthRecord.GetName() {
			recordInResourceHealthRecords = true
		}
	}

	if !recordInResourceHealthRecords {
		r.Log.Info("health record not found in resource state health records")

		controllerutil.RemoveFinalizer(healthRecord, healthRecordCleanupFinalizer)
		err := r.Client.Update(ctx, healthRecord)
		if err != nil {
			return defaultRequeue, err
		}
	}

	var updatedHealthRecords []platformv1alpha1.HealthRecord
	r.Log.Info("getting health records")

	healthRecords := &platformv1alpha1.HealthRecordList{}
	err := r.List(ctx, healthRecords)
	if err != nil {
		r.Log.Error(err, "error listing healthRecords")
		return defaultRequeue, err
	}

	for _, record := range healthRecords.Items {
		if record.GetName() != healthRecord.GetName() {
			r.Log.Info("updating health records list", "item", record.GetName())

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

func (r *HealthRecordReconciler) getResourceHealthRecords(resReq *unstructured.Unstructured) []any {
	status := resReq.Object["status"]
	statusMap, ok := status.(map[string]interface{})
	if !ok {
		r.Log.Info(
			"error getting status of resource request", "name", resReq.GetName(), "namespace", resReq.GetNamespace(),
		)
	}
	currentHealthStatus, ok := statusMap["healthStatus"].(map[string]any)
	if !ok {
		r.Log.Info(
			"error getting health status of resource request", "name", resReq.GetName(), "namespace", resReq.GetNamespace(),
		)
	}
	healthRecords, ok := currentHealthStatus["healthRecords"].([]any)
	if !ok {
		r.Log.Info(
			"error getting health records in resource request status", "name", resReq.GetName(), "namespace", resReq.GetNamespace(),
		)
	}
	return healthRecords
}
