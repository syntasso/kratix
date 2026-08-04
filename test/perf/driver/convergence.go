package driver

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Progress reports how many of the expected RRs have reached Reconciled=True.
type Progress struct {
	Total      int
	Reconciled int
	Elapsed    time.Duration
}

// WaitForReconciled polls the namespace listing the RR GVK until every named
// request has condition Reconciled=True. report is called every poll
// interval and at the end. The poll period is 2s.
func WaitForReconciled(
	ctx context.Context,
	c client.Client,
	gvk schema.GroupVersionKind,
	namespace string,
	expected []string,
	timeout time.Duration,
	report func(Progress),
) error {
	wantSet := make(map[string]struct{}, len(expected))
	for _, n := range expected {
		wantSet[n] = struct{}{}
	}
	start := time.Now()
	deadline := start.Add(timeout)
	period := 2 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		done, err := countReconciled(ctx, c, gvk, namespace, wantSet)
		if err != nil {
			return err
		}
		if report != nil {
			report(Progress{Total: len(expected), Reconciled: done, Elapsed: time.Since(start)})
		}
		if done >= len(expected) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("convergence timed out: %d/%d reconciled after %s",
				done, len(expected), time.Since(start).Truncate(time.Second))
		}
		time.Sleep(period)
	}
}

func countReconciled(
	ctx context.Context,
	c client.Client,
	gvk schema.GroupVersionKind,
	namespace string,
	want map[string]struct{},
) (int, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk)
	if err := c.List(ctx, list, client.InNamespace(namespace)); err != nil {
		return 0, fmt.Errorf("list rrs: %w", err)
	}
	done := 0
	for i := range list.Items {
		obj := &list.Items[i]
		if _, ok := want[obj.GetName()]; !ok {
			continue
		}
		if isReconciledTrue(obj) {
			done++
		}
	}
	return done, nil
}

func isReconciledTrue(obj *unstructured.Unstructured) bool {
	conds, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "Reconciled" && m["status"] == "True" {
			return true
		}
	}
	return false
}
