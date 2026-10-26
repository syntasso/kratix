package resourceutil

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// ListDeliveryEntries returns the WorkPlacements of the given Works that are awaiting
// delivery or were rejected, and are not being deleted, sorted by WorkPlacement name.
func ListDeliveryEntries(ctx context.Context, c client.Client, works []v1alpha1.Work) ([]DeliveryEntry, error) {
	var entries []DeliveryEntry
	for _, work := range works {
		var placements v1alpha1.WorkPlacementList
		if err := c.List(ctx, &placements,
			client.InNamespace(work.GetNamespace()),
			client.MatchingLabels{v1alpha1.WorkLabel: work.GetName()},
		); err != nil {
			return nil, fmt.Errorf("listing WorkPlacements of Work %s/%s: %w", work.GetNamespace(), work.GetName(), err)
		}
		for _, placement := range placements.Items {
			if !placement.GetDeletionTimestamp().IsZero() {
				continue
			}
			write := apimeta.FindStatusCondition(placement.Status.Conditions, v1alpha1.WriteSucceededConditionType)
			if write == nil || (write.Reason != v1alpha1.WorkloadsAwaitingDeliveryReason && write.Reason != v1alpha1.WorkloadsRejectedReason) {
				continue
			}
			entries = append(entries, DeliveryEntry{
				WorkPlacement: placement.GetName(),
				Destination:   placement.Spec.TargetDestinationName,
				Reason:        write.Reason,
				Message:       write.Message,
			})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].WorkPlacement < entries[j].WorkPlacement })
	return entries, nil
}

func MarkReconciledWorksPending(obj *unstructured.Unstructured, entries []DeliveryEntry) bool {
	if message, found := reconciledDeliveryMessage(entries, v1alpha1.WorkloadsAwaitingDeliveryReason); found {
		return markReconciled(obj, v1.ConditionUnknown, WorksAwaitingDeliveryReason, message)
	}
	return MarkReconciledPending(obj, "WorksPending")
}

func MarkReconciledWorksFailing(obj *unstructured.Unstructured, entries []DeliveryEntry) bool {
	if message, found := reconciledDeliveryMessage(entries, v1alpha1.WorkloadsRejectedReason); found {
		return markReconciled(obj, v1.ConditionFalse, WorksRejectedReason, message)
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
	return cutToRunes(matching[0].Message, reconciledDeliveryMessageMaxRunes-len([]rune(suffix))) + suffix, true
}

func cutToRunes(text string, maxRunes int) string {
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
	return cutToRunes(message.String(), worksSucceededMessageMaxRunes)
}
