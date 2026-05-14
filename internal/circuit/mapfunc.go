package circuit

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// MapFunc wraps inner so that emitted reconcile.Requests are filtered through breaker.
// If breaker is nil, inner is returned unchanged.
func MapFunc(inner handler.MapFunc, breaker Breaker) handler.MapFunc {
	if breaker == nil {
		return inner
	}
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		out := inner(ctx, obj)
		if len(out) == 0 {
			return out
		}
		kept := out[:0]
		for _, req := range out {
			if breaker.Allow(req.NamespacedName) {
				kept = append(kept, req)
			}
		}
		return kept
	}
}

// AllowKey is exported for callers that already have a namespaced key (e.g.
// the primary For() predicate path) and want to consult the breaker directly.
func AllowKey(breaker Breaker, key types.NamespacedName) bool {
	if breaker == nil {
		return true
	}
	return breaker.Allow(key)
}
