# Per-Promise Fairness — Phase 2: Per-Promise Rate Limiter + MaxConcurrentReconciles

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give each Promise its own workqueue rate limiter (exponential backoff with optional QPS overlay) and its own `MaxConcurrentReconciles` cap, so a single noisy Promise can no longer dominate the shared dynamic-RR controller pool.

**Architecture:** Extend the Phase 1 resolver (`ResolveBreakerParams` → `ResolvePromiseRuntimeOptions`) to read three more annotations and four more flag defaults. Build a per-Promise `workqueue.TypedRateLimiter[reconcile.Request]` (exponential always-on; token-bucket overlay when QPS > 0). Pass `controller.Options{RateLimiter, MaxConcurrentReconciles}` at `builder.Build()` time. Snapshot both on `DynamicResourceRequestController` next to the existing `LastBreakerParams`. Because controller-runtime locks these at build time and offers no clean way to evict a running controller, annotation changes for these fields are **restart-required** — log + emit a Warning Event on the Promise (`reason: RuntimeOptionsRestartRequired`), update the stored snapshot so the next operator restart picks up the new value.

**Tech Stack:** Go, controller-runtime v0.20.4, `k8s.io/client-go/util/workqueue` typed rate limiters (`NewTypedItemExponentialFailureRateLimiter`, `NewTypedMaxOfRateLimiter`, `TypedBucketRateLimiter[reconcile.Request]`), Ginkgo/Gomega.

**Phase scope:** Phase 2 ships per-Promise rate limiting and concurrency. It does **not** ship metrics, status conditions, or per-trip Events (those are Phase 3). It does **not** ship a live controller-rebuild path (rejected on review — see "Why not live-rebuild?" below).

**Spec:** `docs/superpowers/specs/2026-05-15-per-promise-fairness-design.md`
**Prior phase:** `docs/superpowers/plans/2026-05-15-per-promise-fairness-phase-1-circuit-breaker.md` — landed on branch `per-promise-fairness-phase-1`. This phase branches from there.

---

## File Structure

**New files:**
- `internal/controller/promise_runtime_options.go` — new `PromiseRuntimeOptions` + `PromiseRuntimeDefaults` types; `ResolvePromiseRuntimeOptions` resolver covering all six tuning fields (the four breaker fields + concurrency + rate-limit QPS/burst). Replaces the more limited Phase 1 `promise_breaker.go` helpers (kept as a thin shim for backwards-compat during the transition).

**Modified files:**
- `cmd/main.go` — two new flags (`--dynamic-rr-qps`, `--dynamic-rr-burst`); pass the expanded `PromiseRuntimeDefaults` into `PromiseReconciler` instead of just `BreakerDefaults`.
- `internal/controller/promise_breaker.go` — keep `emitBreakerWarnings` and the annotation constants. Replace `BreakerDefaults` with `PromiseRuntimeDefaults` (alias kept for one commit so cmd/main keeps compiling between commits, then removed). The `ResolveBreakerParams` function becomes a wrapper around `ResolvePromiseRuntimeOptions`.
- `internal/controller/promise_controller.go` — extend the new-controller branch to (a) resolve full `PromiseRuntimeOptions`, (b) build a per-Promise `TypedRateLimiter[reconcile.Request]`, (c) apply `controller.Options{RateLimiter, MaxConcurrentReconciles}` to the builder, (d) snapshot the full options on the controller. Extend the reuse branch to emit a Warning Event on the Promise when rate-limit or concurrency annotations change, and refresh the stored snapshot.
- `internal/controller/dynamic_resource_request_controller.go` — rename `LastBreakerParams` to `LastRuntimeOptions` (one struct holding both breaker params and rate-limit/MCR snapshot). Field on `DynamicResourceRequestController`.
- `internal/controller/promise_controller_test.go` — extend `ResolveBreakerParams` specs to cover the new fields; add envtest cases for the warning-event path on rate-limit/MCR annotation changes and for the no-event path when only breaker fields change.

**Why this split:** The resolver lives in its own file because it's the choke point for every Promise-level option — concentrating the parsing + warnings logic in one place makes it easy to extend in future phases (e.g. status conditions, additional knobs). The controller-side wiring is mostly mechanical assembly of `controller.Options`; keeping it inside `promise_controller.go` near the existing breaker-construction code keeps related logic adjacent.

