package v1alpha1

import (
	"context"
	"fmt"
	"sort"

	"github.com/syntasso/kratix/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const destinationDocsURL = "https://docs.kratix.io/main/reference/destinations/intro"

type stateStoreWithDelivery interface {
	client.Object
	GetDelivery() *v1alpha1.Delivery
}

// deliveryMode returns the state store's delivery mode, or "" when it has none.
func deliveryMode(stateStore stateStoreWithDelivery) string {
	if stateStore.GetDelivery() == nil {
		return ""
	}
	return stateStore.GetDelivery().Mode
}

// refuseDeliveryModeUnderAggregatedYAML refuses a delivery mode on a state store that an aggregatedYAML Destination references.
func refuseDeliveryModeUnderAggregatedYAML(ctx context.Context, c client.Client, kind string, stateStore stateStoreWithDelivery) error {
	if stateStore.GetDeletionTimestamp() != nil || deliveryMode(stateStore) == "" {
		return nil
	}

	destinations := &v1alpha1.DestinationList{}
	if err := c.List(ctx, destinations, client.MatchingFields{
		v1alpha1.DestinationStateStoreRefIndex: fmt.Sprintf("%s.%s", kind, stateStore.GetName()),
	}); err != nil {
		return fmt.Errorf("failed to list destinations: %w", err)
	}

	var names []string
	for _, destination := range destinations.Items {
		if destination.Spec.Filepath.Mode == v1alpha1.FilepathModeAggregatedYAML {
			names = append(names, destination.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}

	sort.Strings(names)
	return fmt.Errorf("delivery.mode cannot be set while Destination %q uses filepath.mode aggregatedYAML on this state store; see %s",
		names[0], destinationDocsURL)
}
