package driver

import (
	"context"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

// ApplyPromise reads the YAML file at path and creates (or updates) the
// Promise. It blocks until the Promise reports Available=True or the context
// is cancelled.
func ApplyPromise(ctx context.Context, c client.Client, path string) (*platformv1alpha1.Promise, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read promise yaml: %w", err)
	}
	p := &platformv1alpha1.Promise{}
	if err := yaml.Unmarshal(raw, p); err != nil {
		return nil, fmt.Errorf("unmarshal promise yaml: %w", err)
	}

	existing := &platformv1alpha1.Promise{}
	err = c.Get(ctx, types.NamespacedName{Name: p.GetName()}, existing)
	switch {
	case apierrors.IsNotFound(err):
		if err := c.Create(ctx, p); err != nil {
			return nil, fmt.Errorf("create promise: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("get existing promise: %w", err)
	default:
		p.SetResourceVersion(existing.GetResourceVersion())
		if err := c.Update(ctx, p); err != nil {
			return nil, fmt.Errorf("update promise: %w", err)
		}
	}

	if err := WaitForPromiseAvailable(ctx, c, p.GetName(), 5*time.Minute); err != nil {
		return nil, err
	}
	return p, nil
}

func WaitForPromiseAvailable(ctx context.Context, c client.Client, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		p := &platformv1alpha1.Promise{}
		if err := c.Get(ctx, types.NamespacedName{Name: name}, p); err != nil {
			return fmt.Errorf("get promise %s: %w", name, err)
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == "Available" && cond.Status == metav1.ConditionTrue {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("promise %s did not become Available within %s", name, timeout)
}

// DeletePromise removes the Promise and blocks until it (and its cascading
// finalizer chain) is fully gone.
func DeletePromise(ctx context.Context, c client.Client, name string, timeout time.Duration) error {
	p := &platformv1alpha1.Promise{}
	if err := c.Get(ctx, types.NamespacedName{Name: name}, p); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := c.Delete(ctx, p); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := c.Get(ctx, types.NamespacedName{Name: name}, p)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil && !meta.IsNoMatchError(err) {
			return err
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("promise %s not deleted within %s", name, timeout)
}