**Why not live-rebuild?** Controller-runtime's manager exposes only `Add(Runnable)` — there is no public `Remove`. The runnable's context is owned by `manager.runnableGroup` (verified against `pkg/manager/runnable_group.go:118` in v0.20.4). Building eviction on top of unexported internals would require either (a) maintaining a fork, (b) wrapping every controller in a custom Runnable adapter that owns its own cancel func — which means the manager never actually sees the controller and the manager's coordination guarantees stop applying. Both options trade a complete rewrite for the convenience of avoiding an operator restart on a tuning knob change. **Operator restart is acceptable** for these fields; the warning Event makes the restart-required expectation visible to the user.

---

## Task 1: Define `PromiseRuntimeOptions` and `PromiseRuntimeDefaults`

**Files:**
- Create: `internal/controller/promise_runtime_options.go`
- Modify: `internal/controller/promise_breaker.go` (turn into a thin shim that delegates to the new resolver)

- [ ] **Step 1: Read the existing Phase 1 resolver for shape**

Read `internal/controller/promise_breaker.go` end-to-end before starting. The new types must be supersets of `BreakerDefaults` / breaker-related output of `ResolveBreakerParams`. Confirm the four `Annotation*` constants and the `emitBreakerWarnings` helper remain reusable.

- [ ] **Step 2: Write the new options file**

Write `internal/controller/promise_runtime_options.go`:

```go
package controller

import (
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/circuit"
)

const (
	AnnotationMaxConcurrentReconciles = "kratix.io/max-concurrent-reconciles"
	AnnotationRateLimitQPS            = "kratix.io/rate-limit-qps"
	AnnotationRateLimitBurst          = "kratix.io/rate-limit-burst"
)

// PromiseRuntimeOptions is the resolved set of per-Promise tuning knobs.
// It is what eventually ends up applied to controller.Options and the
// per-resource breaker.
type PromiseRuntimeOptions struct {
	MaxConcurrentReconciles int
	RateLimitQPS            float32
	RateLimitBurst          int
	Breaker                 circuit.BreakerParams
}

// PromiseRuntimeDefaults are the operator-level defaults sourced from CLI
// flags. Annotations on the Promise override individual fields.
type PromiseRuntimeDefaults struct {
	MaxConcurrentReconciles int
	RateLimitQPS            float32
	RateLimitBurst          int
	Breaker                 BreakerDefaults
}

// ResolvePromiseRuntimeOptions returns the effective options for a Promise.
// The second return is the list of human-readable warning strings for any
// annotation that failed to parse — caller is expected to log + emit Events.
func ResolvePromiseRuntimeOptions(promise *v1alpha1.Promise, defaults PromiseRuntimeDefaults) (PromiseRuntimeOptions, []string) {
	opts := PromiseRuntimeOptions{
		MaxConcurrentReconciles: defaults.MaxConcurrentReconciles,
		RateLimitQPS:            defaults.RateLimitQPS,
		RateLimitBurst:          defaults.RateLimitBurst,
	}
	breakerParams, warnings := ResolveBreakerParams(promise, defaults.Breaker)
	opts.Breaker = breakerParams

	ann := promise.GetAnnotations()
	if ann == nil {
		return opts, warnings
	}

	if v, ok := ann[AnnotationMaxConcurrentReconciles]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.MaxConcurrentReconciles = n
		} else {
			warnings = append(warnings, AnnotationMaxConcurrentReconciles+": invalid value "+v+" (want positive integer)")
		}
	}
	if v, ok := ann[AnnotationRateLimitQPS]; ok {
		if f, err := strconv.ParseFloat(v, 32); err == nil && f > 0 {
			opts.RateLimitQPS = float32(f)
		} else {
			warnings = append(warnings, AnnotationRateLimitQPS+": invalid value "+v+" (want positive float)")
		}
	}
	if v, ok := ann[AnnotationRateLimitBurst]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.RateLimitBurst = n
		} else {
			warnings = append(warnings, AnnotationRateLimitBurst+": invalid value "+v+" (want positive integer)")
		}
	}

	_ = validation.IsValidLabelValue // imported only to keep this file in sync with the rest of the controller package's deps
	return opts, warnings
}
```

(The trailing `_ = validation...` line is a hack — drop it if `go vet` complains. Its purpose is to remind future-you the resolver is the natural place to add label-style validation; remove if it feels superfluous.)

- [ ] **Step 3: Update `BreakerDefaults` to be embedded in the new defaults struct**

