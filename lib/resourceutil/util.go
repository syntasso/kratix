package resourceutil

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/hash"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	ConfigureWorkflowCompletedCondition    = clusterv1.ConditionType("ConfigureWorkflowCompleted")
	ConfigureWorkflowCompletedFailedReason = "ConfigureWorkflowFailed"
	DeleteWorkflowCompletedCondition       = clusterv1.ConditionType("DeleteWorkflowCompleted")
	DeleteWorkflowCompletedFailedReason    = "DeleteWorkflowFailed"
	PipelinesExecutedSuccessfully          = "PipelinesExecutedSuccessfully"
	ManualReconciliationLabel              = "kratix.io/manual-reconciliation"
	ReconcileResourcesLabel                = "kratix.io/reconcile-resources"
	promiseAvailableCondition              = clusterv1.ConditionType("PromiseAvailable")
	promiseRequirementsNotMetReason        = "PromiseRequirementsNotInstalled"
	promiseRequirementsNotMetMessage       = "Promise Requirements are not installed"
	promiseRequirementsMetReason           = "PromiseAvailable"
	promiseRequirementsMetMessage          = "Promise Requirements are met"
	WorksSucceededCondition                = clusterv1.ConditionType("WorksSucceeded")
	ReconciledCondition                    = clusterv1.ConditionType("Reconciled")
	pausedReconciliationReason             = "PausedReconciliation"
)

func GetConfigureWorkflowCompletedConditionStatus(obj *unstructured.Unstructured) v1.ConditionStatus {
	condition := GetCondition(obj, ConfigureWorkflowCompletedCondition)
	if condition == nil {
		return v1.ConditionUnknown
	}
	return condition.Status
}

func MarkConfigureWorkflowAsRunning(logger logr.Logger, obj *unstructured.Unstructured) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               ConfigureWorkflowCompletedCondition,
		Status:             v1.ConditionFalse,
		Message:            "Pipelines are still in progress",
		Reason:             "PipelinesInProgress",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
	logging.Info(logger, "marking configure workflow as running", "condition", ConfigureWorkflowCompletedCondition, "value", v1.ConditionFalse, "reason", "PipelinesInProgress")
}

func MarkConfigureWorkflowAsFailed(logger logr.Logger, obj *unstructured.Unstructured, failedPipeline string) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               ConfigureWorkflowCompletedCondition,
		Status:             v1.ConditionFalse,
		Message:            fmt.Sprintf("A Configure Pipeline has failed: %s", failedPipeline),
		Reason:             ConfigureWorkflowCompletedFailedReason,
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
	logging.Warn(logger, "marking configure workflow as failed", "condition", ConfigureWorkflowCompletedCondition, "value", v1.ConditionFalse, "reason", ConfigureWorkflowCompletedFailedReason)
}

