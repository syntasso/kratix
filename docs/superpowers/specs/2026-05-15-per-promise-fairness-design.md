# Per-Promise Fairness for Dynamic Resource Request Controllers

**Status:** Draft
**Author:** Phill Morton
**Date:** 2026-05-15

## Problem

Kratix runs one `controller-runtime` manager that hosts a dynamically built controller per Promise (`PromiseReconciler.ensureDynamicControllerIsStarted`). Today every dynamic controller shares:

- One client-side rate limiter (`--kube-api-qps` / `--kube-api-burst`).
- A single global `--dynamic-rr-max-concurrent-reconciles` setting applied identically to every Promise.
- An implicit default workqueue rate limiter.

In a cluster with 6 Promises and ~5000 resources each, this produces head-of-line blocking between Promises and offers no defence against a single hot resource saturating its Promise's workqueue. Tuning QPS globally is a blunt instrument; one noisy Promise affects every other Promise.

## Goal

Give each Promise its own bounded share of reconcile capacity, with optional per-Promise overrides via annotations, and add a defence against a single misbehaving resource starving the rest of its Promise.

Non-goals:

- API-server-side fairness (covered by APF).
- Per-Promise pods or process isolation (option 3 from initial brainstorm — explicitly out of scope).
- Per-Promise REST client or informer cache (rejected; non-standard, see "Alternatives considered").

## Prior art

Crossplane is the closest analog (controller-per-XRD on a shared manager). Verified in `crossplane/crossplane@main`:

- One core pod, one manager, one shared cached + uncached client, one shared informer cache (`internal/engine/engine.go:50-81`).
- One named controller per XRD started as a goroutine via `ControllerEngine.Start` (`engine.go:~245`); each gets its own workqueue, rate limiter, and `MaxConcurrentReconciles`.
- Noisy-neighbour isolation is enforced at the workqueue layer + a per-XR token-bucket circuit breaker that drops watch events at enqueue (`internal/circuit/token_bucket.go`, `internal/circuit/mapfunc.go`).
- Default circuit breaker params: `burst=100`, `refillRate=1.0/sec`, `cooldown=5m` (`cmd/crossplane/core/core.go:118-120`).

This design applies the same model to Kratix's existing Promise → dynamic controller structure.

## Design

### Architecture

Single operator process, single manager, shared client, shared informer cache — unchanged. Isolation lives at the controller layer:

- **Per-Promise controller** (already exists). Extended with `controller.Options` for rate limiter and concurrency.
- **Per-Promise workqueue rate limiter.** `workqueue.TypedRateLimiter[reconcile.Request]` constructed per Promise. Exponential failure limiter (1s → 30s cap) always on; optional token-bucket overlay when QPS is annotation-specified.
- **Per-Promise `MaxConcurrentReconciles`.** Resolved per Promise; falls back to existing flag.
- **Per-resource circuit breaker.** New `internal/circuit` package. Token-bucket keyed by `types.NamespacedName`. Mounted at the *enqueue* layer — wraps `MapFunc`s in the existing `Watches(...)` calls. Delete events bypass.
- **`StartedDynamicControllers`** tracking extended with a `PromiseRuntimeOptions` snapshot for change detection.

What stays the same: client, cache, scheme, REST mapper, all watches and predicates, leader election, every `r.Client.*` call site in `DynamicResourceRequestController`.

### Components & API surface

**New package: `internal/circuit/`**

```go
type State int // closed | half-open | open

type Breaker interface {
    Allow(key types.NamespacedName) bool
    Observe(key types.NamespacedName, success bool)
    State(key types.NamespacedName) State
    UpdateParams(BreakerParams)
}

type BreakerParams struct {
    Burst       float64
    RefillRate  float64
    Cooldown    time.Duration
    HalfOpenProbeInterval time.Duration // default 30s
    Disabled    bool
}

func NewTokenBucketBreaker(BreakerParams, clock.Clock) Breaker

func MapFunc(inner handler.MapFunc, breaker Breaker) handler.MapFunc
func Predicate(breaker Breaker) predicate.Predicate
```

**New struct: `PromiseRuntimeOptions`** (in `internal/controller/`)

```go
type PromiseRuntimeOptions struct {
    MaxConcurrentReconciles int
    RateLimitQPS            float32
    RateLimitBurst          int
    CircuitBreakerBurst     float64
    CircuitBreakerRefill    float64
    CircuitBreakerCooldown  time.Duration
    CircuitBreakerDisabled  bool
}

type PromiseRuntimeDefaults struct {
    MaxConcurrentReconciles int
    RateLimitQPS            float32
    RateLimitBurst          int
    CircuitBreakerBurst     float64
    CircuitBreakerRefill    float64
    CircuitBreakerCooldown  time.Duration
    CircuitBreakerEnabled   bool
}

func ResolvePromiseRuntimeOptions(p *v1alpha1.Promise, defaults PromiseRuntimeDefaults) (PromiseRuntimeOptions, []string)
```