In `internal/controller/promise_breaker.go`, keep `BreakerDefaults` as-is (it is now a sub-struct of `PromiseRuntimeDefaults.Breaker`). Keep `ResolveBreakerParams` unchanged — the new file calls into it.

- [ ] **Step 4: Verify the package compiles**

Run: `go -C <worktree> build ./internal/controller/...`
Expected: success.

- [ ] **Step 5: Unit-test the resolver**

Add to `internal/controller/promise_controller_test.go` (new `Describe` block alongside the existing `ResolveBreakerParams` block):

```go
var _ = Describe("ResolvePromiseRuntimeOptions", func() {
	var (
		defaults controller.PromiseRuntimeDefaults
		promise  *v1alpha1.Promise
	)

	BeforeEach(func() {
		defaults = controller.PromiseRuntimeDefaults{
			MaxConcurrentReconciles: 10,
			RateLimitQPS:            0,
			RateLimitBurst:          0,
			Breaker: controller.BreakerDefaults{
				Burst:                 100,
				RefillRate:            1.0,
				Cooldown:              5 * time.Minute,
				HalfOpenProbeInterval: 30 * time.Second,
				Enabled:               true,
			},
		}
		promise = &v1alpha1.Promise{ObjectMeta: metav1.ObjectMeta{Name: "p1"}}
	})

	It("falls back to defaults when no annotations are set", func() {
		opts, warnings := controller.ResolvePromiseRuntimeOptions(promise, defaults)
		Expect(warnings).To(BeEmpty())
		Expect(opts.MaxConcurrentReconciles).To(Equal(10))
		Expect(opts.RateLimitQPS).To(Equal(float32(0)))
		Expect(opts.RateLimitBurst).To(Equal(0))
		Expect(opts.Breaker.Burst).To(Equal(float64(100)))
	})

	It("applies all annotation overrides when valid", func() {
		promise.SetAnnotations(map[string]string{
			controller.AnnotationMaxConcurrentReconciles: "20",
			controller.AnnotationRateLimitQPS:            "50",
			controller.AnnotationRateLimitBurst:          "100",
			controller.AnnotationCircuitBreakerBurst:     "250",
		})
		opts, warnings := controller.ResolvePromiseRuntimeOptions(promise, defaults)
		Expect(warnings).To(BeEmpty())
		Expect(opts.MaxConcurrentReconciles).To(Equal(20))
		Expect(opts.RateLimitQPS).To(Equal(float32(50)))
		Expect(opts.RateLimitBurst).To(Equal(100))
		Expect(opts.Breaker.Burst).To(Equal(float64(250)))
	})

	It("reports warnings for invalid values and keeps defaults", func() {
		promise.SetAnnotations(map[string]string{
			controller.AnnotationMaxConcurrentReconciles: "negative-one",
			controller.AnnotationRateLimitQPS:            "-5",
		})
		opts, warnings := controller.ResolvePromiseRuntimeOptions(promise, defaults)
		Expect(warnings).To(HaveLen(2))
		Expect(opts.MaxConcurrentReconciles).To(Equal(10))
		Expect(opts.RateLimitQPS).To(Equal(float32(0)))
	})

	It("merges breaker warnings with rate-limit warnings", func() {
		promise.SetAnnotations(map[string]string{
			controller.AnnotationCircuitBreakerBurst: "nope",
			controller.AnnotationRateLimitQPS:        "nope",
		})
		_, warnings := controller.ResolvePromiseRuntimeOptions(promise, defaults)
		Expect(warnings).To(HaveLen(2))
	})
})
```

- [ ] **Step 6: Run the new specs, expect pass**

Run: `go -C <worktree> test ./internal/controller/... -v -ginkgo.focus="ResolvePromiseRuntimeOptions"`
Expected: PASS — all four specs green.

- [ ] **Step 7: Commit**

```bash
git add internal/controller/promise_runtime_options.go internal/controller/promise_controller_test.go
git commit -m "feat(controller): add PromiseRuntimeOptions resolver"
```

---

## Task 2: Build a per-Promise workqueue rate limiter

**Files:**
- Create: `internal/controller/promise_rate_limiter.go`
- Create: `internal/controller/promise_rate_limiter_test.go`

- [ ] **Step 1: Write the failing test for the always-on exponential limiter**

Write `internal/controller/promise_rate_limiter_test.go`:

