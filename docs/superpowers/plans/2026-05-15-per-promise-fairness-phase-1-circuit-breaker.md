# Per-Promise Fairness — Phase 1: Per-Resource Circuit Breaker

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-resource token-bucket circuit breaker that drops events at the enqueue layer of every dynamic resource-request controller, so a single hot resource can't starve its Promise's workqueue.

**Architecture:** New `internal/circuit/` package provides a `Breaker` interface and a token-bucket implementation. Each dynamic RR controller gets its own `Breaker` instance, wired into the existing three `Watches(...)` MapFuncs and the primary `For(...)` source via a predicate. Global flags configure defaults; `kratix.io/circuit-breaker-*` annotations override per Promise. Breaker params are live-updatable; the breaker reference is stored on `DynamicResourceRequestController` so `Reconcile` can call `Observe` at the end of each pass.

**Tech Stack:** Go, controller-runtime v0.20.4, k8s.io/utils/clock, Ginkgo/Gomega, envtest, `goleak`.

**Phase scope:** This phase ships hot-resource protection only. Phase 2 adds per-Promise rate limiter + MaxConcurrentReconciles + the full `PromiseRuntimeOptions` resolver. Phase 3 adds Prometheus metrics, status conditions, and structured events. This phase emits log lines and parses only the breaker-related subset of annotations.

**Spec:** `docs/superpowers/specs/2026-05-15-per-promise-fairness-design.md`

---

## File Structure

**New files:**
- `internal/circuit/breaker.go` — `Breaker` interface, `BreakerParams`, `State` enum, `NewTokenBucketBreaker` constructor, token-bucket per-key state machine.
- `internal/circuit/breaker_test.go` — Ginkgo unit tests with injected fake clock.
- `internal/circuit/mapfunc.go` — `MapFunc` wrapper that gates `handler.MapFunc` calls through a breaker.
- `internal/circuit/predicate.go` — `Predicate` that gates the primary `For(...)` source by `req.NamespacedName`.
- `internal/circuit/wrappers_test.go` — Ginkgo unit tests for `MapFunc` and `Predicate`.
- `internal/circuit/suite_test.go` — Ginkgo bootstrap for the package.

**Modified files:**
- `cmd/main.go` — new flags `--circuit-breaker-burst`, `--circuit-breaker-refill-rate`, `--circuit-breaker-cooldown`, `--circuit-breaker-enabled`; passed into `PromiseReconciler`.
- `internal/controller/promise_controller.go` — new `BreakerDefaults` field on `PromiseReconciler`; new `resolveBreakerParams` helper that reads annotations and falls back to defaults; `ensureDynamicControllerIsStarted` constructs a `Breaker` per Promise, wraps the three `MapFunc`s and adds a breaker predicate to the `For(...)` builder; annotation change comparison drives live `UpdateParams`; breaker reference stored on `DynamicResourceRequestController` and dropped on teardown.
- `internal/controller/dynamic_resource_request_controller.go` — new `Breaker circuit.Breaker` field on `DynamicResourceRequestController`; `Reconcile` adds `defer r.Breaker.Observe(req.NamespacedName, retErr == nil)` after the existing named-return setup.
- `internal/controller/promise_controller_test.go` — envtest cases for breaker wiring, annotation update path, teardown.
- `go.mod` / `go.sum` — promote `k8s.io/utils/clock` from indirect to direct.

**Why this split:** `internal/circuit` is a leaf package with no kratix imports — it can be unit-tested in isolation with a fake clock. The two wrapper files are tiny adapters between the breaker and controller-runtime types, kept separate so they can be tested without spinning up envtest. The controller files only gain wiring; the bulk of the logic lives in `internal/circuit`.

---

## Task 1: Bootstrap `internal/circuit` package with Breaker types

**Files:**
- Create: `internal/circuit/breaker.go`
- Create: `internal/circuit/suite_test.go`

- [ ] **Step 1: Create the package suite_test scaffold**

Write `internal/circuit/suite_test.go`:

```go
package circuit_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCircuit(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Circuit Suite")
}
```

- [ ] **Step 2: Create breaker.go with type declarations only**

Write `internal/circuit/breaker.go`:

```go
package circuit

import (
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
)

// State is the breaker state for a single resource key.
type State int

const (
	StateClosed State = iota
	StateHalfOpen
	StateOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half-open"
	case StateOpen:
		return "open"
	default:
		return "unknown"
	}
}

// BreakerParams configures a Breaker. Zero values are NOT safe defaults;
// callers should populate every field.
type BreakerParams struct {
	Burst                 float64
	RefillRate            float64 // tokens per second
	Cooldown              time.Duration
	HalfOpenProbeInterval time.Duration
	Disabled              bool
}

// Breaker gates enqueue decisions and tracks per-resource state.
// All methods are safe for concurrent use.
type Breaker interface {
	Allow(key types.NamespacedName) bool
	Observe(key types.NamespacedName, success bool)
	State(key types.NamespacedName) State
	UpdateParams(params BreakerParams)
}

// NewTokenBucketBreaker constructs a Breaker. clk may be nil to use a real clock.
func NewTokenBucketBreaker(params BreakerParams, clk clock.Clock) Breaker {
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &tokenBucketBreaker{
		params: params,
		clock:  clk,
		keys:   map[types.NamespacedName]*bucketState{},
	}
}

type bucketState struct {
	tokens     float64
	lastRefill time.Time
	state      State
	openedAt   time.Time
	lastProbe  time.Time
}

type tokenBucketBreaker struct {
	// unexported fields filled in Task 2
	params BreakerParams
	clock  clock.Clock
	keys   map[types.NamespacedName]*bucketState
}

// Stub method bodies to satisfy the interface; real logic in Task 2.
func (b *tokenBucketBreaker) Allow(key types.NamespacedName) bool        { return true }
func (b *tokenBucketBreaker) Observe(key types.NamespacedName, ok bool)  {}
func (b *tokenBucketBreaker) State(key types.NamespacedName) State       { return StateClosed }
func (b *tokenBucketBreaker) UpdateParams(params BreakerParams)          {}
```

