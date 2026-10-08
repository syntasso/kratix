package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/healthdefinition"
	"github.com/syntasso/kratix/lib/resourceutil"
)

// reconcileExpectedHealth writes expectedRecords and the HealthChecksSucceeded condition
// onto rr when it expects records at a promise version; reports whether anything changed.
func reconcileExpectedHealth(
	ctx context.Context, c client.Client, logger logr.Logger, promise *v1alpha1.Promise, rr *unstructured.Unstructured,
	records []v1alpha1.HealthRecord,
) (bool, error) {
	expectedVersion, _, _ := unstructured.NestedString(rr.Object, "status", "healthStatus", "expectedPromiseVersion")
	if expectedVersion == "" {
		return false, nil
	}
	healthDefinitions, _, _ := unstructured.NestedInt64(rr.Object, "status", "healthStatus", "healthDefinitions")

	expectedRecords, err := expectedHealthRecords(ctx, c, logger, promise, rr, expectedVersion, healthDefinitions)
	if err != nil {
		return false, err
	}

	changed := false
	current, found, _ := unstructured.NestedInt64(rr.Object, "status", "healthStatus", "expectedRecords")
	if !found || current != expectedRecords {
		if err := unstructured.SetNestedField(rr.Object, expectedRecords, "status", "healthStatus", "expectedRecords"); err != nil {
			return false, err
		}
		changed = true
	}

	condition := healthChecksCondition(records, rr.GetNamespace(), expectedVersion, healthDefinitions, expectedRecords)
	if resourceutil.SetConditionWithTransitionTime(rr, &condition) {
		changed = true
	}
	return changed, nil
}

func resourceWorksNamespaceAndLabels(promise *v1alpha1.Promise, rr *unstructured.Unstructured) (string, map[string]string) {
	namespace, resourceNamespace := rr.GetNamespace(), ""
	if promise.WorkflowPipelineNamespaceSet() {
		namespace, resourceNamespace = promise.WorkflowPipelineNamespace(), rr.GetNamespace()
	}
	return namespace, resourceutil.GetWorkLabels(promise.GetName(), rr.GetName(), resourceNamespace, "", v1alpha1.WorkTypeResource)
}

// expectedHealthRecords counts the records a resource should receive at expectedVersion:
// per workload group, its HealthDefinitions times the destinations it is placed on.
func expectedHealthRecords(
	ctx context.Context, c client.Client, logger logr.Logger, promise *v1alpha1.Promise, rr *unstructured.Unstructured,
	expectedVersion string, healthDefinitions int64,
) (int64, error) {
	if healthDefinitions == 0 {
		return 0, nil
	}

	works, err := resourceWorks(ctx, c, promise.GetName(), rr)
	if err != nil {
		return 0, err
	}

	var expected int64
	for i := range works {
		work := &works[i]
		if !work.DeletionTimestamp.IsZero() || work.GetLabels()[v1alpha1.DryRunLabel] == "true" ||
			work.GetAnnotations()[v1alpha1.HealthDefinitionsVersionAnnotation] != expectedVersion {
			continue
		}
		for _, group := range work.Spec.WorkloadGroups {
			count := groupHealthDefinitions(logger, work, group)
			if count == 0 {
				continue
			}
			placements, err := countWorkPlacements(ctx, c, work, group.ID)
			if err != nil {
				return 0, err
			}
			expected += count * placements
		}
	}
	return expected, nil
}

// resourceWorks finds the resource's Works wherever a promise revision wrote them, so a
// changed pipelineNamespace does not hide Works written under the old one.
func resourceWorks(ctx context.Context, c client.Client, promiseName string, rr *unstructured.Unstructured) ([]v1alpha1.Work, error) {
	works := &v1alpha1.WorkList{}
	workLabels := resourceutil.GetWorkLabels(promiseName, rr.GetName(), "", "", v1alpha1.WorkTypeResource)
	if err := c.List(ctx, works, client.MatchingLabels(workLabels)); err != nil {
		return nil, err
	}
	var owned []v1alpha1.Work
	for _, work := range works.Items {
		if workResourceNamespace(&work) == rr.GetNamespace() {
			owned = append(owned, work)
		}
	}
	return owned, nil
}