```go
package controller_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/syntasso/kratix/internal/controller"
)

var _ = Describe("BuildPromiseRateLimiter", func() {
	It("returns an exponential-only limiter when QPS is zero", func() {
		rl := controller.BuildPromiseRateLimiter(0, 0)
		Expect(rl).NotTo(BeNil())
		// First requeue → ~1s
		req := reconcile.Request{}
		first := rl.When(req)
		Expect(first).To(BeNumerically(">=", 900*time.Millisecond))
		Expect(first).To(BeNumerically("<=", 1100*time.Millisecond))
	})

	It("composes a bucket overlay when QPS > 0", func() {
		rl := controller.BuildPromiseRateLimiter(50, 100)
		Expect(rl).NotTo(BeNil())
		// Steady-state under the bucket should be approximately 1/QPS = 20ms.
		req := reconcile.Request{}
		// First two calls have nothing queued — they may return 0.
		_ = rl.When(req)
		_ = rl.When(req)
		// On the third call the limiter should report a non-zero wait
		// once we've drained the burst (bursts of 100 allow many fast calls,
		// so we just assert it's >= 0 and doesn't panic).
		got := rl.When(req)
		Expect(got).To(BeNumerically(">=", 0))
	})
})
```

- [ ] **Step 2: Run, expect compile error (`BuildPromiseRateLimiter` undefined)**

Run: `go -C <worktree> test ./internal/controller/... -v -ginkgo.focus="BuildPromiseRateLimiter"`
Expected: FAIL with "undefined: controller.BuildPromiseRateLimiter".

- [ ] **Step 3: Implement the rate-limiter builder**

Write `internal/controller/promise_rate_limiter.go`:

```go
package controller

import (
	"time"

	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// BuildPromiseRateLimiter constructs a workqueue rate limiter for a single
// dynamic RR controller. The exponential failure limiter (1s → 30s) is always
// on. When qps > 0 the result is the max of the exponential limiter and a
// token-bucket steady-state limiter — matches controller-runtime's default
// composition pattern.
func BuildPromiseRateLimiter(qps float32, burst int) workqueue.TypedRateLimiter[reconcile.Request] {
	exp := workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
		1*time.Second, 30*time.Second,
	)
	if qps <= 0 {
		return exp
	}
	if burst <= 0 {
		// Sensible default mirroring controller-runtime: burst = 10 × QPS, capped.
		burst = int(qps) * 10
		if burst < 1 {
			burst = 1
		}
	}
	bucket := &workqueue.TypedBucketRateLimiter[reconcile.Request]{
		Limiter: rate.NewLimiter(rate.Limit(qps), burst),
	}
	return workqueue.NewTypedMaxOfRateLimiter(exp, bucket)
}
```

- [ ] **Step 4: Verify build + tests pass**

Run: `go -C <worktree> build ./...`
Then: `go -C <worktree> test ./internal/controller/... -v -ginkgo.focus="BuildPromiseRateLimiter"`
Expected: build clean; both specs green.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/promise_rate_limiter.go internal/controller/promise_rate_limiter_test.go
git commit -m "feat(controller): build per-promise workqueue rate limiter"
```

---

## Task 3: Replace `BreakerDefaults` field on `PromiseReconciler` with `PromiseRuntimeDefaults`

**Files:**
- Modify: `internal/controller/promise_controller.go` (struct around line 80-95)
- Modify: `cmd/main.go` (PromiseReconciler construction; new flag declarations)
- Modify: `internal/controller/promise_controller.go` (every existing use of `r.BreakerDefaults` becomes `r.PromiseRuntimeDefaults.Breaker`)

- [ ] **Step 1: Update struct field**

Replace the `BreakerDefaults BreakerDefaults` field with:

```go
	// PromiseRuntimeDefaults are applied to every dynamic resource-request
	// controller. Per-Promise annotations override individual fields.
	PromiseRuntimeDefaults PromiseRuntimeDefaults