- [ ] **Step 3: Promote k8s.io/utils to direct dependency**

Run: `go mod tidy`
Expected: no errors; `go.mod` shows `k8s.io/utils` without the `// indirect` comment.

- [ ] **Step 4: Verify the package compiles**

Run: `go build ./internal/circuit/...`
Expected: no output (success).

- [ ] **Step 5: Verify the empty test suite runs**

Run: `go test ./internal/circuit/... -v`
Expected: PASS, "Ran 0 of 0 Specs".

- [ ] **Step 6: Commit**

```bash
git add internal/circuit/breaker.go internal/circuit/suite_test.go go.mod go.sum
git commit -m "feat(circuit): scaffold per-resource circuit breaker package"
```

---

## Task 2: Implement token-bucket breaker logic

**Files:**
- Modify: `internal/circuit/breaker.go`
- Create: `internal/circuit/breaker_test.go`

- [ ] **Step 1: Write the failing test for Allow on a fresh key**

Write `internal/circuit/breaker_test.go`:

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

var _ = Describe("TokenBucketBreaker", func() {
	var (
		clk     *testclock.FakeClock
		params  circuit.BreakerParams
		breaker circuit.Breaker
		key     types.NamespacedName
	)

	BeforeEach(func() {
		clk = testclock.NewFakeClock(time.Unix(0, 0))
		params = circuit.BreakerParams{
			Burst:                 5,
			RefillRate:            1.0,
			Cooldown:              5 * time.Minute,
			HalfOpenProbeInterval: 30 * time.Second,
		}
		breaker = circuit.NewTokenBucketBreaker(params, clk)
		key = types.NamespacedName{Namespace: "ns", Name: "rr"}
	})

	It("allows a fresh key and starts closed", func() {
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.State(key)).To(Equal(circuit.StateClosed))
	})
})
```

- [ ] **Step 2: Run the test to confirm it fails on Allow behaviour**

Run: `go test ./internal/circuit/... -v -run TestCircuit`
Expected: PASS for `allows a fresh key` (stub returns true). Confirm the test executes — we want this as a regression baseline before adding more cases.

- [ ] **Step 3: Add the failing test for token exhaustion opening the breaker**

Add to `internal/circuit/breaker_test.go` inside the `Describe` block, after the existing `It`:

```go
	It("opens after burst is exhausted and stays open during cooldown", func() {
		// Drain all 5 tokens.
		for i := 0; i < 5; i++ {
			Expect(breaker.Allow(key)).To(BeTrue(), "drain %d", i)
		}
		// 6th call has no tokens → trips open.
		Expect(breaker.Allow(key)).To(BeFalse())
		Expect(breaker.State(key)).To(Equal(circuit.StateOpen))

		// During cooldown, still open.
		clk.Step(4 * time.Minute)
		Expect(breaker.Allow(key)).To(BeFalse())
		Expect(breaker.State(key)).To(Equal(circuit.StateOpen))
	})
```

- [ ] **Step 4: Run the test, expect failure**

Run: `go test ./internal/circuit/... -v`
Expected: FAIL — stub `Allow` always returns true, so drain test fails on the 6th call.

- [ ] **Step 5: Implement the token-bucket Allow logic**

Replace the `Allow`, `State`, and `UpdateParams` stubs in `internal/circuit/breaker.go` with:

```go
import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
)

type tokenBucketBreaker struct {
	mu     sync.Mutex
	params BreakerParams
	clock  clock.Clock
	keys   map[types.NamespacedName]*bucketState
}

func (b *tokenBucketBreaker) Allow(key types.NamespacedName) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.params.Disabled {
		return true
	}

	now := b.clock.Now()
	st, ok := b.keys[key]
	if !ok {
		st = &bucketState{
			tokens:     b.params.Burst,
			lastRefill: now,
			state:      StateClosed,
		}
		b.keys[key] = st
	}

	// Refill.
	elapsed := now.Sub(st.lastRefill).Seconds()
	if elapsed > 0 {
		st.tokens = minFloat(b.params.Burst, st.tokens+elapsed*b.params.RefillRate)
		st.lastRefill = now
	}

	switch st.state {
	case StateOpen:
		if now.Sub(st.openedAt) < b.params.Cooldown {
			return false
		}
		// Cooldown elapsed → move to half-open.
		st.state = StateHalfOpen
		st.lastProbe = now
		return true
	case StateHalfOpen:
		if now.Sub(st.lastProbe) < b.params.HalfOpenProbeInterval {
			return false
		}
		st.lastProbe = now
		return true
	}

	if st.tokens < 1 {
		st.state = StateOpen
		st.openedAt = now
		return false
	}
	st.tokens--
	return true
}

func (b *tokenBucketBreaker) State(key types.NamespacedName) State {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.keys[key]
	if !ok {
		return StateClosed
	}
	return st.state
}

