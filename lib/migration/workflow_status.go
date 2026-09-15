package migration

import (
	"context"
	"fmt"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PromiseWorkflowStatus moves the pipelines an older Kratix recorded under
// status.kratix.workflows into the configure workflow's own record. Until it
// has run, such a Promise cannot be decoded as a Promise, so the webhook
// rejects every write to it: it must run before the manager starts.
func PromiseWorkflowStatus(ctx context.Context, c client.Client) error {
	promises := &unstructured.UnstructuredList{}
	promises.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("PromiseList"))
	if err := c.List(ctx, promises); err != nil {
		return fmt.Errorf("failed to list promises: %w", err)
	}

	for i := range promises.Items {
		promise := &promises.Items[i]
		moved, err := MovePromiseWorkflowRecord(promise)
		if err != nil {
			return fmt.Errorf("failed to migrate promise %s: %w", promise.GetName(), err)
		}
		if !moved {
			continue
		}
		if err := c.Status().Update(ctx, promise); err != nil {
			return fmt.Errorf("failed to migrate promise %s: %w", promise.GetName(), err)
		}
	}

	return nil
}

func MovePromiseWorkflowRecord(promise *unstructured.Unstructured) (bool, error) {
	workflows, found, err := unstructured.NestedMap(promise.Object, "status", "kratix", "workflows")
	if err != nil || !found {
		return false, err
	}

	pipelines, found := workflows["pipelines"]
	if !found {
		return false, nil
	}

	configure := map[string]any{"pipelines": pipelines}
	if generation, found := workflows["suspendedGeneration"]; found {
		configure["suspendedGeneration"] = generation
	}
	workflows[string(v1alpha1.WorkflowActionConfigure)] = configure
	delete(workflows, "pipelines")
	delete(workflows, "suspendedGeneration")

	return true, unstructured.SetNestedMap(promise.Object, workflows, "status", "kratix", "workflows")
}
