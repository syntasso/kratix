# Kratix controller perf rig

A reproducible scale test for the kratix controller reconcile path. Creates
N resource requests against a no-op Promise, captures controller `/metrics`
snapshots and the controller log throughout, then reduces both into a
summary.

## What this measures

- **Wall-clock to converge**: from "first RR created" to "every RR has
  `Reconciled=True`".
- **Reconciles per RR**: how many times each RR is reconciled before it
  settles, derived from controller log lines. This is the signal that
  uncovered the self-watch storm (~20 reconciles per RR).
- **Peak workqueue depth** and **peak longest-running processor** per
  controller, from `workqueue_depth` and
  `workqueue_longest_running_processor_seconds`.
- **`controller_runtime_reconcile_total` Δ** per controller across the run.

## Prerequisites

- `kind-platform` cluster with kratix installed (`make dev-env` or
  `make quick-start` will do it).
- The rig scrapes metrics over **HTTPS on :8443** with a bearer token (the
  default secure-metrics configuration). It auto-creates a ServiceAccount
  + ClusterRoleBinding (`perf-metrics-scraper` in `kratix-platform-system`,
  bound to `kratix-platform-metrics-reader`) and mints a short-lived
  token via TokenRequest. TLS verification is skipped.
- `kubectl` on PATH, with a context entry for the cluster.

## Run

```sh
# default: N=500, run dir = run-<unix-ts>
make perf-test

# 2500 RRs, named baseline-2500 in test/perf/results/baseline-2500/
PERF_N=2500 PERF_RUN=baseline-2500 PERF_TIMEOUT=60m make perf-test
```

Environment variables (with defaults):

| var | default | meaning |
|---|---|---|
| `PERF_N` | 500 | number of resource requests to create |
| `PERF_RUN` | `run-<ts>` | subdirectory under `test/perf/results/` |
| `PERF_CONTEXT` | `kind-platform` | kube context |
| `PERF_NAMESPACE` | `default` | namespace for RR creation |
| `PERF_TIMEOUT` | `30m` | convergence timeout |
| `PERF_BASENAME` | `perftest` | RR name prefix; RRs are `<base>-NNNNN` |

## Output

Each run creates `test/perf/results/<run>/` containing:

- `controller.log` — full tail of the controller pod during the run
- `metrics-T+NNNNNs.prom` — one Prometheus textfile snapshot per second
- `port-forward.log` — kubectl port-forward stdout/stderr
- `summary.md` — reduced markdown table (paste into findings docs)

## How convergence is detected

The dynamic RR controller sets `Reconciled=True` once both
`ConfigureWorkflowCompleted=True` and `WorksSucceeded=True`. The no-op
Promise has one trivial `busybox true` configure pipeline; the pipeline
produces no manifests, so:

1. Workflow Job completes immediately → `ConfigureWorkflowCompleted=True`.
2. No Works produced → `WorksSucceeded=True` vacuously.
3. → `Reconciled=True`.

The driver polls the RR list every 2s and reports `N/Total reconciled`
until all RRs converge or the timeout fires.

## Comparing runs

Two runs of the same N in different `PERF_RUN` directories can be compared
by diffing the two `summary.md` files. The current main `findings` doc was
produced by exactly this kind of comparison: `baseline-500` vs `patched-500`.

## When to use this rig

- Validating any perf-related change to a kratix controller (predicate
  filters, watch scope, status coalescing, MCR tuning).
- Reproducing regressions when someone reports "scale degraded".
- Generating before/after numbers for PR descriptions.

This rig is intentionally *not* in `go test ./...`. The `//go:build perf`
build tag keeps it out of normal test runs and CI; you opt in with
`make perf-test`.