func (b *tokenBucketBreaker) UpdateParams(params BreakerParams) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.params = params
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 6: Run tests, expect pass**

Run: `go test ./internal/circuit/... -v`
Expected: PASS — both `It` blocks green.

- [ ] **Step 7: Add the failing test for Observe transitioning half-open → closed on success**

Add to the `Describe` block:

```go
	It("recovers from half-open to closed on a successful Observe", func() {
		// Trip open.
		for i := 0; i < 6; i++ {
			breaker.Allow(key)
		}
		Expect(breaker.State(key)).To(Equal(circuit.StateOpen))

		// Cooldown elapses → next Allow puts us in half-open.
		clk.Step(6 * time.Minute)
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.State(key)).To(Equal(circuit.StateHalfOpen))

		// Successful Observe closes the breaker and refills tokens to burst.
		breaker.Observe(key, true)
		Expect(breaker.State(key)).To(Equal(circuit.StateClosed))
	})

	It("re-opens from half-open on a failed Observe", func() {
		for i := 0; i < 6; i++ {
			breaker.Allow(key)
		}
		clk.Step(6 * time.Minute)
		breaker.Allow(key) // → half-open

		breaker.Observe(key, false)
		Expect(breaker.State(key)).To(Equal(circuit.StateOpen))
	})

	It("ignores Observe in the closed state", func() {
		breaker.Allow(key)
		breaker.Observe(key, false)
		Expect(breaker.State(key)).To(Equal(circuit.StateClosed))
	})

	It("returns true unconditionally when Disabled", func() {
		breaker.UpdateParams(circuit.BreakerParams{Disabled: true})
		for i := 0; i < 100; i++ {
			Expect(breaker.Allow(key)).To(BeTrue())
		}
	})
```

- [ ] **Step 8: Run tests, expect failure on Observe**

Run: `go test ./internal/circuit/... -v`
Expected: FAIL — `Observe` is still a stub.

- [ ] **Step 9: Implement Observe**

Replace the `Observe` stub in `internal/circuit/breaker.go` with:

```go
func (b *tokenBucketBreaker) Observe(key types.NamespacedName, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.keys[key]
	if !ok {
		return
	}
	if st.state != StateHalfOpen {
		return
	}
	if success {
		st.state = StateClosed
		st.tokens = b.params.Burst
		st.lastRefill = b.clock.Now()
		return
	}
	st.state = StateOpen
	st.openedAt = b.clock.Now()
}
```

- [ ] **Step 10: Run tests, expect pass**

Run: `go test ./internal/circuit/... -v`
Expected: PASS — all six specs green.

- [ ] **Step 11: Commit**

```bash
git add internal/circuit/breaker.go internal/circuit/breaker_test.go
git commit -m "feat(circuit): implement token-bucket per-resource breaker"
```

---

## Task 3: MapFunc and Predicate wrappers

**Files:**
- Create: `internal/circuit/mapfunc.go`
- Create: `internal/circuit/predicate.go`
- Create: `internal/circuit/wrappers_test.go`

- [ ] **Step 1: Write the failing test for MapFunc wrapper passing through when allowed**

Write `internal/circuit/wrappers_test.go`:

```go
package circuit_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/syntasso/kratix/internal/circuit"
)

var _ = Describe("MapFunc wrapper", func() {
	var (
		clk     *testclock.FakeClock
		breaker circuit.Breaker
		inner   handler.MapFunc
		gated   handler.MapFunc
	)

	BeforeEach(func() {
		clk = testclock.NewFakeClock(time.Unix(0, 0))
		breaker = circuit.NewTokenBucketBreaker(circuit.BreakerParams{
			Burst:      2,
			RefillRate: 0.1,
			Cooldown:   time.Minute,
		}, clk)
		inner = func(_ context.Context, _ client.Object) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "rr"}}}
		}
		gated = circuit.MapFunc(inner, breaker)
	})

	It("passes through requests while the breaker is closed", func() {
		obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rr"}}
		Expect(gated(context.Background(), obj)).To(HaveLen(1))
	})

	It("drops requests once the breaker opens", func() {
		obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rr"}}
		// First two pass, third drained.
		gated(context.Background(), obj)
		gated(context.Background(), obj)
		Expect(gated(context.Background(), obj)).To(BeEmpty())
	})

	It("returns inner result unchanged when inner emits nothing", func() {
		emptyInner := func(_ context.Context, _ client.Object) []reconcile.Request { return nil }
		gated := circuit.MapFunc(emptyInner, breaker)
		obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rr"}}
		Expect(gated(context.Background(), obj)).To(BeNil())
	})
})

var _ = Describe("Predicate wrapper", func() {
	var (
		clk     *testclock.FakeClock
		breaker circuit.Breaker
		pred    interface {
			Create(event.CreateEvent) bool
			Update(event.UpdateEvent) bool
			Delete(event.DeleteEvent) bool
			Generic(event.GenericEvent) bool
		}
	)

	BeforeEach(func() {
		clk = testclock.NewFakeClock(time.Unix(0, 0))
		breaker = circuit.NewTokenBucketBreaker(circuit.BreakerParams{
			Burst:      1,
			RefillRate: 0.01,
			Cooldown:   time.Minute,
		}, clk)
		pred = circuit.Predicate(breaker)
	})

	It("allows Create when breaker is closed and drops once open", func() {
		obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rr"}}
		Expect(pred.Create(event.CreateEvent{Object: obj})).To(BeTrue())
		Expect(pred.Create(event.CreateEvent{Object: obj})).To(BeFalse())
	})

	It("always allows Delete events even when the breaker is open", func() {
		obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "rr"}}
		// Trip open.
		pred.Create(event.CreateEvent{Object: obj})
		pred.Create(event.CreateEvent{Object: obj})

		Expect(pred.Delete(event.DeleteEvent{Object: obj})).To(BeTrue())
	})
})
```

