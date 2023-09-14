package resourceutil

import (
	"fmt"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
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

func PipelineForPromiseExists(logger logr.Logger, promise v1alpha1.Promise, jobs []batchv1.Job) (bool, error) {
	if len(jobs) == 0 {
		return false, nil
	}

	// sort the pipepeineJobs by creation date
	sort.Slice(jobs, func(i, j int) bool {
		t1 := jobs[i].GetCreationTimestamp().Time
		t2 := jobs[j].GetCreationTimestamp().Time
		return t1.Before(t2)
	})

	mostRecentJob := jobs[len(jobs)-1]
	mostRecentHash := mostRecentJob.GetLabels()[pipeline.KratixResourceHashLabel]
	return mostRecentHash == fmt.Sprintf("%d", promise.GetGeneration()), nil
}

func PipelineForRequestExists(logger logr.Logger, rr *unstructured.Unstructured, jobs []batchv1.Job) (bool, error) {
	if len(jobs) == 0 {
		return false, nil
	}

	// sort the pipepeineJobs by creation date
	sort.Slice(jobs, func(i, j int) bool {
		t1 := jobs[i].GetCreationTimestamp().Time
		t2 := jobs[j].GetCreationTimestamp().Time
		return t1.Before(t2)
	})

	mostRecentJob := jobs[len(jobs)-1]

	mostRecentHash := mostRecentJob.GetLabels()[pipeline.KratixResourceHashLabel]
	currentRequestHash, err := hash.ComputeHash(rr)
	if err != nil {
		logger.Info("Cannot determine if the request is an update. Requeueing", "reason", err.Error())
		return false, nil
	}

	return mostRecentHash == currentRequestHash, nil
}

func IsThereAPipelineRunning(logger logr.Logger, jobs []batchv1.Job) bool {
	if len(jobs) == 0 {
		return false
	}

	var incompleteJobs bool
	for _, job := range jobs {
		for _, condition := range job.Status.Conditions {
			switch condition.Type {
			// TODO: deal with other conditions; what should kratix do if
			// the job is in a failed or suspended state?
			case batchv1.JobComplete:
				if condition.Status == v1.ConditionFalse {
					logger.Info("There's a pipeline still running, won't trigger a new pipeline")
					return true
				}
			default:
				incompleteJobs = true
			}
		}
	}

	return incompleteJobs
}
