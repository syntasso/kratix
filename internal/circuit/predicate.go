package circuit

import (
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// Predicate returns a predicate that consults breaker.Allow for Create/Update/Generic
// events. Delete events always pass so that cleanup is never blocked.
func Predicate(breaker Breaker) predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return AllowKey(breaker, types.NamespacedName{
				Namespace: e.Object.GetNamespace(),
				Name:      e.Object.GetName(),
			})
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return AllowKey(breaker, types.NamespacedName{
				Namespace: e.ObjectNew.GetNamespace(),
				Name:      e.ObjectNew.GetName(),
			})
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return AllowKey(breaker, types.NamespacedName{
				Namespace: e.Object.GetNamespace(),
				Name:      e.Object.GetName(),
			})
		},
		DeleteFunc: func(_ event.DeleteEvent) bool { return true },
	}
}