- [ ] **Step 2: Run test, expect compile error (MapFunc/Predicate don't exist)**

Run: `go test ./internal/circuit/... -v`
Expected: FAIL with "undefined: circuit.MapFunc" and "undefined: circuit.Predicate".

- [ ] **Step 3: Implement MapFunc wrapper**

Write `internal/circuit/mapfunc.go`:

```go
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
```

- [ ] **Step 4: Implement Predicate wrapper**

Write `internal/circuit/predicate.go`:

```go
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
```

- [ ] **Step 5: Run tests, expect pass**

Run: `go test ./internal/circuit/... -v`
Expected: PASS — all MapFunc and Predicate specs green.

- [ ] **Step 6: Commit**

```bash
git add internal/circuit/mapfunc.go internal/circuit/predicate.go internal/circuit/wrappers_test.go
git commit -m "feat(circuit): add MapFunc and Predicate enqueue wrappers"
```

---

## Task 4: Wire breaker flags into cmd/main.go

**Files:**
- Modify: `cmd/main.go:115-180` (flag declarations and parsing) and `cmd/main.go:315-325` (PromiseReconciler construction)

- [ ] **Step 1: Declare breaker flag variables**

In `cmd/main.go`, find the block around line 115-125 that declares `dynamicRRMaxConcurrentReconciles` and add after it:

```go
var (
	circuitBreakerBurst       float64
	circuitBreakerRefillRate  float64
	circuitBreakerCooldown    time.Duration
	circuitBreakerEnabled     bool
)
```

(If `time` is not already imported in `cmd/main.go`, leave the import to be added when needed — controller-runtime durations are pulled in elsewhere; verify with `goimports` in step 4.)

- [ ] **Step 2: Register the flags**

In `cmd/main.go`, find the flag registrations around line 164-180 and add after `dynamic-rr-max-concurrent-reconciles`:

```go
	flag.Float64Var(&circuitBreakerBurst, "circuit-breaker-burst", 100,
		"Default token-bucket burst for per-resource circuit breakers. "+
			"Overridable per Promise via the kratix.io/circuit-breaker-burst annotation.")
	flag.Float64Var(&circuitBreakerRefillRate, "circuit-breaker-refill-rate", 1.0,
		"Default token-bucket refill rate (tokens per second). "+
			"Overridable per Promise via the kratix.io/circuit-breaker-refill annotation.")
	flag.DurationVar(&circuitBreakerCooldown, "circuit-breaker-cooldown", 5*time.Minute,
		"Default cooldown after a circuit breaker opens. "+
			"Overridable per Promise via the kratix.io/circuit-breaker-cooldown annotation.")
	flag.BoolVar(&circuitBreakerEnabled, "circuit-breaker-enabled", true,
		"Global kill switch for per-resource circuit breakers. When false, breakers are constructed but always Allow.")
```

- [ ] **Step 3: Pass breaker defaults to PromiseReconciler**

In `cmd/main.go`, find the `PromiseReconciler` construction around line 315-325 and add the `BreakerDefaults` field:

```go
		DynamicRRMaxConcurrentReconciles: dynamicRRMaxConcurrentReconciles,
		BreakerDefaults: controller.BreakerDefaults{
			Burst:                 circuitBreakerBurst,
			RefillRate:            circuitBreakerRefillRate,
			Cooldown:              circuitBreakerCooldown,
			HalfOpenProbeInterval: 30 * time.Second,
			Enabled:               circuitBreakerEnabled,
		},
```

- [ ] **Step 4: Run goimports to ensure imports are clean**

Run: `goimports -w cmd/main.go`
Expected: no output; file rewritten with any missing imports added.

- [ ] **Step 5: Verify cmd/main.go compiles**

Run: `go build ./cmd/...`
Expected: error — `controller.BreakerDefaults` is undefined (we add it in Task 5). Confirm the error message matches; this is the expected pre-Task-5 state.

- [ ] **Step 6: Commit (deferred — bundle with Task 5 since it currently breaks build)**

Skip the commit here. We will land cmd/main.go and PromiseReconciler changes in a single commit at the end of Task 5 so the tree compiles between commits.

---

## Task 5: Add BreakerDefaults + annotation resolver to PromiseReconciler

**Files:**
- Modify: `internal/controller/promise_controller.go` (struct around line 81-95, helpers near the top of the file)
- Create: `internal/controller/promise_breaker.go` — new helper file to keep `promise_controller.go` from growing further.

- [ ] **Step 1: Add BreakerDefaults type and field**

Write `internal/controller/promise_breaker.go`:

```go
package controller

import (
	"strconv"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/circuit"
	"github.com/syntasso/kratix/internal/logging"
)

const (
	AnnotationCircuitBreakerBurst    = "kratix.io/circuit-breaker-burst"
	AnnotationCircuitBreakerRefill   = "kratix.io/circuit-breaker-refill"
	AnnotationCircuitBreakerCooldown = "kratix.io/circuit-breaker-cooldown"
	AnnotationCircuitBreakerDisabled = "kratix.io/circuit-breaker-disabled"
)

// BreakerDefaults are the operator-level defaults applied to every Promise's
// breaker. Annotations on a Promise override individual fields.
type BreakerDefaults struct {
	Burst                 float64
	RefillRate            float64
	Cooldown              time.Duration
	HalfOpenProbeInterval time.Duration
	Enabled               bool
}

// ResolveBreakerParams resolves the effective BreakerParams for a Promise.
// Returns the params plus a list of human-readable warning strings for
// annotations that failed to parse.
func ResolveBreakerParams(promise *v1alpha1.Promise, defaults BreakerDefaults) (circuit.BreakerParams, []string) {
	params := circuit.BreakerParams{
		Burst:                 defaults.Burst,
		RefillRate:            defaults.RefillRate,
		Cooldown:              defaults.Cooldown,
		HalfOpenProbeInterval: defaults.HalfOpenProbeInterval,
		Disabled:              !defaults.Enabled,
	}
	var warnings []string

	ann := promise.GetAnnotations()
	if ann == nil {
		return params, nil
	}

	if v, ok := ann[AnnotationCircuitBreakerBurst]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			params.Burst = f
		} else {
			warnings = append(warnings, AnnotationCircuitBreakerBurst+": invalid value "+v+" (want positive float)")
		}
	}
	if v, ok := ann[AnnotationCircuitBreakerRefill]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			params.RefillRate = f
		} else {
			warnings = append(warnings, AnnotationCircuitBreakerRefill+": invalid value "+v+" (want positive float)")
		}
	}
	if v, ok := ann[AnnotationCircuitBreakerCooldown]; ok {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			params.Cooldown = d
		} else {
			warnings = append(warnings, AnnotationCircuitBreakerCooldown+": invalid duration "+v)
		}
	}
	if v, ok := ann[AnnotationCircuitBreakerDisabled]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			params.Disabled = b
		} else {
			warnings = append(warnings, AnnotationCircuitBreakerDisabled+": invalid bool "+v)
		}
	}

	return params, warnings
}

// emitBreakerWarnings records each warning on the Promise as a Warning Event
// and logs it. Safe to call with a nil recorder (logs only).
func emitBreakerWarnings(recorder record.EventRecorder, log logr.Logger, promise *v1alpha1.Promise, warnings []string) {
	for _, w := range warnings {
		logging.Info(log, "promise breaker annotation warning", "promise", promise.GetName(), "warning", w)
		if recorder != nil {
			recorder.Event(promise, corev1.EventTypeWarning, "CircuitBreakerAnnotationInvalid", w)
		}
	}
}
```

- [ ] **Step 2: Add BreakerDefaults to PromiseReconciler struct**

In `internal/controller/promise_controller.go` around line 86-90 (after the `DynamicRRMaxConcurrentReconciles` field), add:

```go
	// BreakerDefaults are applied to every dynamic resource-request controller's
	// per-resource circuit breaker. Per-Promise annotations override these.
	BreakerDefaults BreakerDefaults
```

- [ ] **Step 3: Write unit test for the resolver**

Add to `internal/controller/promise_controller_test.go` (in a new top-level `Describe` block at the end of the file):

```go
var _ = Describe("ResolveBreakerParams", func() {
	var (
		defaults controller.BreakerDefaults
		promise  *v1alpha1.Promise
	)

	BeforeEach(func() {
		defaults = controller.BreakerDefaults{
			Burst:                 100,
			RefillRate:            1.0,
			Cooldown:              5 * time.Minute,
			HalfOpenProbeInterval: 30 * time.Second,
			Enabled:               true,
		}
		promise = &v1alpha1.Promise{
			ObjectMeta: metav1.ObjectMeta{Name: "p1"},
		}
	})

	It("returns defaults when no annotations are set", func() {
		params, warnings := controller.ResolveBreakerParams(promise, defaults)
		Expect(warnings).To(BeEmpty())
		Expect(params.Burst).To(Equal(float64(100)))
		Expect(params.RefillRate).To(Equal(1.0))
		Expect(params.Cooldown).To(Equal(5 * time.Minute))
		Expect(params.Disabled).To(BeFalse())
	})

	It("applies all annotation overrides when valid", func() {
		promise.SetAnnotations(map[string]string{
			controller.AnnotationCircuitBreakerBurst:    "200",
			controller.AnnotationCircuitBreakerRefill:   "2.5",
			controller.AnnotationCircuitBreakerCooldown: "10m",
			controller.AnnotationCircuitBreakerDisabled: "true",
		})
		params, warnings := controller.ResolveBreakerParams(promise, defaults)
		Expect(warnings).To(BeEmpty())
		Expect(params.Burst).To(Equal(float64(200)))
		Expect(params.RefillRate).To(Equal(2.5))
		Expect(params.Cooldown).To(Equal(10 * time.Minute))
		Expect(params.Disabled).To(BeTrue())
	})

	It("falls back to defaults and reports warnings for invalid values", func() {
		promise.SetAnnotations(map[string]string{
			controller.AnnotationCircuitBreakerBurst:    "not-a-number",
			controller.AnnotationCircuitBreakerCooldown: "10x",
		})
		params, warnings := controller.ResolveBreakerParams(promise, defaults)
		Expect(warnings).To(HaveLen(2))
		Expect(params.Burst).To(Equal(float64(100)))
		Expect(params.Cooldown).To(Equal(5 * time.Minute))
	})

	It("treats Enabled=false as Disabled=true", func() {
		defaults.Enabled = false
		params, _ := controller.ResolveBreakerParams(promise, defaults)
		Expect(params.Disabled).To(BeTrue())
	})
})
```

- [ ] **Step 4: Run the new spec, expect pass**

Run: `go test ./internal/controller/... -v -ginkgo.focus="ResolveBreakerParams"`
Expected: PASS — all four `It` blocks green.

- [ ] **Step 5: Verify the full controller package still builds**

Run: `go build ./...`
Expected: still failing on `dynamic_resource_request_controller.go` if we haven't touched it yet — but `cmd/main.go` should now compile because `BreakerDefaults` exists. Confirm cmd builds:

Run: `go build ./cmd/...`
Expected: success.

- [ ] **Step 6: Commit cmd/main.go + resolver together**

```bash
git add cmd/main.go internal/controller/promise_breaker.go internal/controller/promise_controller.go internal/controller/promise_controller_test.go
git commit -m "feat(controller): add circuit breaker defaults flag and annotation resolver"
```

---

## Task 6: Store breaker on DynamicResourceRequestController and call Observe in Reconcile

**Files:**
- Modify: `internal/controller/dynamic_resource_request_controller.go` (struct around line 80-95, Reconcile entry around line 96)

- [ ] **Step 1: Add Breaker field to the struct**

In `internal/controller/dynamic_resource_request_controller.go`, find the `DynamicResourceRequestController` struct (near line 80) and add a `Breaker` field next to `EventRecorder`:

```go
	EventRecorder              record.EventRecorder
	Breaker                    circuit.Breaker
```

Add the import:

```go
	"github.com/syntasso/kratix/internal/circuit"
```

- [ ] **Step 2: Add Observe deferred call in Reconcile**

Find the start of `Reconcile` around line 96. It currently uses named returns:

```go
func (r *DynamicResourceRequestController) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
```

Immediately after the signature line, add:

```go
	defer func() {
		if r.Breaker != nil {
			r.Breaker.Observe(req.NamespacedName, retErr == nil)
		}
	}()
```

- [ ] **Step 3: Verify the controller compiles**

Run: `go build ./internal/controller/...`
Expected: success.

- [ ] **Step 4: Run the existing dynamic_resource_request_controller_test suite to confirm Observe doesn't break existing flows**

Run: `go test ./internal/controller/... -v -ginkgo.focus="DynamicResourceRequestController"`
Expected: PASS — `r.Breaker` is nil in existing tests, so the deferred Observe is a no-op.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/dynamic_resource_request_controller.go
git commit -m "feat(controller): observe reconcile outcomes against per-promise breaker"
```

---

## Task 7: Construct breaker per Promise in ensureDynamicControllerIsStarted

**Files:**
- Modify: `internal/controller/promise_controller.go:1139-1256` (the body of `ensureDynamicControllerIsStarted`)

- [ ] **Step 1: Add breaker construction at the top of the new-controller branch**

In `internal/controller/promise_controller.go`, find the new-controller branch (line ~1169 onward, after the `logging.Info(logger, "starting dynamic controller")` line). Before constructing `dynamicResourceRequestController`, add:

```go
	breakerParams, warnings := ResolveBreakerParams(promise, r.BreakerDefaults)
	emitBreakerWarnings(r.Manager.GetEventRecorderFor("PromiseController"), logger, promise, warnings)
	breaker := circuit.NewTokenBucketBreaker(breakerParams, nil)
```

Add the import at the top of the file:

```go
	"github.com/syntasso/kratix/internal/circuit"
```

- [ ] **Step 2: Pass breaker to the controller**

Inside the `&DynamicResourceRequestController{...}` literal (around line 1173-1189), add:

```go
		Breaker: breaker,
```

- [ ] **Step 3: Wrap the three MapFuncs and add a predicate to the For() source**

Replace the existing builder chain (lines ~1194-1248) with a version that wraps each MapFunc through `circuit.MapFunc` and adds `circuit.Predicate(breaker)` to the primary source.

For the primary source — extend the existing predicate slice:

```go
	primaryPredicates := []predicate.Predicate{circuit.Predicate(breaker)}
	if r.DynamicRRFilterNoOpWrites {
		primaryPredicates = append(primaryPredicates, dynamicRRNoOpWriteFilter())
	}
	dynamicControllerBuilder := ctrl.NewControllerManagedBy(r.Manager).
		For(unstructuredCRD, builder.WithPredicates(primaryPredicates...))
```

(Add `"sigs.k8s.io/controller-runtime/pkg/predicate"` to imports if not already present.)

For the three `Watches(...)` calls, wrap each `handler.EnqueueRequestsFromMapFunc(...)`:

```go
		Watches(
			&batchv1.Job{},
			handler.EnqueueRequestsFromMapFunc(circuit.MapFunc(r.jobEventHandler(promise), breaker)),
			builder.WithPredicates(kratixManagedJobPredicate()),
		).
		Watches(
			&v1alpha1.Work{},
			handler.EnqueueRequestsFromMapFunc(circuit.MapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				work := obj.(*v1alpha1.Work)
				rrName, labelExists := work.Labels[v1alpha1.ResourceNameLabel]
				if !labelExists || work.Labels[v1alpha1.PromiseNameLabel] != promise.GetName() {
					return nil
				}
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: work.Namespace,
						Name:      rrName,
					},
				}}
			}, breaker)),
			builder.WithPredicates(dynamicRRWorkPredicate()),
		).
		Watches(
			&v1alpha1.ResourceBinding{},
			handler.EnqueueRequestsFromMapFunc(circuit.MapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				resourceBinding := obj.(*v1alpha1.ResourceBinding)
				rrName, labelExists := resourceBinding.Labels[v1alpha1.ResourceNameLabel]
				if !labelExists || resourceBinding.Labels[v1alpha1.PromiseNameLabel] != promise.GetName() {
					return nil
				}
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: resourceBinding.Namespace,
						Name:      rrName,
					},
				}}
			}, breaker)),
			builder.WithPredicates(dynamicRRResourceBindingPredicate())).
		Build(dynamicResourceRequestController)