`PromiseRuntimeDefaults` is populated once at startup from the operator flags and passed through to every resolution call. `ResolvePromiseRuntimeOptions` returns a list of parse-warning strings for invalid annotations; the caller emits Events from those.

**Modified: `cmd/main.go`** — new flags (all optional, all default to current behaviour where applicable):

| Flag | Type | Default | Notes |
|---|---|---|---|
| `--dynamic-rr-qps` | float | 0 | 0 = no token-bucket overlay; exponential only |
| `--dynamic-rr-burst` | int | 0 | Ignored unless `--dynamic-rr-qps > 0` |
| `--circuit-breaker-burst` | float | 100 | Crossplane parity |
| `--circuit-breaker-refill-rate` | float | 1.0 | Crossplane parity |
| `--circuit-breaker-cooldown` | duration | 5m | Crossplane parity |
| `--circuit-breaker-enabled` | bool | true | Global kill switch |

`--dynamic-rr-max-concurrent-reconciles` already exists; reused as the per-Promise default.
`--kube-api-qps` / `--kube-api-burst` untouched (orthogonal — manager-wide client budget).

**Annotation schema (Promise CR)** — all optional, all override flag defaults:

```
kratix.io/max-concurrent-reconciles: "20"
kratix.io/rate-limit-qps:            "50"
kratix.io/rate-limit-burst:          "100"
kratix.io/circuit-breaker-burst:     "200"
kratix.io/circuit-breaker-refill:    "2.0"
kratix.io/circuit-breaker-cooldown:  "10m"
kratix.io/circuit-breaker-disabled:  "true"
```

Annotations chosen over spec fields: no CRD schema change, no API version bump, additive, easy to roll back. Promotion to spec fields is a future option if these become core to the Promise contract.

### Data flow

```
event source ──► predicate ──► MapFunc ──► [BREAKER] ──► workqueue ──► [RATE LIMITER] ──► reconcile worker
                              (existing)    (NEW)        (existing)    (NEW)              (existing pool, NEW size)
```

**Breaker placement.** Wraps the three existing `MapFunc`s in `ensureDynamicControllerIsStarted` (Job, Work, ResourceBinding). The primary `For(unstructuredCRD)` source has no MapFunc, so we attach a `predicate.Predicate` that calls `breaker.Allow(req.NamespacedName)` and returns false when open. Delete events bypass.

Rationale for enqueue-side: a single hot resource generating 100 events/sec must not fill the workqueue and starve sibling resources of the same Promise. Dropping at enqueue keeps the queue healthy.

**Rate limiter composition.** Always-on exponential failure limiter (`NewTypedItemExponentialFailureRateLimiter[reconcile.Request](1s, 30s)`). When `rate-limit-qps > 0`, compose with a `BucketRateLimiter` via `NewTypedMaxOfRateLimiter`. The bucket bounds steady-state; the exponential bounds retry storms.

**Observe hook.** At end of `Reconcile`:

```go
defer r.Breaker.Observe(req.NamespacedName, err == nil)
```

### Lifecycle & reconfiguration

**New Promise.** Build path unchanged structurally. Options resolved, limiter and breaker constructed, controller built with `controller.Options{RateLimiter, MaxConcurrentReconciles}`. Breaker reference stored on the `DynamicResourceRequestController`; options snapshot stored on `StartedDynamicControllers` entry.

**Annotation changed.** `PromiseReconciler` compares resolved options to stored snapshot:

| Change | Live update? | Action |
|---|---|---|
| `circuit-breaker-*` | Yes | `breaker.UpdateParams(...)`; per-resource state preserved |
| `max-concurrent-reconciles` | No | Stop + rebuild controller (existing `restartDynamicControllerWatch`) |
| `rate-limit-*` | No | Stop + rebuild controller |

Annotation parse errors → log + Event on the Promise; fall back to previous valid options. A typo must not brick a 5000-resource controller.

**Promise deleted.** Existing teardown path. Breaker registry entry dropped along with the controller; breaker is owned by the controller instance and GC'd naturally.

**Leader election.** Unchanged. The manager handles election; per-Promise controllers don't run on followers.

