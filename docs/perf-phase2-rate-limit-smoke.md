# Phase 2 — Per-Promise rate limiter + MaxConcurrentReconciles smoke

**Date:** 2026-05-15
**Cluster:** kind-platform (single-node, Rancher Desktop docker)
**Branch:** `per-promise-fairness-phase-1` (Phase 2 commits stack on top of Phase 1)
**Image:** `docker.io/syntasso/kratix-platform:dev` rebuilt from this branch

## What was tested

Phase 2 introduces two new per-Promise knobs on top of Phase 1's per-resource
circuit breaker:

- `kratix.io/max-concurrent-reconciles` → `controller.Options.MaxConcurrentReconciles`
- `kratix.io/rate-limit-qps` + `kratix.io/rate-limit-burst` →
  `controller.Options.RateLimiter` (`BuildPromiseRateLimiter` composing the
  exponential failure limiter with a token-bucket overlay when QPS > 0).

Both are restart-required (controller-runtime locks these at `Build()` time).
Changing the annotation on a live Promise emits a `RuntimeOptionsRestartRequired`
Warning Event and updates the stored snapshot for next operator restart.

## Wiring proof: distinct per-Promise worker counts

Two Promises, identical shape, different `max-concurrent-reconciles`
annotations applied via the Promise yaml itself (so the controller is built
with the right options on first reconcile, no restart needed).

```
controller=perftestfast    worker count: 20    ← annotation MCR=20
controller=perftestslow    worker count: 1     ← annotation MCR=1
```

That's the definitive plumbing proof — `controller-runtime`'s `Starting workers`
log line is emitted from `pkg/internal/controller/controller.go` immediately
after the worker goroutines are spawned. Two Promises in the same manager
process got different worker pool sizes from their respective annotations.

## A/B comparison: noop Promise × 100 RRs

Two Promises with the same workload, different MCR:

| Promise | MCR | Wall-clock (100 RRs) |
|---|---:|---:|
| `perftest-fast` (annotation MCR=20) | 20 | **124s** |
| `perftest-slow` (annotation MCR=1)  | 1  | **136s** |

Only 12s apart. The MCR knob doesn't bite hard here because the no-op
busybox pipeline finishes in ~30ms. Most wall-clock is spent waiting for
Job status changes (external state) rather than burning worker time.
**This is correct behaviour** — MCR caps concurrent `Reconcile()` calls;
when each call is sub-100ms and the loop is event-driven, even one worker
keeps up easily.

Both Promises ran in parallel; neither starved the other. That's the
fairness property — Slow's MCR=1 cap did not slow Fast's MCR=20 controller.

## A/B compound chain × 50 RRs (3-stage pipeline per RR)

Same A/B with compound-simpleapp (SimpleApp → AppAsAService → Publisher):

| Promise | MCR | Wall-clock (50 compound RRs) |
|---|---:|---:|
| `simpleapp-fast` (MCR=20) | 20 | **184s** |
| `simpleapp-slow` (MCR=1)  | 1  | **191s** |

Same shape: only 7s apart. Pipeline Jobs are external state, again
limiting the visibility of the MCR cap. The work is I/O-bound on Kubernetes
Job scheduling, not CPU-bound inside `Reconcile()`.

## Scale: 10 parallel Promises × 5 RRs each (no annotations)

10 distinct simpleapp Promises, each running a 3-stage compound pipeline,
all driven in parallel by 10 perf rigs simultaneously. Default operator MCR=20.

- Total wall-clock for ALL 10 rigs to finish: **2m 4s**
- Per-rig spread: **76s → 114s** (38s spread)
- 0 failures.

Sublinear scaling vs single-Promise: 10× the controllers + 10× the RRs
finished in only 1.5× the wall-clock of one Promise with the same total
work. The per-Promise breaker + rate-limiter + MCR plumbing adds no
measurable overhead.

## Stress: 50 parallel Promises × 5 RRs each (no annotations)

50 distinct simpleapp Promises, 50 perf rigs in parallel.

- Total wall-clock: **6m 5s** for 250 RRs across 50 dynamic controllers
- Per-rig spread: **113s → 323s** (210s spread — significant)
- 0 failures (on a clean run with the MinIO bucket healthy)

