# Phase 1 — Circuit breaker smoke results

**Date:** 2026-05-15
**Cluster:** kind-platform (single node, Rancher Desktop docker)
**Branch:** `per-promise-fairness-phase-1`
**Image:** `docker.io/syntasso/kratix-platform:dev` rebuilt from this branch

## What was tested

- Manager started with no `--circuit-breaker-*` flags set explicitly, so
  every dynamic RR controller picked up the defaults (`burst=100`,
  `refillRate=1`, `cooldown=5m`, `enabled=true`).
- `make perf-test PERF_N=250 PERF_RUN=phase1-circuit-smoke-250` against
  the existing perftest Promise.
- Live `kubectl annotate` of the easyapp Promise to tighten then remove
  breaker params, confirming live `UpdateParams` works.

## No-regression check (perf rig, N=250)

From `test/perf/results/phase1-circuit-smoke-250/summary.md`:

- Wall-clock to converge: **1m23s**
- Total reconciles (controller log): **3172**
- Reconciles per RR — p50: **13**, p95: **15**, p99: **15**, max: **16**
- Peak workqueue depth (perftest controller): **0**
- Peak longest-running reconcile: **0.000s**

Every RR sat well under the default `burst=100`. The breaker is wired into
every enqueue path (3 `Watches` MapFuncs + `For` predicate) and was
consulted on every event without trimming any genuine traffic.

## Wiring proof: live UpdateParams on annotation change

```
$ kubectl annotate promise easyapp \
    kratix.io/circuit-breaker-burst=2 \
    kratix.io/circuit-breaker-refill=0.1 \
    --overwrite
```

Manager log:

```json
{"msg":"updating breaker params","promise":"easyapp",
 "old":{"Burst":100,"RefillRate":1,"Cooldown":300000000000,
        "HalfOpenProbeInterval":30000000000,"Disabled":false},
 "new":{"Burst":2,"RefillRate":0.1,"Cooldown":300000000000,
        "HalfOpenProbeInterval":30000000000,"Disabled":false}}
```

```
$ kubectl annotate promise easyapp \
    kratix.io/circuit-breaker-burst- \
    kratix.io/circuit-breaker-refill-
```

Manager log:

```json
{"msg":"updating breaker params","promise":"easyapp",
 "old":{"Burst":2,"RefillRate":0.1,...},
 "new":{"Burst":100,"RefillRate":1,...}}
```

This proves the full chain end-to-end:

1. `--circuit-breaker-*` flag defaults reach `PromiseReconciler.BreakerDefaults`.
2. `ResolveBreakerParams` parses Promise annotations and overrides defaults.
3. The reuse branch in `ensureDynamicControllerIsStarted` detects param
   changes against `LastBreakerParams` and calls `UpdateParams` on the
   already-running breaker — no controller restart.
4. Annotation removal correctly reverts to the flag defaults.

## What this smoke does NOT cover

- Forcing the breaker to actually *trip* (state transition closed → open).
  At burst=100, the perf rig with single-shot RR creation never approaches
  the threshold. Phase 3 will add Prometheus metrics
  (`kratix_circuit_breaker_trips_total`) which give a passive signal even
  when the breaker rarely fires; for Phase 1 we rely on unit tests in
  `internal/circuit/` (token-bucket arithmetic, half-open recovery, etc.)
  to cover the state machine.
- Multi-Promise fairness. The whole motivation for the feature is two+
  Promises competing under load — testing that needs Phase 2's
  per-Promise rate limiter + `MaxConcurrentReconciles` to be meaningful.

## Files

- `test/perf/results/phase1-circuit-smoke-250/` — full perf rig output
  (controller.log, summary.md, /metrics snapshots).