```

- [ ] **Step 4: Run go build to confirm the file compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 5: Run the existing PromiseReconciler suite to confirm no regressions**

Run: `go test ./internal/controller/... -v -ginkgo.focus="PromiseReconciler"`
Expected: PASS — existing tests don't set annotations, so they get default breaker params (`Burst=100`, generous enough to not interfere).

- [ ] **Step 6: Commit**

```bash
git add internal/controller/promise_controller.go
git commit -m "feat(controller): gate dynamic RR enqueue paths through per-promise breaker"
```

---

## Task 8: Snapshot breaker params on StartedDynamicControllers and live-update on annotation change

**Files:**
- Modify: `internal/controller/dynamic_resource_request_controller.go` (struct)
- Modify: `internal/controller/promise_controller.go` (`ensureDynamicControllerIsStarted` reuse branch around line 1143-1167)

- [ ] **Step 1: Add LastBreakerParams field to the controller struct**

In `internal/controller/dynamic_resource_request_controller.go`, add to the struct:

```go
	// LastBreakerParams is the most recently applied set of breaker params.
	// Compared against the resolved params on every Promise reconcile so we
	// only call UpdateParams when something actually changed.
	LastBreakerParams circuit.BreakerParams
```

- [ ] **Step 2: Initialise LastBreakerParams when constructing the controller**

In `promise_controller.go`, in the new-controller branch (just after the `breaker := ...` line from Task 7), set:

```go
	dynamicResourceRequestController.LastBreakerParams = breakerParams