func MarkResourceRequestAsWorksFailed(obj *unstructured.Unstructured, works []string) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               WorksSucceededCondition,
		Status:             v1.ConditionFalse,
		Message:            fmt.Sprintf("Some works associated with this resource failed: [%s]", strings.Join(works, ",")),
		Reason:             "WorksFailing",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func MarkResourceRequestAsWorksMisplaced(obj *unstructured.Unstructured, works []string) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               WorksSucceededCondition,
		Status:             v1.ConditionFalse,
		Message:            fmt.Sprintf("Some works associated with this resource are misplaced: [%s]", strings.Join(works, ",")),
		Reason:             "WorksMisplaced",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func MarkResourceRequestAsWorksPending(obj *unstructured.Unstructured, works []string) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               WorksSucceededCondition,
		Status:             v1.ConditionUnknown,
		Message:            fmt.Sprintf("Some works associated with this resource are not ready: [%s]", strings.Join(works, ",")),
		Reason:             "WorksPending",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func MarkResourceRequestAsWorksSucceeded(obj *unstructured.Unstructured) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               WorksSucceededCondition,
		Status:             v1.ConditionTrue,
		Message:            "All works associated with this resource are ready",
		Reason:             "WorksSucceeded",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func MarkReconciledPending(obj *unstructured.Unstructured, reason string) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               ReconciledCondition,
		Status:             v1.ConditionUnknown,
		Message:            "Pending",
		Reason:             reason,
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func MarkReconciledFailing(obj *unstructured.Unstructured, reason string) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               ReconciledCondition,
		Status:             v1.ConditionFalse,
		Message:            "Failing",
		Reason:             reason,
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func MarkReconciledTrue(obj *unstructured.Unstructured) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               ReconciledCondition,
		Status:             v1.ConditionTrue,
		Message:            "Reconciled",
		Reason:             "Reconciled",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func MarkReconciledPaused(obj *unstructured.Unstructured) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               ReconciledCondition,
		Status:             v1.ConditionUnknown,
		Message:            "Paused",
		Reason:             pausedReconciliationReason,
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func MarkDeleteWorkflowAsFailed(logger logr.Logger, obj *unstructured.Unstructured) {
	condition := clusterv1.Condition{
		Type:               DeleteWorkflowCompletedCondition,
		Status:             v1.ConditionFalse,
		Message:            "The Delete Pipeline has failed",
		Reason:             DeleteWorkflowCompletedFailedReason,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}
	SetCondition(obj, &condition)
	logging.Warn(logger, "marking delete workflow as failed", "condition", condition.Type, "value", condition.Status, "reason", condition.Reason)
}

func SortJobsByCreationDateTime(jobs []batchv1.Job, desc bool) []batchv1.Job {
	sort.Slice(jobs, func(i, j int) bool {
		t1 := jobs[i].GetCreationTimestamp().Time
		t2 := jobs[j].GetCreationTimestamp().Time
		if desc {
			return t1.Before(t2)
		}
		return t1.After(t2)
	})
	return jobs
}

func PipelineWithDesiredSpecExists(logger logr.Logger, obj *unstructured.Unstructured, jobs []batchv1.Job) (*batchv1.Job, error) {
	if len(jobs) == 0 {
		return nil, nil
	}

	jobs = SortJobsByCreationDateTime(jobs, true)
	mostRecentJob := jobs[len(jobs)-1]

	mostRecentHash := mostRecentJob.GetLabels()[v1alpha1.KratixResourceHashLabel]
	currentRequestHash, err := hash.ComputeHashForResource(obj)
	if err != nil {
		logging.Warn(logger, "cannot determine if the request is an update; requeueing", "reason", err.Error())
		return nil, err
	}

	if mostRecentHash == currentRequestHash {
		return &mostRecentJob, nil
	}
	return nil, nil
}

func IsThereAPipelineRunning(logger logr.Logger, jobs []batchv1.Job) bool {
	if len(jobs) == 0 {
		return false
	}

	for _, job := range jobs {
		//A Job only has a condition after its finished or failed to run. No
		//conditions means its still active
		if len(job.Status.Conditions) == 0 {
			return true
		}

		// A job only ever has conditions: (none), Complete, Suspended, Failed, and FailureTarget
		//FailureTarget is not documented :shrug:
		// Complete, failed or suspended being true means nothing is running
		complete := hasCondition(job, batchv1.JobComplete, v1.ConditionTrue)
		suspended := hasCondition(job, batchv1.JobSuspended, v1.ConditionTrue)
		failed := hasCondition(job, batchv1.JobFailed, v1.ConditionTrue)
		if complete || suspended || failed {
			continue
		}
		return true
	}
	return false
}

func hasCondition(job batchv1.Job, conditionType batchv1.JobConditionType, conditionStatus v1.ConditionStatus) bool {
	for _, condition := range job.Status.Conditions {
		if condition.Type == conditionType && condition.Status == conditionStatus {
			return true
		}
	}
	return false
}

// If a job has no active pods we can suspend it
func SuspendablePipelines(logger logr.Logger, jobs []batchv1.Job) []batchv1.Job {
	if len(jobs) == 0 {
		return nil
	}

	jobsToSuspend := []batchv1.Job{}
	for _, job := range jobs {
		if job.Spec.Suspend != nil && *job.Spec.Suspend {
			continue
		}

		if job.Status.Active == 0 {
			jobsToSuspend = append(jobsToSuspend, job)
		}
	}

	return jobsToSuspend
}

