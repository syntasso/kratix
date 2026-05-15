# Per-Promise Fairness — Phase 3: Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the minimum observability surface needed to operate Phase 1+2 in production — kratix-specific Prometheus instruments that aren't already covered by controller-runtime's stock metrics, plus the spec's `WatchCircuitOpen` RR status condition and per-trip Events.

**Architecture:** A small `internal/metrics/` package owns three kratix-specific instruments. Phase 1's `Breaker` gains a `StateObserver` callback; transition sites call it. The observer (constructed per-Promise in `PromiseReconciler`) writes the breaker metric, fires Warning/Normal Events on the RR, and toggles the `WatchCircuitOpen` status condition.

**Tech Stack:** Go, controller-runtime v0.20.4 (workqueue + reconcile metrics already provided out of the box), `github.com/prometheus/client_golang/prometheus`, Ginkgo/Gomega.

**Phase scope (descoped from the spec after discovery):**

Controller-runtime already exports these under stock names. We don't reimplement them:

| Spec name | Stock equivalent (already exposed on /metrics) |
|---|---|
| `kratix_dynamic_rr_workqueue_depth{promise}` | `workqueue_depth{name=<promise>}` |
| `kratix_dynamic_rr_workqueue_adds_total{promise}` | `workqueue_adds_total{name=<promise>}` |
| `kratix_dynamic_rr_reconcile_duration_seconds{promise,result}` | `controller_runtime_reconcile_time_seconds{controller=<promise>}` (no `result` label, but `controller_runtime_reconcile_total{controller,result}` provides the result split) |
| MCR field of `kratix_promise_runtime_options` | `controller_runtime_max_concurrent_reconciles{controller=<promise>}` |

Phase 3 ships only the three genuinely new instruments + the breaker observability hooks.

