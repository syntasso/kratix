package resourceutil

import (
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	conditionsutil "sigs.k8s.io/cluster-api/util/conditions"
)

func GetCondition(obj *unstructured.Unstructured, conditionType clusterv1.ConditionType) *clusterv1.Condition {
	getter := conditionsutil.UnstructuredGetter(obj)
	condition := conditionsutil.Get(getter, conditionType)
	return condition
}

func HasCondition(obj *unstructured.Unstructured, conditionType clusterv1.ConditionType) bool {
	return GetCondition(obj, conditionType) != nil
}

func SetCondition(obj *unstructured.Unstructured, condition *clusterv1.Condition) {
	setter := conditionsutil.UnstructuredSetter(obj)
	conditionsutil.Set(setter, condition)
}

// SetConditionIfChanged writes condition onto obj when it differs in status, reason or
// message, and reports whether it wrote. Comparing status alone leaves a condition that
// stays Unknown reporting whichever reason it was first given.
func SetConditionIfChanged(obj *unstructured.Unstructured, condition *clusterv1.Condition) bool {
	existing := GetCondition(obj, condition.Type)
	if existing != nil &&
		existing.Status == condition.Status &&
		existing.Reason == condition.Reason &&
		existing.Message == condition.Message {
		return false
	}
	SetCondition(obj, condition)
	return true
}

// SetConditionKeepingTransitionTime writes condition when status, reason or message differ.
// Unlike conditionsutil.Set it keeps LastTransitionTime while the status is unchanged.
func SetConditionKeepingTransitionTime(obj *unstructured.Unstructured, condition *clusterv1.Condition) bool {
	setter := conditionsutil.UnstructuredSetter(obj)
	conditions := setter.GetConditions()
	index := -1
	for i := range conditions {
		if conditions[i].Type == condition.Type {
			index = i
		}
	}
	condition.LastTransitionTime = metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	if index >= 0 {
		existing := conditions[index]
		if existing.Status == condition.Status {
			if existing.Reason == condition.Reason && existing.Message == condition.Message {
				return false
			}
			condition.LastTransitionTime = existing.LastTransitionTime
		}
		conditions[index] = *condition
	} else {
		conditions = append(conditions, *condition)
	}
	setter.SetConditions(conditions)
	return true
}

func HasReconcilePausedCondition(obj *unstructured.Unstructured) bool {
	cond := GetCondition(obj, ReconciledCondition)
	return cond != nil && cond.Status == v1.ConditionUnknown && cond.Reason == pausedReconciliationReason
}
