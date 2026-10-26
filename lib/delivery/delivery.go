// Package delivery lets a build register how a state store's writes reach their destination.
// Kratix prepares the change; a registered writer decides where it goes and when it is delivered.
package delivery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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

// Factory builds the writer for a mode. base is the writer the state store would use on its own;
// creds is the state store's secret.
type Factory func(base writers.StateStoreWriter, config map[string]string,
	creds map[string][]byte) (writers.StateStoreWriter, error)

// Result is how a write ended
type Result struct {
	Outcome Outcome
	// VersionID identifies the delivered content. Set only when Delivered.
	VersionID string
	// Receipt identifies the delivery to ask about later. Set only when Pending.
	Receipt string
	// CheckAfter is how long to wait before checking a Pending delivery. Zero uses the default.
	CheckAfter time.Duration
	// Reason says why, for Rejected. It reaches the resource request.
	Reason string
}

// Writer is a StateStoreWriter whose writes may not be finished when they return. Implementing it
// also takes over readiness and the Destination's canary files.
// Target is the WorkPlacement a delivery carries. A writer that asks a person to approve
// the change needs to name what the change is for.
type Target struct {
	// Dir is the path within the StateStore the workloads are written under.
	Dir           string
	Namespace     string
	WorkPlacement string
	Promise       string
	// Resource is empty for a Promise's dependencies.
	Resource    string
	Pipeline    string
	Destination string
}

type Writer interface {
	writers.StateStoreWriter

	Deliver(ctx context.Context, target Target,
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

// Register panics: it runs from init(), and a dropped registration sends writes to the wrong place.
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
