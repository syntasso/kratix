// Package delivery lets a build register how a state store's writes reach their destination.
// Kratix prepares the change; a registered writer decides where it goes and when it is delivered.
package delivery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/writers"
)

// Outcome is how far a write has got once the writer has accepted it.
type Outcome string

const (
	Delivered Outcome = "Delivered"
	Pending   Outcome = "Pending"
	Rejected  Outcome = "Rejected"
)

// BuiltIn is the mode of a state store with no delivery block. It cannot be registered.
const BuiltIn = ""

// Factory builds the writer for a mode. base is the writer the state store would use on its own,
// so a mode can delegate the parts it does not change. creds is the state store's secret, which a
// mode needs when it talks to the host's API as well as to git.
type Factory func(base writers.StateStoreWriter, config map[string]string,
	creds map[string][]byte) (writers.StateStoreWriter, error)

// Result is how a write ended
type Result struct {
	Outcome Outcome
	// VersionID identifies the delivered content. Set only when Delivered.
	VersionID string
	// Receipt identifies the delivery to ask about later. Set only when Pending.
	Receipt string
	// Reason says why, for Rejected. It reaches the resource request.
	Reason string
}

// Writer is a StateStoreWriter whose writes may not be finished when they return. A writer that
// implements it also takes over readiness and the Destination's canary files, because neither
// can wait for a delivery that has not happened yet.
type Writer interface {
	writers.StateStoreWriter

	Deliver(ctx context.Context, subDir, workPlacementName string,
		create []v1alpha1.Workload, remove []string) (Result, error)

	Check(ctx context.Context, receipt string) (Result, error)
}

// Deferred reports whether this writer finishes its writes before returning.
func Deferred(writer writers.StateStoreWriter) (Writer, bool) {
	deferred, ok := writer.(Writer)
	return deferred, ok
}

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register panics rather than returning an error: it runs from init(), where there is no caller
// to handle one, and a silently dropped registration would leave writes going to the wrong place.
func Register(mode string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()

	if mode == BuiltIn {
		panic("delivery: cannot register the built-in mode")
	}
	if factory == nil {
		panic(fmt.Sprintf("delivery: nil factory registered for mode %q", mode))
	}
	if _, taken := registry[mode]; taken {
		panic(fmt.Sprintf("delivery: mode %q is already registered", mode))
	}
	registry[mode] = factory
}

// For returns nil for the built-in mode, so callers use the state store's own writer unchanged.
func For(mode string) (Factory, error) {
	mu.RLock()
	defer mu.RUnlock()

	if mode == BuiltIn {
		return nil, nil
	}
	factory, found := registry[mode]
	if !found {
		return nil, fmt.Errorf("no writer registered for delivery mode %q%s", mode, available())
	}
	return factory, nil
}

func available() string {
	if len(registry) == 0 {
		return "; this build registers none"
	}
	modes := make([]string, 0, len(registry))
	for mode := range registry {
		modes = append(modes, mode)
	}
	sort.Strings(modes)
	return "; this build registers " + strings.Join(modes, ", ")
}