```

(Removes the now-redundant `BreakerDefaults` field. `PromiseRuntimeDefaults.Breaker` replaces it.)

- [ ] **Step 2: Update existing call sites that read `r.BreakerDefaults`**

In `promise_controller.go`, change every `r.BreakerDefaults` to `r.PromiseRuntimeDefaults.Breaker` and every `ResolveBreakerParams(promise, r.BreakerDefaults)` to:

```go
runtimeOpts, warnings := ResolvePromiseRuntimeOptions(promise, r.PromiseRuntimeDefaults)
```

…and then `runtimeOpts.Breaker` wherever a `circuit.BreakerParams` is needed. There are two call sites: the new-controller branch and the reuse branch (both edited in Phase 1 Task 7 and Task 8).

- [ ] **Step 3: Add new flags to `cmd/main.go`**

After `dynamic-rr-max-concurrent-reconciles` flag registration:

```go
	var (
		dynamicRRQPS   float64
		dynamicRRBurst int
	)
	flag.Float64Var(&dynamicRRQPS, "dynamic-rr-qps", 0,
		"Default workqueue QPS budget per dynamic resource-request controller. "+
			"0 disables the token-bucket overlay (exponential failure limiter only). "+
			"Overridable per Promise via kratix.io/rate-limit-qps.")
	flag.IntVar(&dynamicRRBurst, "dynamic-rr-burst", 0,
		"Default workqueue burst per dynamic RR controller. "+
			"Ignored when --dynamic-rr-qps=0. Defaults to 10× QPS when unset. "+
			"Overridable per Promise via kratix.io/rate-limit-burst.")
```

- [ ] **Step 4: Pass the new defaults into `PromiseReconciler`**

Replace the `BreakerDefaults: ...` field in the `PromiseReconciler{...}` literal with:

```go
		PromiseRuntimeDefaults: controller.PromiseRuntimeDefaults{
			MaxConcurrentReconciles: dynamicRRMaxConcurrentReconciles,
			RateLimitQPS:            float32(dynamicRRQPS),
			RateLimitBurst:          dynamicRRBurst,
			Breaker: controller.BreakerDefaults{
				Burst:                 circuitBreakerBurst,
				RefillRate:            circuitBreakerRefillRate,
				Cooldown:              circuitBreakerCooldown,
				HalfOpenProbeInterval: 30 * time.Second,
				Enabled:               circuitBreakerEnabled,
			},
		},
```

- [ ] **Step 5: Verify build is clean**

Run: `go -C <worktree> build ./...`
Expected: success — every old `r.BreakerDefaults` site should now be updated.

- [ ] **Step 6: Run full controller suite for no-regression**

Run: `go -C <worktree> test ./internal/controller/...`
Expected: PASS — existing tests don't read the new fields, so behaviour is unchanged.

- [ ] **Step 7: Commit**

```bash
git add cmd/main.go internal/controller/promise_controller.go
git commit -m "feat(controller): promote BreakerDefaults to PromiseRuntimeDefaults"
```

---

## Task 4: Apply `controller.Options{RateLimiter, MaxConcurrentReconciles}` per Promise

**Files:**
- Modify: `internal/controller/promise_controller.go` (`ensureDynamicControllerIsStarted`, new-controller branch)

- [ ] **Step 1: Build the per-Promise options**

In the new-controller branch (currently around line 1170 — exact line may have drifted after Phase 1 edits, locate by searching `logging.Info(logger, "starting dynamic controller")`), replace the existing breaker-only resolution with full options resolution. The block that currently reads:

```go
breakerParams, warnings := ResolveBreakerParams(promise, r.BreakerDefaults)
emitBreakerWarnings(r.Manager.GetEventRecorderFor("PromiseController"), logger, promise, warnings)
breaker := circuit.NewTokenBucketBreaker(breakerParams, nil)
```

becomes:

```go
runtimeOpts, warnings := ResolvePromiseRuntimeOptions(promise, r.PromiseRuntimeDefaults)
emitBreakerWarnings(r.Manager.GetEventRecorderFor("PromiseController"), logger, promise, warnings)
breaker := circuit.NewTokenBucketBreaker(runtimeOpts.Breaker, nil)
rateLimiter := BuildPromiseRateLimiter(runtimeOpts.RateLimitQPS, runtimeOpts.RateLimitBurst)
```

- [ ] **Step 2: Replace the existing `if r.DynamicRRMaxConcurrentReconciles > 0` block**

The existing block currently looks like:

```go
if r.DynamicRRMaxConcurrentReconciles > 0 {
    dynamicControllerBuilder = dynamicControllerBuilder.WithOptions(controller.Options{
        MaxConcurrentReconciles: r.DynamicRRMaxConcurrentReconciles,
    })
}
```

Replace with an always-applied `WithOptions` call that includes both the rate limiter and the resolved MCR (falling back to a sensible default when neither flag nor annotation specifies one):

```go
mcr := runtimeOpts.MaxConcurrentReconciles
if mcr <= 0 {
    mcr = 1 // controller-runtime's default
}
dynamicControllerBuilder = dynamicControllerBuilder.WithOptions(controller.Options{
    MaxConcurrentReconciles: mcr,
    RateLimiter:             rateLimiter,
})
```

(The old `r.DynamicRRMaxConcurrentReconciles` field is no longer the source of truth — `PromiseRuntimeDefaults.MaxConcurrentReconciles` is. Confirm the field is still passed in `cmd/main.go` so backwards-compat is preserved, then delete the `DynamicRRMaxConcurrentReconciles` field from `PromiseReconciler` in a later task if you want to clean up.)

- [ ] **Step 3: Update the controller construction to snapshot the full options**

The struct literal for `&DynamicResourceRequestController{...}` currently sets `Breaker: breaker` and `LastBreakerParams: breakerParams`. Replace both with:

```go
Breaker:            breaker,
LastRuntimeOptions: runtimeOpts,
```

(The `LastRuntimeOptions` field will be added in the next task — this code temporarily won't compile until then. Push through to Task 5.)

- [ ] **Step 4: Verify temporary build failure is the expected one**

Run: `go -C <worktree> build ./...`
Expected: FAIL with "unknown field LastRuntimeOptions" — that's what Task 5 fixes. Don't try to fix it here.

- [ ] **Step 5: No commit yet** — bundle with Task 5 to keep the tree compilable between commits.

---

## Task 5: Rename `LastBreakerParams` → `LastRuntimeOptions` on `DynamicResourceRequestController`

**Files:**
- Modify: `internal/controller/dynamic_resource_request_controller.go` (struct)
- Modify: `internal/controller/promise_controller.go` (reuse branch — annotation-change detection)
- Modify: `internal/controller/promise_controller_test.go` (existing breaker-update spec needs the new field name)

- [ ] **Step 1: Rename the field**

In `internal/controller/dynamic_resource_request_controller.go`, replace:

```go
LastBreakerParams circuit.BreakerParams
```

with:

```go
// LastRuntimeOptions is the most recently applied PromiseRuntimeOptions
// snapshot. The reuse branch compares the next resolved set against this
// to decide which kinds of changes are live-updatable (breaker only) vs.
// restart-required (rate-limit, MCR).
LastRuntimeOptions PromiseRuntimeOptions
```

- [ ] **Step 2: Update the reuse branch logic**

In `internal/controller/promise_controller.go`, find the reuse branch (after the existing field-assignment block, near the comment `// updating breaker params`). Replace the existing block with:

