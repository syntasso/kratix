package resourceutil

import (
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