**Backwards compatibility.** Zero annotations on every Promise → behaviour matches today *except* every dynamic controller now also has the explicit workqueue exponential limiter (1s–30s) and the default circuit breaker. Defaults are generous (`burst=100`, `refill=1.0/sec`) — only catches pathological resources. Strict no-op upgrade: deploy with `--circuit-breaker-enabled=false`, observe, then flip on.

### Observability

**Metrics** (Prometheus, registered against `controller-runtime`'s registry):

| Metric | Type | Labels |
|---|---|---|
| `kratix_dynamic_rr_workqueue_depth` | gauge | `promise` |
| `kratix_dynamic_rr_workqueue_adds_total` | counter | `promise` |
| `kratix_dynamic_rr_workqueue_drops_total` | counter | `promise`, `reason` |
| `kratix_dynamic_rr_reconcile_duration_seconds` | histogram | `promise`, `result` |
| `kratix_circuit_breaker_state` | gauge | `promise`, `resource` (only when open/half-open) |
| `kratix_circuit_breaker_trips_total` | counter | `promise` |
| `kratix_promise_runtime_options` | gauge | `promise`, `option` |

Cardinality note: `kratix_circuit_breaker_state` only emits per-resource labels when state is open or half-open. Steady-state healthy resources contribute nothing.

**Logs & events.** Breaker state transitions logged at Info with old/new state and tokens remaining. Each trip emits a Warning Event on the underlying RR (`reason=CircuitBreakerOpen`); recovery emits Normal. Annotation parse failures emit Warning Events on the Promise.

**Status condition.** New `WatchCircuitOpen` condition on the RR when its breaker is open. Cleared when closed.

### Testing strategy

**Unit — `internal/circuit/`** (pure Go, injected clock):
- Token-bucket arithmetic: drain, refill, half-open probe timing, cooldown enforcement, deletion bypass.
- `MapFunc` wrapper: pass when closed, drop when open, deletes always pass.
- `Breaker.Observe` state machine transitions.

**Unit — `PromiseRuntimeOptions` resolver:**
- Annotation → struct mapping per field; invalid values fall back to defaults and produce a warning.
- Flag → struct fallback when annotation absent.
- Round-trip determinism.

**Integration — envtest, `internal/controller/`:**
- New Promise with `max-concurrent-reconciles=2` → built controller has correct options snapshot.
- Annotation change for breaker-only fields → no controller restart. Annotation change for QPS/concurrency → restart triggered.
- Promise deletion → breaker entry removed; no goroutine leaks (`goleak`).
- Two Promises with different concurrency caps reconciling concurrently → measured ratio within tolerance.

**Manual smoke** (Phill's 6-Promise × 5000-resource cluster):
- Apply existing state with new build, no annotations. Compare workqueue depth / reconcile p95 / API request rate to baseline.
- Add `kratix.io/max-concurrent-reconciles=20` to one Promise; observe queue drain faster while others stay steady.
- Force a hot resource; verify breaker trips, events drop, status condition appears, other resources of same Promise stay healthy.

### What this design does not address

- **API-server saturation.** All Promises still share one rest.Config / QPS. If the bottleneck is the API server, tune `--kube-api-qps` / APF; this design doesn't move that ceiling. Fairness here is *between Kratix controllers*, not at the API server boundary.
- **Slow reconciles.** If individual reconciles are slow (e.g. a 30s workflow Job), the limiter and breaker don't speed them up. They prevent one slow Promise starving others.
- **Manager-process CPU/memory pressure.** Still one pod. CPU-bound across all controllers requires horizontal scaling or per-Promise Deployments (out of scope).

## Alternatives considered

**Per-Promise REST client with custom QPS (Approach A in brainstorm).** Rejected. Verified non-standard across Crossplane, ArgoCD, Flux, Cluster API, OperatorSDK, Knative. `client.NewDelegatingClient` is deprecated since controller-runtime v0.15; modern `client.New` with shared cache works but no public operator does this. Footguns: scheme pointer must match manager's; REST mapper interactions; ongoing maintenance burden with no community reference. The actual symptom (queue contention, head-of-line blocking) is addressed by the workqueue-side approach.

**Per-Promise Manager / in-process or Deployment (Approaches 2 and 3 from brainstorm).** Rejected. Memory cost of duplicating informer caches × N Promises is high; per-Promise Deployments add operational overhead (RBAC, image plumbing, scaling). Workqueue isolation gets us most of the value at a fraction of the cost.

**APF (API Priority and Fairness).** Server-side, doesn't help client-side QPS contention. Complementary, not a substitute.

## Open questions

None as of design approval.