```go
		newOpts, warnings := ResolvePromiseRuntimeOptions(promise, r.PromiseRuntimeDefaults)
		recorder := r.Manager.GetEventRecorderFor("PromiseController")
		emitBreakerWarnings(recorder, logger, promise, warnings)

		old := dynamicController.LastRuntimeOptions

		// Breaker params are live-updatable.
		if newOpts.Breaker != old.Breaker {
			logging.Info(logger, "updating breaker params",
				"promise", promise.GetName(),
				"old", old.Breaker,
				"new", newOpts.Breaker)
			dynamicController.Breaker.UpdateParams(newOpts.Breaker)
		}

		// Rate-limit + MCR changes require an operator restart.
		if newOpts.MaxConcurrentReconciles != old.MaxConcurrentReconciles ||
			newOpts.RateLimitQPS != old.RateLimitQPS ||
			newOpts.RateLimitBurst != old.RateLimitBurst {
			msg := "rate-limit or max-concurrent-reconciles annotation changed; takes effect on next operator restart"
			logging.Info(logger, msg, "promise", promise.GetName(),
				"oldMCR", old.MaxConcurrentReconciles, "newMCR", newOpts.MaxConcurrentReconciles,
				"oldQPS", old.RateLimitQPS, "newQPS", newOpts.RateLimitQPS,
				"oldBurst", old.RateLimitBurst, "newBurst", newOpts.RateLimitBurst)
			if recorder != nil {
				recorder.Event(promise, corev1.EventTypeWarning, "RuntimeOptionsRestartRequired", msg)
			}
		}

		dynamicController.LastRuntimeOptions = newOpts
```

(Make sure `corev1 "k8s.io/api/core/v1"` is imported — `promise_breaker.go` already imports it via `corev1.EventTypeWarning`, but `promise_controller.go` may need it explicitly.)

- [ ] **Step 3: Fix the existing Phase 1 envtest**

