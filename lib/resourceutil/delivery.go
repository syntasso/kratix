package resourceutil

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	WorksAwaitingDeliveryReason = "WorksAwaitingDelivery"
	WorksRejectedReason         = "WorksRejected"

	reconciledDeliveryMessageMaxRunes = 120
	worksSucceededMessageMaxRunes     = 4096
)

// DeliveryEntry is a WorkPlacement whose delivery is waiting for approval or was rejected.
type DeliveryEntry struct {
	WorkPlacement string
	Destination   string
	Reason        string
	Message       string
}

// DeliveryEntries returns the WorkPlacements recorded on the given Works' status that are
// awaiting delivery or were rejected, sorted by WorkPlacement name.
func DeliveryEntries(works []v1alpha1.Work) []DeliveryEntry {
	var entries []DeliveryEntry
	for _, work := range works {
		for _, placement := range work.Status.WorkPlacementStatuses {
			if placement.Reason != v1alpha1.WorkloadsAwaitingDeliveryReason && placement.Reason != v1alpha1.WorkloadsRejectedReason {
				continue
			}
			entries = append(entries, DeliveryEntry{
				WorkPlacement: placement.Name,
				Destination:   placement.Destination,
				Reason:        placement.Reason,
				Message:       placement.Message,
			})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].WorkPlacement < entries[j].WorkPlacement })
	return entries
}

// IsOrdinaryWorksFailure reports whether failing Works are failing for a reason a delivery entry does not explain.
func IsOrdinaryWorksFailure(works []v1alpha1.Work, worksFailing bool, entries []DeliveryEntry) bool {
	if worksFailing && !slices.ContainsFunc(entries, func(entry DeliveryEntry) bool {
		return entry.Reason == v1alpha1.WorkloadsRejectedReason
	}) {
		return true
	}
	return slices.ContainsFunc(works, func(work v1alpha1.Work) bool {
		ready := apimeta.FindStatusCondition(work.Status.Conditions, "Ready")
		write := apimeta.FindStatusCondition(work.Status.Conditions, v1alpha1.WriteSucceededConditionType)
		return ready != nil && ready.Message == "Failing" &&
			write != nil && write.Status == metav1.ConditionFalse && write.Reason == v1alpha1.WorkplacementsFailingReason
	})
}

func MarkReconciledWorksPending(obj *unstructured.Unstructured, entries []DeliveryEntry) bool {
	if message, found := reconciledDeliveryMessage(entries, v1alpha1.WorkloadsAwaitingDeliveryReason); found {
		return markReconciled(obj, v1.ConditionUnknown, WorksAwaitingDeliveryReason, message)
	}
	return MarkReconciledPending(obj, "WorksPending")
}

// MarkReconciledWorksFailing shows a delivery text only when ordinaryFailure is false, so a
// rejection or a pending approval elsewhere never hides a Work that failed to write.
func MarkReconciledWorksFailing(obj *unstructured.Unstructured, entries []DeliveryEntry, ordinaryFailure bool) bool {
	if !ordinaryFailure {
		if message, found := reconciledDeliveryMessage(entries, v1alpha1.WorkloadsRejectedReason); found {
			return markReconciled(obj, v1.ConditionFalse, WorksRejectedReason, message)
		}
		if message, found := reconciledDeliveryMessage(entries, v1alpha1.WorkloadsAwaitingDeliveryReason); found {
			return markReconciled(obj, v1.ConditionUnknown, WorksAwaitingDeliveryReason, message)
		}
	}
	return MarkReconciledFailing(obj, "WorksFailing")
}

func markReconciled(obj *unstructured.Unstructured, status v1.ConditionStatus, reason, message string) bool {
	return SetConditionIfChanged(obj, &clusterv1.Condition{
		Type:               ReconciledCondition,
		Status:             status,
		Message:            message,
		Reason:             reason,
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func reconciledDeliveryMessage(entries []DeliveryEntry, workPlacementReason string) (string, bool) {
	var matching []DeliveryEntry
	for _, entry := range entries {
		if entry.Reason == workPlacementReason {
			matching = append(matching, entry)
		}
	}
	if len(matching) == 0 {
		return "", false
	}
	suffix := ""
	if len(matching) > 1 {
		suffix = fmt.Sprintf(" (+%d more)", len(matching)-1)
	}
	return CutToRunes(matching[0].Message, reconciledDeliveryMessageMaxRunes-len([]rune(suffix))) + suffix, true
}

// CutToRunes cuts text to maxRunes characters, ending in "…", without splitting a multi-byte character.
func CutToRunes(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes-1]) + "…"
}

func worksMessage(summary string, works []string, entries []DeliveryEntry) string {
	var message strings.Builder
	fmt.Fprintf(&message, "%s: [%s]", summary, strings.Join(works, ","))
	for _, entry := range entries {
		fmt.Fprintf(&message, ". Destination %s: %s", entry.Destination, entry.Message)
	}
	return CutToRunes(message.String(), worksSucceededMessageMaxRunes)
}