```

(Insert immediately after the `r.StartedDynamicControllers[controllerName] = dynamicResourceRequestController` line or right after the struct literal — wherever the controller is in scope. Put it before the function returns.)

- [ ] **Step 3: Add live-update path on the reuse branch**

In `promise_controller.go`, find the reuse branch (line ~1143-1167, inside `if r.dynamicControllerHasAlreadyStarted(...)`). After the existing field assignments and before the `if dynamicController.WatchStopped` check, add:

```go
		newParams, warnings := ResolveBreakerParams(promise, r.BreakerDefaults)
		emitBreakerWarnings(r.Manager.GetEventRecorderFor("PromiseController"), logger, promise, warnings)
		if newParams != dynamicController.LastBreakerParams {
			logging.Info(logger, "updating breaker params",
				"promise", promise.GetName(),
				"old", dynamicController.LastBreakerParams,
				"new", newParams)
			dynamicController.Breaker.UpdateParams(newParams)
			dynamicController.LastBreakerParams = newParams
		}
```

- [ ] **Step 4: Write the envtest case**

Add to `internal/controller/promise_controller_test.go` inside the existing Promise lifecycle `Describe` block (where other envtest specs live — locate by searching for `When("a Promise is created"`). Add a new `When` block:

```go
	When("a Promise's circuit-breaker annotations are updated", func() {
		It("calls UpdateParams on the running breaker without restarting the controller", func() {
			// Assumes a Promise has already been created in BeforeEach as `promise`
			// and the dynamic controller has started. Replace `promiseName` and
			// the lookup helper with whatever this suite already uses.
			By("annotating the Promise with a new burst")
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, promise)).To(Succeed())
			ann := promise.GetAnnotations()
			if ann == nil {
				ann = map[string]string{}
			}
			ann[controller.AnnotationCircuitBreakerBurst] = "250"
			promise.SetAnnotations(ann)
			Expect(k8sClient.Update(ctx, promise)).To(Succeed())

			By("waiting for the reconciler to apply the new params")
			Eventually(func(g Gomega) {
				dc, ok := promiseReconciler.StartedDynamicControllers[promise.GetDynamicControllerName(testLogger)]
				g.Expect(ok).To(BeTrue())
				g.Expect(dc.LastBreakerParams.Burst).To(Equal(float64(250)))
			}, "10s", "200ms").Should(Succeed())
		})
	})