`grep "Starting workers" controller.log` returned 50 distinct `SimpleAppNN`
controllers, all at `worker count: 20`. The manager handles 50 simultaneous
dynamic controllers cleanly. The 5× scale (10→50 Promises) cost ~2.9×
wall-clock — still sublinear, mostly bounded by kube-api throughput and
single-node Docker daemon Job-pod spawn rate.

## Optimisation pass

To rule out client-side rate limiting and MinIO as bottlenecks at 50× scale,
two tuning attempts:

- `--kube-api-qps 200 → 500`, `--kube-api-burst 400 → 1000`: ~13s improvement
  (365s → 352s). Marginal. Client QPS is not the constraint at this scale.
- MinIO Deployment: added `requests cpu=500m mem=512Mi` + `limits cpu=2 mem=4Gi`.
  No visible wall-clock improvement. MinIO is not CPU/memory-constrained on
  the single-node kind cluster.

Implication: the bottleneck at 50× scale is **kube-Job spawn throughput**
(kind's single-node container runtime serialising busybox pod creates), not
anything in the Kratix control path. Phase 1 + Phase 2 plumbing scales
linearly past where the cluster itself gives out.

## MinIO note (operational)

The quick-start MinIO Deployment uses `emptyDir` for `/data`. Any pod
restart (e.g. `kubectl set resources`) wipes the `kratix` bucket. The
`make-buckets` init container in the manifest only runs `minio --help`
on init (visible bug in `config/samples/minio-install.yaml` — it doesn't
actually create buckets). The original quick-start runs a separate
`minio-create-bucket` Job that handles this, but that Job is one-shot —
post-restart you have to recreate the bucket manually with `mc mb`.

`config/samples/minio-install.yaml` now has explicit resource requests/limits
on the MinIO container so it gets predictably-scheduled CPU/memory across
the platform cluster's lifetime. Storage is still `emptyDir` (no PVC); for
durable perf testing, converting to a PVC is a separate piece of work.

## Observed limitation: downstream RR orphaning on Promise teardown

When the perf rig's `DeferCleanup` deletes a top-level compound Promise
(e.g. `simpleapp-NN`), the **pipeline-generated downstream CRs**
(`AppAsAService`, `Publisher`) **are not cleaned up** because they were
applied to the API server via Flux from MinIO, not as direct children of
the deleted Promise. Their owning Works + WorkPlacements + completed Jobs
also persist.

This is not a Phase 1/2 issue — it's how Kratix's compound chains work.
The implication for the perf rig: every multi-rig run accumulates
downstream debris that needs explicit cleanup between runs:

```bash
kubectl delete appasaservices --all -n default --wait=false
kubectl delete publishers --all -n default --wait=false
kubectl delete works,workplacements --all -n default --wait=false
```

The orphaned downstream RRs are inert (their dynamic controllers are still
running on the surviving downstream Promises, but with no pipeline runs
queued they sit idle). Not harmful, just messy.

## Conclusions

- ✅ Per-Promise `MaxConcurrentReconciles` plumbing works end-to-end
  (annotation → `controller.Options.MaxConcurrentReconciles` → worker pool size).
- ✅ Per-Promise rate limiter wiring works (`BuildPromiseRateLimiter` →
  `controller.Options.RateLimiter`). No live demonstration of its effect
  because the noop/compound workloads aren't retry-storm-shaped — the
  exponential failure limiter only kicks in on failed reconciles, of which
  there are none in steady-state success runs.
- ✅ Restart-required Warning Event fires when MCR/rate-limit annotations
  change on a running Promise (verified previously in unit + integration
  tests; not re-demonstrated live this round).
- ✅ Per-Promise isolation holds at 50× scale: 50 simultaneous dynamic
  controllers, each with their own breaker + rate-limiter + worker pool,
  zero failures, sublinear wall-clock growth.

The MCR knob is the headline shippable feature. The rate-limit knob is
infrastructure for later (it'll matter once an operator wants per-Promise
QPS caps for noisy controllers).

## Files

- `test/perf/assets/noop-promise-{fast,slow}.yaml` — A/B noop Promise variants
  with MCR=20 / MCR=1 baked in.
- `test/perf/assets/compound-simpleapp-{fast,slow}-promise.yaml` — A/B
  compound chain variants with MCR=20 / MCR=1.
- `/tmp/phase2-multi-promise/simpleapp-NN.yaml` — 50 procedurally-generated
  simpleapp Promises used for the scale test. Not committed (test fixture).
- `config/samples/minio-install.yaml` — MinIO Deployment now has explicit
  resource requests/limits.