// SetStatus takes in key value pairs in the statuses argument.
// Example: key1, value1, key2, value2, ...
// All keys must be castable to string. Values can be any type.
func SetStatus(rr *unstructured.Unstructured, logger logr.Logger, statuses ...interface{}) {
	if len(statuses) == 0 {
		return
	}

	if len(statuses)%2 != 0 {
		logging.Warn(logger, "invalid status; expecting key:value pair", "status", statuses)
		return
	}

	if rr.Object["status"] == nil {
		rr.Object["status"] = map[string]interface{}{}
	}

	nestedMap := rr.Object["status"].(map[string]interface{})
	for i := 0; i < len(statuses); i += 2 {
		key := statuses[i]
		// convert key to string
		keyStr, ok := key.(string)
		if !ok {
			logging.Warn(logger, "invalid status key; expecting string", "key", key)
			continue
		}
		value := statuses[i+1]
		nestedMap[keyStr] = value
	}

	// If there is no status, clean up empty status map
	if len(nestedMap) == 0 {
		delete(rr.Object, "status")
		return
	}

	err := unstructured.SetNestedMap(rr.Object, nestedMap, "status")

	if err != nil {
		logging.Warn(logger, "failed to set status; ignoring", "map", nestedMap)
	}
}

func GetStatus(rr *unstructured.Unstructured, key string) string {
	if rr.Object["status"] == nil {
		return ""
	}

	nestedMap := rr.Object["status"].(map[string]interface{})
	if nestedMap[key] == nil {
		return ""
	}

	return nestedMap[key].(string)
}

func GetWorkflowsCounterStatus(rr *unstructured.Unstructured, key string) int64 {
	if rr.Object["status"] == nil {
		return -1
	}

	nestedMap := rr.Object["status"].(map[string]interface{})
	value := nestedMap[key]
	if value == nil {
		return -1
	}

	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	}

	return -1
}

// GetObservedGeneration returns 0 when either status or status.observedGeneration is nil
func GetObservedGeneration(rr *unstructured.Unstructured) int64 {
	if rr.Object["status"] == nil {
		return 0
	}

	nestedMap := rr.Object["status"].(map[string]interface{})
	if nestedMap["observedGeneration"] == nil {
		return 0
	}

	return nestedMap["observedGeneration"].(int64)
}

func GetResourceNames(items []unstructured.Unstructured) []string {
	var names []string
	for _, item := range items {
		resource := item.GetName()
		//if the resource is destination scoped it has no namespace
		if item.GetNamespace() != "" {
			resource = fmt.Sprintf("%s/%s", item.GetNamespace(), item.GetName())
		}
		names = append(names, resource)
	}

	return names
}

func MarkPromiseConditionAsNotAvailable(obj *unstructured.Unstructured, logger logr.Logger) {
	SetStatus(obj, logger, "message", "Pending")

	condition := &clusterv1.Condition{
		Type:               promiseAvailableCondition,
		Status:             v1.ConditionFalse,
		Reason:             promiseRequirementsNotMetReason,
		Message:            promiseRequirementsNotMetMessage,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}

	SetCondition(obj, condition)
}

func MarkPromiseConditionAsAvailable(obj *unstructured.Unstructured, logger logr.Logger) {
	condition := &clusterv1.Condition{
		Type:               promiseAvailableCondition,
		Status:             v1.ConditionTrue,
		Reason:             promiseRequirementsMetReason,
		Message:            promiseRequirementsMetMessage,
		LastTransitionTime: metav1.NewTime(time.Now()),
	}

	SetCondition(obj, condition)
}

func IsPromiseMarkedAsUnavailable(obj *unstructured.Unstructured) bool {
	condition := GetCondition(obj, promiseAvailableCondition)
	if condition == nil {
		return false
	}

	return condition.Status == v1.ConditionFalse
}