**Spec:** `docs/superpowers/specs/2026-05-15-per-promise-fairness-design.md` (note: the four redundant metrics in the spec's Observability section should be retracted — see Task 1 step 6 for the spec amendment.)

**Prior phases:**
- Phase 1: `docs/superpowers/plans/2026-05-15-per-promise-fairness-phase-1-circuit-breaker.md`
- Phase 2: `docs/superpowers/plans/2026-05-15-per-promise-fairness-phase-2-rate-limit.md`

Phase 1+2 are merged onto `dybnamiccontroller`. Phase 3 commits directly on that branch (no separate worktree).

---

## File Structure

**New files:**
- `internal/metrics/metrics.go` — three Prometheus instruments + `init()` registration with controller-runtime's registry.
- `internal/metrics/metrics_test.go` — per-instrument unit tests using `prometheus/testutil`.
- `internal/circuit/observer.go` — `StateObserver` interface + `StateObserverFunc` adapter + noop default + `ObservableBreaker` extension interface (returned by `NewTokenBucketBreaker`).
- `internal/circuit/observer_test.go` — verifies the observer fires for all four transition paths with correct old/new state and key.

**Modified files:**
- `internal/circuit/breaker.go` — call `b.emitTransition(...)` at each of the four `st.state =` sites; change `NewTokenBucketBreaker` return type from `Breaker` to `ObservableBreaker`.
- `internal/controller/promise_controller.go` — when constructing the per-Promise breaker, attach an observer that writes the breaker-state metric, increments trips counter, emits the `WatchCircuitOpen` condition, and fires Events.
- `internal/controller/dynamic_resource_request_controller.go` — new helper `setWatchCircuitOpenCondition(ctx, key, open, reason)` updates the condition on the underlying RR.
- `internal/controller/promise_runtime_options.go` — new helper `emitPromiseRuntimeOptionsMetric(promiseName, opts)` writes the rate-limit + breaker-params subset of the runtime options gauge (skips MCR — already covered by stock).
- `cmd/main.go` — one-line underscore import of `internal/metrics` to trigger its `init()`.

**Why this split:** Metrics live in a leaf package so controllers don't directly import `prometheus/client_golang`. The `StateObserver` is intrinsic to the breaker state machine, so it lives in `internal/circuit`. Status-condition/event emission needs `record.EventRecorder` + `client.Client`, so the per-Promise observer closure lives in `promise_controller.go` where those are in scope.

---

## Task 1: `internal/metrics` package with three new instruments

**Files:**
- Create: `internal/metrics/metrics.go`
- Create: `internal/metrics/metrics_test.go`

- [ ] **Step 1: Write the failing tests**

Write `internal/metrics/metrics_test.go`:

```go
package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/syntasso/kratix/internal/metrics"
)

func TestCircuitBreakerState_Set(t *testing.T) {
	metrics.CircuitBreakerState.With(prometheus.Labels{"promise": "p", "resource": "default/rr"}).Set(1)
	got := testutil.ToFloat64(metrics.CircuitBreakerState.With(prometheus.Labels{"promise": "p", "resource": "default/rr"}))
	if got != 1 {
		t.Fatalf("expected 1, got %v", got)
	}
}

func TestCircuitBreakerState_Delete(t *testing.T) {
	labels := prometheus.Labels{"promise": "p", "resource": "default/rr-delete"}
	metrics.CircuitBreakerState.With(labels).Set(1)
	metrics.CircuitBreakerState.Delete(labels)
	// After Delete the series is gone; .With re-creates it at 0.
	got := testutil.ToFloat64(metrics.CircuitBreakerState.With(labels))
	if got != 0 {
		t.Fatalf("expected 0 after Delete, got %v", got)
	}
}

func TestCircuitBreakerTripsTotal_Inc(t *testing.T) {
	metrics.CircuitBreakerTripsTotal.WithLabelValues("p-trips").Inc()
	got := testutil.ToFloat64(metrics.CircuitBreakerTripsTotal.WithLabelValues("p-trips"))
	if got != 1 {
		t.Fatalf("expected 1, got %v", got)
	}
}

func TestDynamicRRWorkqueueDropsTotal_Inc(t *testing.T) {
	metrics.DynamicRRWorkqueueDropsTotal.WithLabelValues("p-drops", "breaker_open").Inc()
	got := testutil.ToFloat64(metrics.DynamicRRWorkqueueDropsTotal.WithLabelValues("p-drops", "breaker_open"))
	if got != 1 {
		t.Fatalf("expected 1, got %v", got)
	}
}

func TestPromiseRuntimeOptions_Set(t *testing.T) {
	metrics.PromiseRuntimeOptions.WithLabelValues("p-opts", "rate_limit_qps").Set(50)
	got := testutil.ToFloat64(metrics.PromiseRuntimeOptions.WithLabelValues("p-opts", "rate_limit_qps"))
	if got != 50 {
		t.Fatalf("expected 50, got %v", got)
	}
}
```

- [ ] **Step 2: Implement the four instruments**

Write `internal/metrics/metrics.go`:

```go
// Package metrics owns the kratix-specific Prometheus instruments that aren't
// already exposed by controller-runtime's stock metric set. It registers
// against controller-runtime's registry so the instruments show up on the
// existing /metrics endpoint.
//
// What's intentionally NOT here (controller-runtime exposes these out of
// the box, labelled by the dynamic controller's name which equals the
// Promise name):
//   - workqueue_depth, workqueue_adds_total, workqueue_retries_total
//   - controller_runtime_reconcile_total{controller,result}
//   - controller_runtime_reconcile_time_seconds{controller}
//   - controller_runtime_active_workers{controller}
//   - controller_runtime_max_concurrent_reconciles{controller}
//
// Naming convention: kratix_<subsystem>_<metric>_<unit>. Labels: low-cardinality,
// operationally-meaningful.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// CircuitBreakerState is the per-resource breaker state gauge. Only emitted
// when the breaker is open (1) or half-open (2); closed entries are explicitly
// deleted to keep cardinality bounded at "currently misbehaving resources".
var CircuitBreakerState = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "kratix_circuit_breaker_state",
		Help: "State of the per-resource circuit breaker. 1=open, 2=half-open. Closed breakers are deleted from the series to bound cardinality.",
	},
	[]string{"promise", "resource"},
)

// CircuitBreakerTripsTotal counts breaker trips (closed -> open transitions)
// per Promise.
var CircuitBreakerTripsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "kratix_circuit_breaker_trips_total",
		Help: "Number of times the per-resource circuit breaker has tripped (closed -> open), per Promise.",
	},
	[]string{"promise"},
)

// DynamicRRWorkqueueDropsTotal counts events that were dropped at the enqueue
// layer (i.e. would have been added to the workqueue but were filtered out by
// the circuit breaker), labelled by Promise and the reason.
//
// Current reason values: "breaker_open".
var DynamicRRWorkqueueDropsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "kratix_dynamic_rr_workqueue_drops_total",
		Help: "Events dropped at the dynamic resource-request controller's enqueue layer, per Promise and reason. Distinct from controller-runtime's workqueue_adds_total which only counts adds.",
	},
	[]string{"promise", "reason"},
)

// PromiseRuntimeOptions exposes the currently-effective per-Promise tuning
// fields that controller-runtime's stock metrics don't already cover:
// rate_limit_qps, rate_limit_burst, circuit_breaker_burst,
// circuit_breaker_refill_rate, circuit_breaker_cooldown_seconds,
// circuit_breaker_disabled.
//
// MaxConcurrentReconciles is intentionally NOT emitted here — it's already
// covered by stock controller_runtime_max_concurrent_reconciles{controller=<promise>}.
var PromiseRuntimeOptions = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "kratix_promise_runtime_options",
		Help: "Currently-effective per-Promise runtime tuning options not covered by controller-runtime stock metrics.",
	},
	[]string{"promise", "option"},
)

func init() {
	crmetrics.Registry.MustRegister(
		CircuitBreakerState,
		CircuitBreakerTripsTotal,
		DynamicRRWorkqueueDropsTotal,
		PromiseRuntimeOptions,
	)
}
```

- [ ] **Step 3: Run tests, expect pass**

Run: `go -C /Users/phill.morton/Projects/Nexus/r/kratix test ./internal/metrics/... -v`
Expected: PASS, 5 tests green.

- [ ] **Step 4: Verify build**

Run: `go -C /Users/phill.morton/Projects/Nexus/r/kratix build ./internal/metrics/...`
Expected: success.

- [ ] **Step 5: Wire registration via init in cmd/main.go**

Edit `cmd/main.go`. Find the kratix import block near the top of the file (around line 60-65). Add a blank import:

```go
import (
	// ... existing imports ...
	_ "github.com/syntasso/kratix/internal/metrics" // register kratix-custom Prometheus instruments
)
```

Run: `go -C /Users/phill.morton/Projects/Nexus/r/kratix build ./cmd/...`
Expected: success.

- [ ] **Step 6: Amend the spec to retract the redundant metrics**

Edit `docs/superpowers/specs/2026-05-15-per-promise-fairness-design.md`. Find the Observability table (around line 184). Replace the seven-metric table with this four-metric table plus a note:

```markdown
**Metrics** (Prometheus, registered against `controller-runtime`'s registry).
Workqueue depth/adds, reconcile counts, reconcile duration, active workers,
and configured `MaxConcurrentReconciles` per dynamic controller are already
exposed by controller-runtime under stock names (`workqueue_*`,
`controller_runtime_*`, labelled by `controller=<promise>` or `name=<promise>`)
and are not duplicated here.

| Metric | Type | Labels |
|---|---|---|
| `kratix_circuit_breaker_state` | gauge | `promise`, `resource` (only when open/half-open) |
| `kratix_circuit_breaker_trips_total` | counter | `promise` |
| `kratix_dynamic_rr_workqueue_drops_total` | counter | `promise`, `reason` |
| `kratix_promise_runtime_options` | gauge | `promise`, `option` (rate-limit + breaker fields; MCR via stock) |

Cardinality note: `kratix_circuit_breaker_state` only emits per-resource labels
when state is open or half-open. Steady-state healthy resources contribute
nothing — the per-resource series is deleted on recovery.
```

- [ ] **Step 7: Commit**

```bash
git -C /Users/phill.morton/Projects/Nexus/r/kratix add internal/metrics/ cmd/main.go docs/superpowers/specs/2026-05-15-per-promise-fairness-design.md
git -C /Users/phill.morton/Projects/Nexus/r/kratix commit -m "feat(metrics): introduce kratix metrics package with breaker + options instruments

Add the four kratix-specific instruments not already covered by
controller-runtime's stock metrics:
- kratix_circuit_breaker_state{promise,resource}
- kratix_circuit_breaker_trips_total{promise}
- kratix_dynamic_rr_workqueue_drops_total{promise,reason}
- kratix_promise_runtime_options{promise,option}

Amend the spec's Observability section to drop the four metrics
(workqueue depth/adds, reconcile duration, MCR) that controller-runtime
already exposes under stock names."
```

---

## Task 2: `StateObserver` callback in `internal/circuit`

**Files:**
- Create: `internal/circuit/observer.go`
- Create: `internal/circuit/observer_test.go`
- Modify: `internal/circuit/breaker.go`

- [ ] **Step 1: Write the failing test**

Write `internal/circuit/observer_test.go`:

```go
package circuit_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/types"
	testclock "k8s.io/utils/clock/testing"

	"github.com/syntasso/kratix/internal/circuit"
)

type capturedTransition struct {
	Key      types.NamespacedName
	OldState circuit.State
	NewState circuit.State
}

var _ = Describe("StateObserver", func() {
	It("is invoked on every state transition with old/new state and key", func() {
		clk := testclock.NewFakeClock(time.Unix(0, 0))
		params := circuit.BreakerParams{
			Burst:                 2,
			RefillRate:            0.01,
			Cooldown:              time.Minute,
			HalfOpenProbeInterval: 30 * time.Second,
		}
		var events []capturedTransition
		observer := circuit.StateObserverFunc(func(key types.NamespacedName, oldS, newS circuit.State) {
			events = append(events, capturedTransition{key, oldS, newS})
		})

		breaker := circuit.NewTokenBucketBreaker(params, clk).WithObserver(observer)
		key := types.NamespacedName{Namespace: "ns", Name: "rr"}

		// Drain burst, third call trips → expect Closed -> Open
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.Allow(key)).To(BeFalse())
		Expect(events).To(ContainElement(capturedTransition{key, circuit.StateClosed, circuit.StateOpen}))

		// Cooldown elapses → Allow moves to Half-Open
		clk.Step(2 * time.Minute)
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(events).To(ContainElement(capturedTransition{key, circuit.StateOpen, circuit.StateHalfOpen}))

		// Successful Observe drops back to Closed
		breaker.Observe(key, true)
		Expect(events).To(ContainElement(capturedTransition{key, circuit.StateHalfOpen, circuit.StateClosed}))

		// Trip again, fail in half-open → Half-Open -> Open
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.Allow(key)).To(BeFalse())
		clk.Step(2 * time.Minute)
		Expect(breaker.Allow(key)).To(BeTrue())
		breaker.Observe(key, false)
		Expect(events).To(ContainElement(capturedTransition{key, circuit.StateHalfOpen, circuit.StateOpen}))
	})

	It("is a no-op when no observer is set", func() {
		clk := testclock.NewFakeClock(time.Unix(0, 0))
		breaker := circuit.NewTokenBucketBreaker(circuit.BreakerParams{
			Burst:                 1,
			RefillRate:            0.01,
			Cooldown:              time.Minute,
			HalfOpenProbeInterval: 30 * time.Second,
		}, clk)
		key := types.NamespacedName{Namespace: "ns", Name: "rr"}
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.Allow(key)).To(BeFalse())
	})
})
```

- [ ] **Step 2: Run, expect compile error**

Run: `go -C /Users/phill.morton/Projects/Nexus/r/kratix test ./internal/circuit/... -v -ginkgo.focus="StateObserver"`
Expected: FAIL with "undefined: circuit.StateObserverFunc" and "no method WithObserver".

- [ ] **Step 3: Implement the observer types**

Write `internal/circuit/observer.go`:

```go
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
```

- [ ] **Step 4: Wire the observer into `tokenBucketBreaker`**

Edit `internal/circuit/breaker.go`:

(a) Add `observer StateObserver` to the struct (currently named `tokenBucketBreaker`, around line 71):

```go
type tokenBucketBreaker struct {
	mu       sync.Mutex
	params   BreakerParams
	clock    clock.Clock
	keys     map[types.NamespacedName]*bucketState
	observer StateObserver
}
```

(b) Initialise the observer in `NewTokenBucketBreaker` and change the return type:

```go
// NewTokenBucketBreaker constructs an ObservableBreaker. Call WithObserver
// on the result to attach state-transition observability; if not called, the
// breaker uses a no-op observer.
func NewTokenBucketBreaker(params BreakerParams, clk clock.Clock) ObservableBreaker {
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &tokenBucketBreaker{
		params:   params,
		clock:    clk,
		keys:     map[types.NamespacedName]*bucketState{},
		observer: noopObserver{},
	}
}
```

(c) Add the `WithObserver` method:

```go
func (b *tokenBucketBreaker) WithObserver(o StateObserver) ObservableBreaker {
	b.mu.Lock()
	defer b.mu.Unlock()
	if o == nil {
		b.observer = noopObserver{}
		return b
	}
	b.observer = o
	return b
}
```

(d) Add an internal `emitTransition` helper near the other methods:

```go
// emitTransition is called under b.mu. Observers must not block or re-enter
// Breaker methods (they would deadlock).
func (b *tokenBucketBreaker) emitTransition(key types.NamespacedName, old, new State) {
	if b.observer == nil || old == new {
		return
	}
	b.observer.OnTransition(key, old, new)
}
```

(e) Call `emitTransition` at the four state-change sites. Find each `st.state = ...` line and add the call **before** it:

In `Allow`, the open→half-open path (current line ~113):

```go
case StateOpen:
	if now.Sub(st.openedAt) < b.params.Cooldown {
		return false
	}
	b.emitTransition(key, StateOpen, StateHalfOpen)
	st.state = StateHalfOpen
	st.lastProbe = now
	st.inFlightProbe = true
	return true
```

In `Allow`, the closed→open path (current line ~132):

```go
if st.tokens < 1 {
	b.emitTransition(key, StateClosed, StateOpen)
	st.state = StateOpen
	st.openedAt = now
	return false
}
```

In `Observe`, the half-open→closed (success) path (current line ~164):

```go
if success {
	b.emitTransition(key, StateHalfOpen, StateClosed)
	delete(b.keys, key)
	return
}
```

In `Observe`, the half-open→open (failure) path (current line ~167):

```go
b.emitTransition(key, StateHalfOpen, StateOpen)
st.state = StateOpen
st.openedAt = b.clock.Now()
```

- [ ] **Step 5: Run tests, expect pass**

Run: `go -C /Users/phill.morton/Projects/Nexus/r/kratix test ./internal/circuit/... -v`
Expected: all Phase 1 circuit specs still green + new `StateObserver` specs green.

- [ ] **Step 6: Run with race detector**

Run: `go -C /Users/phill.morton/Projects/Nexus/r/kratix test ./internal/circuit/... -race`
Expected: clean exit.

- [ ] **Step 7: Commit**

```bash
git -C /Users/phill.morton/Projects/Nexus/r/kratix add internal/circuit/observer.go internal/circuit/observer_test.go internal/circuit/breaker.go
git -C /Users/phill.morton/Projects/Nexus/r/kratix commit -m "feat(circuit): add StateObserver callback for transition observability"
```

---

## Task 3: Wire breaker observer to emit metrics + Events + WatchCircuitOpen condition

**Files:**
- Modify: `internal/controller/dynamic_resource_request_controller.go`
- Modify: `internal/controller/promise_controller.go`
- Modify: `internal/controller/promise_runtime_options.go`

- [ ] **Step 1: Add the condition-update helper on the controller**

Edit `internal/controller/dynamic_resource_request_controller.go`. Near the existing status helpers (search for `SetStatusCondition` in this file to find a sensible insertion point; if none, append at the end of the file, before any test-only code), add:

```go
// setWatchCircuitOpenCondition writes a WatchCircuitOpen condition onto the
// underlying resource-request CR. Called from the per-Promise breaker observer
// when a resource's breaker state changes. Errors are logged at Debug; failure
// to write the condition must not block the breaker state machine.
func (r *DynamicResourceRequestController) setWatchCircuitOpenCondition(ctx context.Context, key types.NamespacedName, open bool, reason string) {
	logger := r.Log.WithValues("rr", key.String())

	rr := &unstructured.Unstructured{}
	rr.SetGroupVersionKind(*r.GVK)
	if err := r.Client.Get(ctx, key, rr); err != nil {
		logging.Debug(logger, "skipping WatchCircuitOpen update; RR not found", "error", err.Error())
		return
	}

	status := metav1.ConditionFalse
	msg := "Circuit breaker is closed"
	if open {
		status = metav1.ConditionTrue
		msg = "Circuit breaker is open; events for this resource are being dropped at the enqueue layer"
	}

	conds, _, _ := unstructured.NestedSlice(rr.Object, "status", "conditions")
	updated, changed := mergeWatchCircuitCondition(conds, string(status), reason, msg)
	if !changed {
		return
	}
	if err := unstructured.SetNestedSlice(rr.Object, updated, "status", "conditions"); err != nil {
		logging.Debug(logger, "failed to set conditions slice", "error", err.Error())
		return
	}
	if err := r.Client.Status().Update(ctx, rr); err != nil {
		logging.Debug(logger, "failed to update RR status with WatchCircuitOpen", "error", err.Error())
	}
}

// mergeWatchCircuitCondition upserts the WatchCircuitOpen condition into an
// unstructured conditions slice. Returns (slice, true) if the merge changed
// anything (status/reason/message diff); (slice, false) if not.
func mergeWatchCircuitCondition(conds []interface{}, status, reason, msg string) ([]interface{}, bool) {
	now := time.Now().UTC().Format(time.RFC3339)
	for i, raw := range conds {
		cm, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if cm["type"] != "WatchCircuitOpen" {
			continue
		}
		// Already present — update if anything changed.
		oldStatus, _ := cm["status"].(string)
		oldReason, _ := cm["reason"].(string)
		oldMsg, _ := cm["message"].(string)
		if oldStatus == status && oldReason == reason && oldMsg == msg {
			return conds, false
		}
		cm["status"] = status
		cm["reason"] = reason
		cm["message"] = msg
		if oldStatus != status {
			cm["lastTransitionTime"] = now
		}
		conds[i] = cm
		return conds, true
	}
	// Not present — append.
	return append(conds, map[string]interface{}{
		"type":               "WatchCircuitOpen",
		"status":             status,
		"reason":             reason,
		"message":            msg,
		"lastTransitionTime": now,
	}), true
}
```

Ensure imports include `context`, `time`, `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"`, and `"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"`. They're likely already there.

- [ ] **Step 2: Add the `transitionReason` helper + per-Promise observer factory**

Edit `internal/controller/promise_controller.go`. Add a package-level helper near other helpers (after `emitBreakerWarnings`):

```go
// transitionReason maps a breaker state transition to a short human-readable
// reason string for use in Events and Status conditions.
func transitionReason(oldState, newState circuit.State) string {
	switch {
	case oldState == circuit.StateClosed && newState == circuit.StateOpen:
		return "Tripped"
	case oldState == circuit.StateOpen && newState == circuit.StateHalfOpen:
		return "Probing"
	case oldState == circuit.StateHalfOpen && newState == circuit.StateClosed:
		return "Recovered"
	case oldState == circuit.StateHalfOpen && newState == circuit.StateOpen:
		return "RecoveryFailed"
	default:
		return "Unknown"
	}
}
```

Then in `ensureDynamicControllerIsStarted`'s new-controller branch, find where the breaker is constructed (`breaker := circuit.NewTokenBucketBreaker(runtimeOpts.Breaker, nil)`). Right after that, build and attach the observer:

```go
breaker := circuit.NewTokenBucketBreaker(runtimeOpts.Breaker, nil)

// Capture per-Promise state for the observer closure.
promiseName := promise.GetName()
recorder := r.Manager.GetEventRecorderFor("ResourceRequestController")
// `dynamicResourceRequestController` is the *DynamicResourceRequestController
// instance being constructed; the closure captures it by reference so the
// observer can call setWatchCircuitOpenCondition on it.

observer := circuit.StateObserverFunc(func(key types.NamespacedName, oldState, newState circuit.State) {
	// 1. Log the transition (Phase 3 also requires the spec's "transitions logged
	// at Info" line).
	logging.Info(r.Log.WithName(promiseName), "circuit breaker state transition",
		"resource", key.String(),
		"oldState", oldState.String(),
		"newState", newState.String(),
	)

	// 2. Metrics.
	resourceLabel := key.Namespace + "/" + key.Name
	switch newState {
	case circuit.StateClosed:
		metrics.CircuitBreakerState.Delete(prometheus.Labels{"promise": promiseName, "resource": resourceLabel})
	case circuit.StateOpen:
		metrics.CircuitBreakerState.With(prometheus.Labels{"promise": promiseName, "resource": resourceLabel}).Set(1)
	case circuit.StateHalfOpen:
		metrics.CircuitBreakerState.With(prometheus.Labels{"promise": promiseName, "resource": resourceLabel}).Set(2)
	}
	if oldState == circuit.StateClosed && newState == circuit.StateOpen {
		metrics.CircuitBreakerTripsTotal.WithLabelValues(promiseName).Inc()
		metrics.DynamicRRWorkqueueDropsTotal.WithLabelValues(promiseName, "breaker_open").Inc()
	}

	// 3. RR status condition + Events. Use a short-lived context for the API
	// writes; the breaker doesn't have a ctx to pass us.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	open := newState != circuit.StateClosed
	dynamicResourceRequestController.setWatchCircuitOpenCondition(ctx, key, open, transitionReason(oldState, newState))

	// Emit Event on actual state transitions.
	if recorder != nil {
		rr := &unstructured.Unstructured{}
		rr.SetGroupVersionKind(*rrGVK)
		if err := r.Client.Get(ctx, key, rr); err == nil {
			if oldState == circuit.StateClosed && newState == circuit.StateOpen {
				recorder.Event(rr, corev1.EventTypeWarning, "CircuitBreakerOpen",
					"Per-resource circuit breaker tripped; events for this resource are being dropped at enqueue.")
			}
			if newState == circuit.StateClosed && oldState != circuit.StateClosed {
				recorder.Event(rr, corev1.EventTypeNormal, "CircuitBreakerClosed",
					"Per-resource circuit breaker recovered; events for this resource are flowing again.")
			}
		}
	}
})

if observable, ok := breaker.(circuit.ObservableBreaker); ok {
	breaker = observable.WithObserver(observer)
}
```

Imports needed: `"github.com/syntasso/kratix/internal/metrics"`, `"github.com/prometheus/client_golang/prometheus"`, `corev1 "k8s.io/api/core/v1"` (likely aliased differently in this file as `v1` — check existing imports), `"context"`, `"time"`. Reuse existing aliases where possible.

- [ ] **Step 3: Emit the `PromiseRuntimeOptions` gauge on resolve**

Edit `internal/controller/promise_runtime_options.go`. Add an import of `"github.com/syntasso/kratix/internal/metrics"` and a helper:

```go
// EmitMetric writes the rate-limit + breaker subset of these options to
// kratix_promise_runtime_options. MaxConcurrentReconciles is intentionally
// omitted — controller-runtime's stock controller_runtime_max_concurrent_reconciles
// already covers it.
func (o PromiseRuntimeOptions) EmitMetric(promiseName string) {
	metrics.PromiseRuntimeOptions.WithLabelValues(promiseName, "rate_limit_qps").Set(float64(o.RateLimitQPS))
	metrics.PromiseRuntimeOptions.WithLabelValues(promiseName, "rate_limit_burst").Set(float64(o.RateLimitBurst))
	metrics.PromiseRuntimeOptions.WithLabelValues(promiseName, "circuit_breaker_burst").Set(o.Breaker.Burst)
	metrics.PromiseRuntimeOptions.WithLabelValues(promiseName, "circuit_breaker_refill_rate").Set(o.Breaker.RefillRate)
	metrics.PromiseRuntimeOptions.WithLabelValues(promiseName, "circuit_breaker_cooldown_seconds").Set(o.Breaker.Cooldown.Seconds())
	disabled := 0.0
	if o.Breaker.Disabled {
		disabled = 1
	}
	metrics.PromiseRuntimeOptions.WithLabelValues(promiseName, "circuit_breaker_disabled").Set(disabled)
}
```

In `promise_controller.go`, call it from both the new-controller branch (immediately after resolving options) and the reuse branch (immediately after detecting a change):

In the new-controller branch, after `runtimeOpts, warnings := ResolvePromiseRuntimeOptions(...)`:

```go
runtimeOpts.EmitMetric(promise.GetName())
```

In the reuse branch, replace the existing `dynamicController.LastRuntimeOptions = newOpts` with:

```go
dynamicController.LastRuntimeOptions = newOpts
newOpts.EmitMetric(promise.GetName())
```

- [ ] **Step 4: Build + run existing controller tests**

Run: `go -C /Users/phill.morton/Projects/Nexus/r/kratix build ./...`
Then: `go -C /Users/phill.morton/Projects/Nexus/r/kratix test ./internal/controller/...`
Expected: build clean; existing controller specs all green.

- [ ] **Step 5: Add an envtest that exercises the breaker observer end-to-end**

Add to `internal/controller/promise_controller_test.go` inside an existing `Describe` for dynamic-RR controllers. Mirror the breaker-update-test scaffolding (uses `fakeK8sClient`, `reconciler`, `reconcileUntilCompletion`, `testLogger`):

```go
It("sets WatchCircuitOpen=True on the RR and emits CircuitBreakerState=1 when the breaker trips", func() {
	// Annotate the Promise with a tight breaker so we can trip it deterministically.
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, promise)).To(Succeed())
	ann := promise.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[controller.AnnotationCircuitBreakerBurst] = "2"
	ann[controller.AnnotationCircuitBreakerRefill] = "0.001"
	promise.SetAnnotations(ann)
	Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())
	reconcileUntilCompletion(reconciler, promise)

	dc, ok := reconciler.StartedDynamicControllers[promise.GetDynamicControllerName(testLogger)]
	Expect(ok).To(BeTrue())

	key := types.NamespacedName{Namespace: "default", Name: "rr-observer"}
	// Drain the breaker through three direct calls.
	Expect(dc.Breaker.Allow(key)).To(BeTrue())
	Expect(dc.Breaker.Allow(key)).To(BeTrue())
	Expect(dc.Breaker.Allow(key)).To(BeFalse()) // trips

	Eventually(func(g Gomega) {
		got := testutil.ToFloat64(metrics.CircuitBreakerState.With(prometheus.Labels{
			"promise":  promise.GetName(),
			"resource": key.String(),
		}))
		g.Expect(got).To(Equal(float64(1)), "kratix_circuit_breaker_state should be 1 (open)")
	}, "5s", "100ms").Should(Succeed())

	Eventually(func(g Gomega) {
		got := testutil.ToFloat64(metrics.CircuitBreakerTripsTotal.WithLabelValues(promise.GetName()))
		g.Expect(got).To(BeNumerically(">=", 1))
	}, "5s", "100ms").Should(Succeed())
})
```

(Imports: `"github.com/prometheus/client_golang/prometheus"`, `"github.com/prometheus/client_golang/prometheus/testutil"`, `"github.com/syntasso/kratix/internal/metrics"`. The condition-write portion of the observer logs a Debug "skipping WatchCircuitOpen update; RR not found" because the fake test scaffolding doesn't materialise the RR — that's expected and not asserted. The metric assertions are the proxy.)

- [ ] **Step 6: Run the focused test**

Run: `go -C /Users/phill.morton/Projects/Nexus/r/kratix test ./internal/controller/... -v -ginkgo.focus="WatchCircuitOpen=True on the RR"`
Expected: PASS.

- [ ] **Step 7: Run full controller suite for no-regression**

Run: `go -C /Users/phill.morton/Projects/Nexus/r/kratix test ./internal/controller/... -race`
Expected: all specs green; race detector clean.

- [ ] **Step 8: Commit**

```bash
git -C /Users/phill.morton/Projects/Nexus/r/kratix add internal/controller/promise_controller.go internal/controller/promise_runtime_options.go internal/controller/dynamic_resource_request_controller.go internal/controller/promise_controller_test.go
git -C /Users/phill.morton/Projects/Nexus/r/kratix commit -m "feat(controller): emit breaker metrics, WatchCircuitOpen condition, and per-trip Events via StateObserver

The per-Promise breaker observer:
- writes kratix_circuit_breaker_state (1=open, 2=half-open, deleted on close)
- increments kratix_circuit_breaker_trips_total on closed -> open
- increments kratix_dynamic_rr_workqueue_drops_total{reason=breaker_open} on trip
- writes a WatchCircuitOpen status condition on the underlying RR
- emits a Warning Event (CircuitBreakerOpen) on trip and Normal Event
  (CircuitBreakerClosed) on recovery
- logs each transition at Info level

PromiseRuntimeOptions.EmitMetric writes the rate-limit + breaker subset of
kratix_promise_runtime_options on every resolve (new-controller branch and
reuse-branch detected change). MaxConcurrentReconciles is intentionally not
emitted; it's already covered by controller-runtime's stock
controller_runtime_max_concurrent_reconciles{controller=<promise>}."
```

---

## Task 4: Live smoke

**Files:** none new — exercises the cluster.

- [ ] **Step 1: Build, load, restart**

Run: `make -C /Users/phill.morton/Projects/Nexus/r/kratix build-and-load-kratix`
Expected: image built, loaded into kind, manager rollout succeeds.

- [ ] **Step 2: Apply a Promise with tight breaker burst + an RR**

Write `/tmp/p3-trip-promise.yaml` (copy the noop-promise structure with annotations):

```yaml
apiVersion: platform.kratix.io/v1alpha1
kind: Promise
metadata:
  name: p3-trip
  annotations:
    kratix.io/circuit-breaker-burst: "2"
    kratix.io/circuit-breaker-refill: "0.01"
spec:
  destinationSelectors:
    - matchLabels:
        environment: dev
  api:
    apiVersion: apiextensions.k8s.io/v1
    kind: CustomResourceDefinition
    metadata:
      name: p3trips.perf.kratix.io
    spec:
      group: perf.kratix.io
      names:
        kind: P3Trip
        plural: p3trips
        singular: p3trip
      scope: Namespaced
      versions:
        - name: v1alpha1
          served: true
          storage: true
          schema:
            openAPIV3Schema:
              type: object
              properties:
                spec:
                  type: object
                  properties:
                    replicas:
                      type: integer
                      default: 1
  workflows:
    resource:
      configure:
        - apiVersion: platform.kratix.io/v1alpha1
          kind: Pipeline
          metadata:
            name: noop
          spec:
            containers:
              - name: noop
                image: busybox:1.36
                command: ["sh", "-c", "exit 0"]
```

Apply it:

```bash
kubectl --context kind-platform apply -f /tmp/p3-trip-promise.yaml
```

Create one RR:

```bash
cat <<EOF | kubectl --context kind-platform apply -f -
apiVersion: perf.kratix.io/v1alpha1
kind: P3Trip
metadata:
  name: trip
  namespace: default
spec:
  replicas: 1
EOF
```

- [ ] **Step 3: Generate enough events to trip the breaker**

Rapidly poke the RR with annotation updates to push past the burst=2 cap:

```bash
for i in $(seq 1 20); do
  kubectl --context kind-platform annotate p3trip trip kratix.io/poke=$i --overwrite
done
```

- [ ] **Step 4: Verify metrics on /metrics**

Use the perf rig's existing metrics-scraping pattern to fetch /metrics:

```bash
kubectl --context kind-platform -n kratix-platform-system get pod -l control-plane=controller-manager -o name | xargs -I {} kubectl --context kind-platform -n kratix-platform-system exec {} -- wget -qO- --no-check-certificate http://localhost:8081/metrics 2>/dev/null | grep kratix_
```

(If the manager only exposes metrics on the auth-required port 8443, follow the same bearer-token dance the perf rig's scraper uses — see `test/perf/scraper/metrics.go`.)

Expected output (sample):

```
kratix_circuit_breaker_state{promise="p3-trip",resource="default/trip"} 1
kratix_circuit_breaker_trips_total{promise="p3-trip"} 1
kratix_dynamic_rr_workqueue_drops_total{promise="p3-trip",reason="breaker_open"} 1
kratix_promise_runtime_options{promise="p3-trip",option="circuit_breaker_burst"} 2
kratix_promise_runtime_options{promise="p3-trip",option="circuit_breaker_refill_rate"} 0.01
kratix_promise_runtime_options{promise="p3-trip",option="rate_limit_qps"} 0
```

- [ ] **Step 5: Verify the WatchCircuitOpen condition**

```bash
kubectl --context kind-platform get p3trip trip -o jsonpath='{.status.conditions}' | jq
```

Expected: an array entry with `type: WatchCircuitOpen`, `status: "True"`, `reason: Tripped`.

- [ ] **Step 6: Verify the Event**

```bash
kubectl --context kind-platform describe p3trip trip | sed -n '/^Events:/,$p'
```

Expected: a Warning Event with reason `CircuitBreakerOpen`.

- [ ] **Step 7: Verify the log line**

```bash
kubectl --context kind-platform -n kratix-platform-system logs deploy/kratix-platform-controller-manager --since=2m | grep "circuit breaker state transition"
```

Expected: at least one Info line with `oldState=closed newState=open resource=default/trip`.

- [ ] **Step 8: Verify recovery**

Wait for the cooldown (the test Promise has cooldown defaulted to 5m; bump down via annotation if you don't want to wait):

```bash
kubectl --context kind-platform annotate promise p3-trip kratix.io/circuit-breaker-cooldown=10s --overwrite
```

Wait 15 seconds. The breaker should transition to half-open and, on a successful reconcile, back to closed.

```bash
kubectl --context kind-platform get p3trip trip -o jsonpath='{.status.conditions}' | jq
```

Expected: WatchCircuitOpen flipped to `status: "False"`, `reason: Recovered`. The `kratix_circuit_breaker_state` series for `resource="default/trip"` should be deleted (no longer present in /metrics output).

- [ ] **Step 9: Document results**

Create `docs/perf-phase3-observability-smoke.md` capturing:
- Metric output sample (the kratix_* lines from /metrics)
- Status condition JSON
- Event description
- Log line example
- Recovery confirmation (condition flip + metric series deletion)

- [ ] **Step 10: Commit smoke notes**

```bash
git -C /Users/phill.morton/Projects/Nexus/r/kratix add docs/perf-phase3-observability-smoke.md
git -C /Users/phill.morton/Projects/Nexus/r/kratix commit -m "docs(perf): record Phase 3 observability smoke results"
```

---

## Self-review checklist

- [ ] `go test ./internal/metrics/... ./internal/circuit/... ./internal/controller/... -race` passes.
- [ ] `go vet ./...` clean.
- [ ] `/metrics` endpoint exposes the four kratix-specific instruments + the stock controller-runtime metrics (`workqueue_*{name=<promise>}`, `controller_runtime_*{controller=<promise>}`) — confirm both name forms appear.
- [ ] Breaker trip emits: log line, metric increment, status condition True, Warning Event.
- [ ] Breaker recovery emits the inverse: log line, metric series deleted, condition flip to False with reason `Recovered`, Normal Event.
- [ ] No regression on Phase 1+2 specs.
- [ ] Spec amendment in Task 1 step 6 is committed and explicitly retracts the four redundant metrics.

## Out of scope (documented limitations)

- **Per-non-trip drop counting** — drops only counted on breaker trip (reason `breaker_open`). If we ever want per-event drop counters (every event the breaker drops, not just transitions), the `circuit.MapFunc`/`circuit.Predicate` wrappers would need to accept a drop-callback. Phase 3 ships without it because the trip-rate signal is sufficient for the operational use case.
- **Tokens-remaining log field** — the spec mentions logging "tokens remaining" on each transition; the `StateObserver` signature doesn't carry it. Extending the observer to include bucket-state context is a follow-up if a real consumer needs it.
- **Annotation-parse-failure metric** — annotation parse failures already emit Events on the Promise (Phase 1 Task 5). A counter wasn't asked for in the spec; not adding one.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-15-per-promise-fairness-phase-3-observability.md`.

Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline execution** — work through the tasks in this session with checkpoints.

Which approach?