```

(Adjust the variable names — `promiseReconciler`, `testLogger`, `k8sClient`, `ctx` — to match the existing test scaffolding. If the existing test file exposes the reconciler differently, mirror that pattern.)

- [ ] **Step 5: Run the envtest, expect pass**

Run: `go test ./internal/controller/... -v -ginkgo.focus="circuit-breaker annotations are updated"`
Expected: PASS within 10 seconds.

- [ ] **Step 6: Run the full controller suite to check for regressions**

Run: `go test ./internal/controller/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/controller/dynamic_resource_request_controller.go internal/controller/promise_controller.go internal/controller/promise_controller_test.go
git commit -m "feat(controller): live-update per-promise breaker params on annotation change"
```

---

## Task 9: Teardown — drop breaker reference when Promise is deleted

**Files:**
- Modify: `internal/controller/promise_controller.go:1263-1295` (`stopDynamicControllerForDeletedPromise`)

- [ ] **Step 1: Read the existing teardown function**

Run: `sed -n '1263,1310p' internal/controller/promise_controller.go`

Confirm the function deletes the entry from `r.StartedDynamicControllers`. The breaker is owned by the controller value, so dropping the map entry GCs it — no explicit work needed beyond a log line.

- [ ] **Step 2: Add a debug log line confirming breaker drop**

In `stopDynamicControllerForDeletedPromise`, immediately before the line that deletes from `r.StartedDynamicControllers`, add:

```go
	if dynamicController.Breaker != nil {
		logging.Debug(logger, "releasing per-promise breaker", "promise", promise.GetName())
	}