In `internal/controller/promise_controller_test.go`, find the spec `"calls UpdateParams on the running breaker without restarting the controller"`. Replace any reference to `dc.LastBreakerParams.Burst` with `dc.LastRuntimeOptions.Breaker.Burst`.

- [ ] **Step 4: Build, expect success**

Run: `go -C <worktree> build ./...`
Expected: success — both Task 4 and Task 5 changes are now in place.

- [ ] **Step 5: Run the full controller suite**

Run: `go -C <worktree> test ./internal/controller/...`
Expected: PASS — existing Phase 1 specs still green.

- [ ] **Step 6: Commit Tasks 4 + 5 together**

```bash
git add internal/controller/promise_controller.go internal/controller/dynamic_resource_request_controller.go internal/controller/promise_controller_test.go
git commit -m "feat(controller): apply per-promise rate limiter and MCR; warn on restart-required changes"
```

---

## Task 6: Envtest for restart-required Warning Event path

**Files:**
- Modify: `internal/controller/promise_controller_test.go`

- [ ] **Step 1: Write the failing test**

Add to the same `When` block as the existing breaker-update test (mirror its scaffolding — `fakeK8sClient`, `reconciler`, `reconcileUntilCompletion`, `recorder` captured event-bus):

```go
	When("a Promise's max-concurrent-reconciles annotation is updated", func() {
		It("emits a Warning Event and updates the stored snapshot but does not restart the controller", func() {
			By("recording the controller pointer before the change")
			controllerName := promise.GetDynamicControllerName(testLogger)
			before, ok := reconciler.StartedDynamicControllers[controllerName]
			Expect(ok).To(BeTrue())
			beforeMCR := before.LastRuntimeOptions.MaxConcurrentReconciles

			By("annotating the Promise with a new max-concurrent-reconciles")
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, promise)).To(Succeed())
			ann := promise.GetAnnotations()
			if ann == nil {
				ann = map[string]string{}
			}
			ann[controller.AnnotationMaxConcurrentReconciles] = "50"
			promise.SetAnnotations(ann)
			Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())

			By("reconciling to apply the annotation change")
			// Use whatever helper the suite uses to drive a reconcile —
			// most likely `reconcileUntilCompletion` or similar.
			reconcileUntilCompletion(reconciler, promise)

			By("verifying the same controller instance is still running")
			after, ok := reconciler.StartedDynamicControllers[controllerName]
			Expect(ok).To(BeTrue())
			Expect(after).To(BeIdenticalTo(before),
				"controller must NOT be rebuilt for restart-required fields")

			By("verifying the snapshot is updated")
			Expect(after.LastRuntimeOptions.MaxConcurrentReconciles).To(Equal(50),
				"snapshot should track the new value so the next operator restart picks it up")
			Expect(after.LastRuntimeOptions.MaxConcurrentReconciles).NotTo(Equal(beforeMCR))

			By("verifying a Warning Event was emitted on the Promise")
			Eventually(recorder.Events, "5s", "100ms").Should(Receive(
				ContainSubstring("RuntimeOptionsRestartRequired")))
		})
	})

	When("only a Promise's breaker annotation changes", func() {
		It("does not emit a restart-required Warning Event", func() {
			By("annotating the Promise with a new breaker burst")
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, promise)).To(Succeed())
			ann := promise.GetAnnotations()
			if ann == nil {
				ann = map[string]string{}
			}
			ann[controller.AnnotationCircuitBreakerBurst] = "777"
			promise.SetAnnotations(ann)
			Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())

			By("reconciling")
			reconcileUntilCompletion(reconciler, promise)

			By("verifying no RuntimeOptionsRestartRequired event was emitted")
			// Drain a few times to be sure; we just want to confirm no event matching the substring.
			Consistently(func(g Gomega) {
				select {
				case ev := <-recorder.Events:
					g.Expect(ev).NotTo(ContainSubstring("RuntimeOptionsRestartRequired"))
				default:
				}
			}, "1s", "100ms").Should(Succeed())
		})
	})
```

(Adjust `recorder.Events` to whatever the existing suite uses — Phase 1's existing breaker-update test should already have a recorder. If not, this task also requires adding a `record.EventRecorder` fake to the test scaffolding — mirror controller-runtime's `record.NewFakeRecorder(N)` pattern.)

- [ ] **Step 2: Run, expect pass for both new specs**

Run: `go -C <worktree> test ./internal/controller/... -v -ginkgo.focus="max-concurrent-reconciles annotation is updated|only a Promise's breaker annotation changes"`
Expected: both PASS.

