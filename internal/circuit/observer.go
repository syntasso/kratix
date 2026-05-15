package circuit

import (
	"k8s.io/apimachinery/pkg/types"
)

// StateObserver is notified after every breaker state transition.
// Implementations must be safe for concurrent use; the breaker may call
// OnTransition from multiple goroutines.
//
// Observers must NOT block and must NOT re-enter Breaker methods — the
// breaker holds its internal mutex when invoking the observer, so a re-entrant
// call would deadlock.
type StateObserver interface {
	OnTransition(key types.NamespacedName, old, new State)
}

// StateObserverFunc adapts a plain function to the StateObserver interface.
type StateObserverFunc func(key types.NamespacedName, old, new State)

// OnTransition satisfies the StateObserver interface.
func (f StateObserverFunc) OnTransition(key types.NamespacedName, old, new State) {
	f(key, old, new)
}

// noopObserver is the zero-value default; never panics.
type noopObserver struct{}

func (noopObserver) OnTransition(types.NamespacedName, State, State) {}

// ObservableBreaker is the public surface for Breakers that support attaching
// a StateObserver. NewTokenBucketBreaker returns this interface so existing
// callers using a `Breaker`-typed variable continue to compile, while new
// callers that want observability can call .WithObserver(...) on the result.
type ObservableBreaker interface {
	Breaker
	WithObserver(StateObserver) ObservableBreaker
}