```

- [ ] **Step 3: Write a goleak-style regression test**

Add to `internal/controller/promise_controller_test.go` inside the deletion `Describe` block:

```go
	It("releases the breaker when the Promise is deleted", func() {
		controllerName := promise.GetDynamicControllerName(testLogger)
		Expect(promiseReconciler.StartedDynamicControllers).To(HaveKey(controllerName))

		Expect(k8sClient.Delete(ctx, promise)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(promiseReconciler.StartedDynamicControllers).NotTo(HaveKey(controllerName))
		}, "10s", "200ms").Should(Succeed())
	})
```

- [ ] **Step 4: Run the test**

Run: `go test ./internal/controller/... -v -ginkgo.focus="releases the breaker"`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/promise_controller.go internal/controller/promise_controller_test.go
git commit -m "feat(controller): release per-promise breaker on promise deletion"
```

---

## Task 10: End-to-end manual smoke

**Files:**
- Use: existing `test/perf/` harness if applicable, or `config/samples/kind-platform-config.yaml`.

- [ ] **Step 1: Build and load the operator image into the dev kind cluster**

Run: `make docker-build kind-load-image` (or whatever the project's standard local-deploy target is — check `Makefile`).
Expected: build succeeds; image loaded.

- [ ] **Step 2: Deploy with circuit breaker disabled (no-op upgrade check)**

Deploy the operator with `--circuit-breaker-enabled=false` set in the manager Deployment args. Apply any existing Promise + RR fixtures.
Expected: behaviour matches pre-change baseline; no events, no log noise about breakers.

- [ ] **Step 3: Flip breaker on with generous defaults**

Roll the operator with `--circuit-breaker-enabled=true` and defaults (`burst=100`, `refill=1.0`, `cooldown=5m`).
Expected: steady-state traffic unaffected; `kubectl get events` shows no `CircuitBreakerAnnotationInvalid` events on healthy Promises.

- [ ] **Step 4: Force a hot resource**

Pick one RR and rapidly toggle a label in a tight loop (>2/sec for ~2 minutes):

```bash
for i in $(seq 1 200); do
  kubectl annotate <rr-kind>/<rr-name> -n <ns> kratix.io/smoke="$i" --overwrite
done
```

Expected: after ~100 events, the breaker for that RR's key opens. Operator logs show `breaker state transition` to `open`. Sibling RRs of the same Promise continue to reconcile.

- [ ] **Step 5: Update the Promise to widen the burst via annotation**

```bash
kubectl annotate promise <name> kratix.io/circuit-breaker-burst="500" --overwrite
```

Expected: operator log shows `updating breaker params` with old=100 new=500. No controller restart in logs.

- [ ] **Step 6: Update annotation to an invalid value**

```bash
kubectl annotate promise <name> kratix.io/circuit-breaker-burst="garbage" --overwrite
```

Expected: Warning Event `CircuitBreakerAnnotationInvalid` on the Promise; breaker continues running with the previous good value (500). No controller crash.

- [ ] **Step 7: Document the smoke results**

Append a section to `docs/perf-flag-comparison.md` summarising what was observed (burst trip count, time to recover, event/log samples). Brief, ~10-15 lines.

- [ ] **Step 8: Commit the smoke notes only**

```bash
git add docs/perf-flag-comparison.md
git commit -m "docs(perf): record phase-1 circuit breaker smoke results"
```

---

## Self-review checklist (run before declaring Phase 1 done)

- [ ] `go test ./internal/circuit/... ./internal/controller/... -race` passes.
- [ ] `go vet ./...` clean.
- [ ] `golangci-lint run` clean (or matches project's normal warning floor).
- [ ] No new `// indirect` lines re-added to `go.mod` for `k8s.io/utils`.
- [ ] `cmd/main.go --help` lists the four new `--circuit-breaker-*` flags with sensible descriptions.
- [ ] An RR's `Reconcile` returning `nil` produces a successful `Observe`; an RR returning an error produces a failure `Observe`. Verified manually in step 4 of Task 10.
- [ ] Breaker is genuinely per-Promise: two Promises hitting their own hot resources at the same time don't interfere (verified by adding a second hot RR under a different Promise during smoke).
- [ ] Annotation removal restores defaults — `kubectl annotate promise <name> kratix.io/circuit-breaker-burst-` triggers UpdateParams back to the flag default.

## Out of scope for Phase 1 (handled in Phase 2 / Phase 3)

- Per-Promise workqueue rate limiter (exponential + token bucket composition).
- Per-Promise `MaxConcurrentReconciles` resolved from annotations.
- Restart-required Warning Events for QPS / concurrency annotation changes.
- Prometheus metrics (`kratix_circuit_breaker_state`, `kratix_circuit_breaker_trips_total`, etc.).
- `WatchCircuitOpen` status condition on the RR.
- Per-trip Warning Events on the underlying RR (currently emitted only as log lines; Phase 3 promotes to Events).