// workResourceNamespace is the namespace of the resource a Work belongs to; only Works in a
// pipeline namespace record it in a label.
func workResourceNamespace(work *v1alpha1.Work) string {
	if namespace := work.GetLabels()[v1alpha1.ResourceNamespaceLabel]; namespace != "" {
		return namespace
	}
	return work.GetNamespace()
}

// groupHealthDefinitions trusts the Work annotation when the Work has a single group, since
// the annotation is the total across groups. Malformed input counts 0 rather than blocking.
func groupHealthDefinitions(logger logr.Logger, work *v1alpha1.Work, group v1alpha1.WorkloadGroup) int64 {
	if len(work.Spec.WorkloadGroups) == 1 {
		count, err := strconv.ParseInt(work.GetAnnotations()[v1alpha1.HealthDefinitionsAnnotation], 10, 64)
		if err != nil {
			logging.Warn(logger, "ignoring invalid health-definitions annotation", "work", work.GetName(), "error", err.Error())
			return 0
		}
		return count
	}

	var count int64
	for _, workload := range group.Workloads {
		content, err := compression.DecompressContent([]byte(workload.Content))
		if err != nil {
			logging.Warn(logger, "ignoring workload that failed to decompress",
				"work", work.GetName(), "filepath", workload.Filepath, "error", err.Error())
			continue
		}
		count += int64(healthdefinition.Count(content))
	}
	return count
}

func countWorkPlacements(ctx context.Context, c client.Client, work *v1alpha1.Work, groupID string) (int64, error) {
	placements := &v1alpha1.WorkPlacementList{}
	err := c.List(ctx, placements, client.InNamespace(work.GetNamespace()), client.MatchingLabels{
		workLabelKey:       work.GetName(),
		workloadGroupIDKey: groupID,
	})
	if err != nil {
		return 0, err
	}
	var count int64
	for i := range placements.Items {
		if placements.Items[i].DeletionTimestamp.IsZero() {
			count++
		}
	}
	return count, nil
}

// healthChecksCondition judges only records in the resource's namespace (D2); state and
// healthRecords stay cluster-wide, so the two can legitimately disagree.
func healthChecksCondition(
	records []v1alpha1.HealthRecord, namespace, expectedVersion string, healthDefinitions, expectedRecords int64,
) clusterv1.Condition {
	condition := func(status v1.ConditionStatus, reason, message string) clusterv1.Condition {
		return clusterv1.Condition{
			Type: resourceutil.HealthChecksSucceededCondition, Status: status, Reason: reason, Message: message,
		}
	}
	if healthDefinitions == 0 {
		return condition(v1.ConditionTrue, resourceutil.HealthChecksNoHealthChecksReason,
			fmt.Sprintf("%s ships no health checks", expectedVersion))
	}

	var reported, unhealthy, degraded int64
	for i := range records {
		record := &records[i]
		if record.Data.PromiseVersion != expectedVersion || !record.DeletionTimestamp.IsZero() || record.GetNamespace() != namespace {
			continue
		}
		switch record.Data.State {
		case "unknown":
			continue
		case "unhealthy":
			unhealthy++
		case "degraded":
			degraded++
		}
		reported++
	}

	switch {
	// Nothing placed means nothing to judge, even if a record is already unhealthy (ADR0016).
	case expectedRecords == 0:
		return condition(v1.ConditionUnknown, resourceutil.HealthChecksWaitingForRecordsReason,
			fmt.Sprintf("health checks for %s have not been placed on a destination yet", expectedVersion))
	case unhealthy > 0:
		return condition(v1.ConditionFalse, resourceutil.HealthChecksUnhealthyReason,
			recordsInState(unhealthy, expectedRecords, expectedVersion, "unhealthy"))
	case reported < expectedRecords:
		return condition(v1.ConditionUnknown, resourceutil.HealthChecksWaitingForRecordsReason,
			recordsReported(reported, expectedRecords, expectedVersion))
	case degraded > 0:
		return condition(v1.ConditionFalse, resourceutil.HealthChecksDegradedReason,
			recordsInState(degraded, expectedRecords, expectedVersion, "degraded"))
	default:
		return condition(v1.ConditionTrue, resourceutil.HealthChecksAllRecordsHealthyReason,
			recordsReported(reported, expectedRecords, expectedVersion))
	}
}