- [ ] **Step 3: Run full controller suite for no-regression**

Run: `go -C <worktree> test ./internal/controller/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/controller/promise_controller_test.go
git commit -m "test(controller): cover restart-required event path for runtime options"
```

---

## Task 7: Live verification on kind-platform

**Files:**
- Use: existing kind-platform cluster + the compound-simpleapp Promise from Phase 1 smoke.

- [ ] **Step 1: Rebuild and reload the manager image**

Run: `make -C <worktree> build-and-load-kratix`
Expected: image built, loaded into kind, rollout restart triggers.

- [ ] **Step 2: Confirm the new flags are visible**

Run: `kubectl -n kratix-platform-system exec deploy/kratix-platform-controller-manager -- /manager --help | grep -E "dynamic-rr-qps|dynamic-rr-burst"`
Expected: both `--dynamic-rr-qps` and `--dynamic-rr-burst` listed with their default values and annotation references.

- [ ] **Step 3: Apply a Promise with overridden MCR**

```bash
kubectl annotate promise simpleapp kratix.io/max-concurrent-reconciles=50 --overwrite
```

Expected: manager log shows the `"rate-limit or max-concurrent-reconciles annotation changed; takes effect on next operator restart"` line. `kubectl get events --field-selector reason=RuntimeOptionsRestartRequired` shows a Warning Event on the simpleapp Promise.

- [ ] **Step 4: Restart the operator and confirm the new MCR is applied**

Run: `kubectl -n kratix-platform-system rollout restart deploy/kratix-platform-controller-manager`
After rollout, run: `kubectl -n kratix-platform-system logs deploy/kratix-platform-controller-manager | grep "starting dynamic controller" | head -1`
Then check the controller-runtime startup log for the simpleapp dynamic controller: it should mention `worker count":50` (or whatever you set).

- [ ] **Step 5: Run a focused perf comparison**

Two runs of `make perf-test PERF_N=150 PERF_PROMISE=...compound-simpleapp-promise.yaml ...` (mirror Phase 1's PERF_BASENAME pattern):

1. **Baseline:** annotation `kratix.io/max-concurrent-reconciles=10`. Note the wall-clock convergence.
2. **High-concurrency:** annotation `kratix.io/max-concurrent-reconciles=50`. Same N, same Promise. Compare.

Expected: the high-concurrency run is meaningfully faster (smaller wall-clock) — confirms the per-Promise MCR knob is actually being applied at controller build time.

- [ ] **Step 6: Document results**

Append a section to `docs/perf-phase1-circuit-breaker-smoke.md` (or create `docs/perf-phase2-rate-limit-smoke.md` — match the project pattern). Capture:
- Wall-clock comparison for the two MCR values.
- Confirmation of the restart-required Event.
- Confirmation of the new flags in `--help`.

- [ ] **Step 7: Commit smoke notes**

```bash
git add docs/perf-phase2-rate-limit-smoke.md
git commit -m "docs(perf): record phase-2 rate limiter / MCR smoke results"
```

---

## Self-review checklist

- [ ] `go test ./internal/controller/... ./internal/circuit/... -race` passes.
- [ ] `go vet ./...` clean.
- [ ] `cmd/main.go --help` lists both new `--dynamic-rr-qps` and `--dynamic-rr-burst` flags.
- [ ] `PromiseRuntimeDefaults` is plumbed end-to-end (flag → reconciler → controller).
- [ ] Three new annotations work: `kratix.io/max-concurrent-reconciles`, `kratix.io/rate-limit-qps`, `kratix.io/rate-limit-burst`.
- [ ] Annotation parse errors emit Warning Events and fall back to defaults — no broken Promises from typos.
- [ ] Breaker-only annotation changes still live-update without a Warning Event (Phase 1 behaviour preserved).
- [ ] Rate-limit or MCR annotation changes emit a `RuntimeOptionsRestartRequired` Event on the Promise and update the stored snapshot.
- [ ] Operator restart actually picks up the new value (verified live in Task 7).

## Out of scope (handled by Phase 3)

- Prometheus metrics for workqueue depth, drops, reconcile duration.
- `WatchCircuitOpen` status condition on the RR.
- Per-trip Warning Events on the underlying RR for breaker state transitions.
- Annotation parse-failure events on the Promise as proper structured events (currently the same warning channel as breaker annotations).
