package driver

import (
	"context"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RequestSpec describes the dynamic CR the rig will create N copies of.
// For the no-op Promise the GVK is perf.kratix.io/v1alpha1 PerfTest.
type RequestSpec struct {
	GVK       schema.GroupVersionKind
	Namespace string
	BaseName  string // requests are named <BaseName>-NNNNN
	Count     int
	Parallel  int // bounded concurrent creates
}

// CreateAll creates Count requests in parallel, capped at Parallel concurrent
// in-flight creates. Returns the names created and the first error if any
// creates failed.
func CreateAll(ctx context.Context, c client.Client, spec RequestSpec) ([]string, error) {
	if spec.Parallel <= 0 {
		spec.Parallel = 50
	}
	names := make([]string, spec.Count)
	for i := 0; i < spec.Count; i++ {
		names[i] = fmt.Sprintf("%s-%05d", spec.BaseName, i+1)
	}

	sem := make(chan struct{}, spec.Parallel)
	var wg sync.WaitGroup
	var (
		firstErr error
		errOnce  sync.Once
	)

	for _, name := range names {
		select {
		case <-ctx.Done():
			return names, ctx.Err()
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(n string) {
			defer wg.Done()
			defer func() { <-sem }()
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(spec.GVK)
			obj.SetNamespace(spec.Namespace)
			obj.SetName(n)
			_ = unstructured.SetNestedField(obj.Object, int64(1), "spec", "replicas")
			if err := c.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
				errOnce.Do(func() { firstErr = fmt.Errorf("create %s: %w", n, err) })
			}
		}(name)
	}
	wg.Wait()
	return names, firstErr
}

// DeleteAll deletes every request matching spec by name. Best effort; errors
// other than NotFound are returned.
func DeleteAll(ctx context.Context, c client.Client, spec RequestSpec) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(spec.GVK)
	if err := c.List(ctx, list, client.InNamespace(spec.Namespace)); err != nil {
		return err
	}
	sem := make(chan struct{}, spec.Parallel)
	var wg sync.WaitGroup
	var (
		firstErr error
		errOnce  sync.Once
	)
	for i := range list.Items {
		item := &list.Items[i]
		wg.Add(1)
		sem <- struct{}{}
		go func(o *unstructured.Unstructured) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := c.Delete(ctx, o); err != nil && !apierrors.IsNotFound(err) {
				errOnce.Do(func() { firstErr = err })
			}
		}(item)
	}
	wg.Wait()
	return firstErr
}

// EnsureNamespace creates the namespace if it does not yet exist.
func EnsureNamespace(ctx context.Context, c client.Client, name string) error {
	ns := &unstructured.Unstructured{}
	ns.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"})
	ns.SetName(name)
	err := c.Create(ctx, ns)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// suppress unused import lint when conditions helpers are added later
var _ = metav1.ConditionTrue
