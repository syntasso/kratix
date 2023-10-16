package resourceutil

import (
	"fmt"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/lib/hash"
	"github.com/syntasso/kratix/lib/pipeline"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	PipelineCompletedCondition = clusterv1.ConditionType("PipelineCompleted")
	ManualReconciliationLabel  = "kratix.io/manual-reconciliation"
)

func GetPipelineCompletedConditionStatus(obj *unstructured.Unstructured) v1.ConditionStatus {
	condition := GetCondition(obj, PipelineCompletedCondition)
	if condition == nil {
		return v1.ConditionUnknown
	}
	return condition.Status
}

func MarkPipelineAsRunning(logger logr.Logger, obj *unstructured.Unstructured) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               PipelineCompletedCondition,
		Status:             v1.ConditionFalse,
		Message:            "Pipeline has not completed",
		Reason:             "PipelineNotCompleted",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
	logger.Info("set conditions", "condition", PipelineCompletedCondition, "value", v1.ConditionFalse)
}

func MarkPipelineAsCompleted(logger logr.Logger, obj *unstructured.Unstructured) {
	SetCondition(obj, &clusterv1.Condition{
		Type:               PipelineCompletedCondition,
		Status:             v1.ConditionTrue,
		Message:            "Pipeline completed",
		Reason:             "PipelineExecutedSuccessfully",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
	logger.Info("set conditions", "condition", PipelineCompletedCondition, "value", v1.ConditionTrue)
}

func PipelineWithDesiredSpecExists(logger logr.Logger, obj *unstructured.Unstructured, jobs []batchv1.Job) (*batchv1.Job, error) {
	if len(jobs) == 0 {
		return nil, nil
	}

	// sort the pipepeineJobs by creation date
	sort.Slice(jobs, func(i, j int) bool {
		t1 := jobs[i].GetCreationTimestamp().Time
		t2 := jobs[j].GetCreationTimestamp().Time
		return t1.Before(t2)
	})

	mostRecentJob := jobs[len(jobs)-1]

	mostRecentHash := mostRecentJob.GetLabels()[pipeline.KratixResourceHashLabel]
	currentRequestHash, err := hash.ComputeHash(obj)
	if err != nil {
		logger.Info("Cannot determine if the request is an update. Requeueing", "reason", err.Error())
		return nil, nil
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

func SetStatus(rr *unstructured.Unstructured, logger logr.Logger, statuses ...string) {
	if len(statuses) == 0 {
		return
	}

	if len(statuses)%2 != 0 {
		logger.Info("invalid status; expecting key:value pair", "status", statuses)
		return
	}

	if rr.Object["status"] == nil {
		rr.Object["status"] = map[string]interface{}{}
	}
	nestedMap := rr.Object["status"].(map[string]interface{})
	for i := 0; i < len(statuses); i += 2 {
		key := statuses[i]
		value := statuses[i+1]
		nestedMap[key] = value
	}

	err := unstructured.SetNestedMap(rr.Object, nestedMap, "status")

	if err != nil {
		logger.Info("failed to set status; ignoring", "map", nestedMap)
	}
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