func recordsReported(reported, expected int64, version string) string {
	return fmt.Sprintf("%d of %d records have reported at %s", reported, expected, version)
}

func recordsInState(count, expected int64, version, state string) string {
	verb := "are"
	if count == 1 {
		verb = "is"
	}
	return fmt.Sprintf("%d of %d records at %s %s %s", count, expected, version, verb, state)
}

// syncResourceBindingHealth copies the resource's health summary and HealthChecksSucceeded
// condition onto its ResourceBinding; a missing binding is not an error.
func syncResourceBindingHealth(
	ctx context.Context, c client.Client, logger logr.Logger, promiseName string, rr *unstructured.Unstructured,
) error {
	binding, err := findResourceBinding(ctx, c, rr.GetNamespace(), rr.GetName(), promiseName)
	if errors.Is(err, errResourceBindingNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	desiredStatus := resourceBindingHealthStatus(rr)
	desiredCondition := resourceBindingHealthCondition(rr)
	conditionType := string(resourceutil.HealthChecksSucceededCondition)
	existingCondition := apiMeta.FindStatusCondition(binding.Status.Conditions, conditionType)
	if reflect.DeepEqual(binding.Status.HealthStatus, desiredStatus) && sameCondition(existingCondition, desiredCondition) {
		return nil
	}

	binding.Status.HealthStatus = desiredStatus
	apiMeta.RemoveStatusCondition(&binding.Status.Conditions, conditionType)
	if desiredCondition != nil {
		binding.Status.Conditions = append(binding.Status.Conditions, *desiredCondition)
	}
	logging.Debug(logger, "updating resource binding health", "binding", binding.GetName())
	return c.Status().Update(ctx, binding)
}

func resourceBindingHealthStatus(rr *unstructured.Unstructured) *v1alpha1.ResourceBindingHealthStatus {
	state, foundState, _ := unstructured.NestedString(rr.Object, "status", "healthStatus", "state")
	version, foundVersion, _ := unstructured.NestedString(rr.Object, "status", "healthStatus", "expectedPromiseVersion")
	expectedRecords, foundRecords, _ := unstructured.NestedInt64(rr.Object, "status", "healthStatus", "expectedRecords")
	if !foundState && !foundVersion && !foundRecords {
		return nil
	}
	status := &v1alpha1.ResourceBindingHealthStatus{State: state, ExpectedPromiseVersion: version}
	if foundRecords {
		status.ExpectedRecords = &expectedRecords
	}
	return status
}

func resourceBindingHealthCondition(rr *unstructured.Unstructured) *metav1.Condition {
	condition := resourceutil.GetCondition(rr, resourceutil.HealthChecksSucceededCondition)
	if condition == nil {
		return nil
	}
	return &metav1.Condition{
		Type:               string(condition.Type),
		Status:             metav1.ConditionStatus(condition.Status),
		Reason:             condition.Reason,
		Message:            condition.Message,
		LastTransitionTime: condition.LastTransitionTime,
	}
}

// sameCondition compares the fields the controller writes; LastTransitionTime by instant,
// since the two sides come from different serialisations.
func sameCondition(a, b *metav1.Condition) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Type == b.Type && a.Status == b.Status && a.Reason == b.Reason && a.Message == b.Message &&
		a.LastTransitionTime.Equal(&b.LastTransitionTime)
}
