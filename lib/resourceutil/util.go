package resourceutil

import (
	"time"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const PipelineCompletedCondition = clusterv1.ConditionType("PipelineCompleted")

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
