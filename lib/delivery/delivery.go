// Package delivery holds the writer every state store is written through. A state store with no
// delivery mode uses Direct
package delivery

import (
	"context"
	"errors"
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
	creds map[string][]byte) (Writer, error)

// Result is how a write ended
type Result struct {
	Outcome Outcome
	// VersionID identifies the delivered content. Set only when Delivered.
	VersionID string
	// Receipt identifies the delivery to ask about later. Set only when Pending.
	Receipt string
	// CheckAfter is how long to wait before checking a Pending delivery. Zero uses the default.
	CheckAfter time.Duration
	// Reason explains a Pending or Rejected result
	Reason string
	// URL links to the delivery, such as a pull request
	URL string
}

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

// Writer is the interface to write to the state store
type Writer interface {
	writers.StateStoreWriter

	Deliver(ctx context.Context, target Target,
		create []v1alpha1.Workload, remove []string) (Result, error)

	Check(ctx context.Context, receipt string) (Result, error)

	// WriteCanary returns true when a canary file was written
	WriteCanary(ctx context.Context, name string, workloads []v1alpha1.Workload) (bool, error)
	RemoveCanary(ctx context.Context, name string, files []string) error
}

// ErrUnknownReceipt means that the Writer cannot parse the provided delivery receipt
var ErrUnknownReceipt = errors.New("delivery receipt not recognised by this delivery mode")

type direct struct {
	writers.StateStoreWriter
}

// Direct is the built-in mode's writer: every call writes through base and finishes before returning.
func Direct(base writers.StateStoreWriter) Writer {
	return direct{StateStoreWriter: base}
}

func (d direct) Deliver(_ context.Context, target Target,
	create []v1alpha1.Workload, remove []string) (Result, error) {
	versionID, err := d.UpdateFiles(target.Dir, target.WorkPlacement, create, remove)
	if err != nil {
		return Result{}, err
	}
	return Result{Outcome: Delivered, VersionID: versionID}, nil
}

func (d direct) Check(_ context.Context, receipt string) (Result, error) {
	return Result{}, fmt.Errorf("%w: %q", ErrUnknownReceipt, receipt)
}

func (d direct) WriteCanary(_ context.Context, name string, workloads []v1alpha1.Workload) (bool, error) {
	if _, err := d.UpdateFiles("", name, workloads, nil); err != nil {
		return false, err
	}
	return true, nil
}

func (d direct) RemoveCanary(_ context.Context, name string, files []string) error {
	return d.DeleteFiles(name, files)
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

// For returns Direct for the built-in mode.
func For(mode string) (Factory, error) {
	mu.RLock()
	defer mu.RUnlock()

	if mode == BuiltIn {
		return func(base writers.StateStoreWriter, _ map[string]string, _ map[string][]byte) (Writer, error) {
			return Direct(base), nil
		}, nil
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
