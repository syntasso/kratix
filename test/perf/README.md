# Kratix controller perf rig

A reproducible scale test for the kratix controller reconcile path. Creates
N resource requests against a Promise, captures controller `/metrics`
snapshots and the controller log throughout, then reduces both into a
summary. `make perf-test` runs verbosely (`-ginkgo.v`) by default, so you'll
see live `STEP:` lines and periodic `N/Total reconciled` progress rather than
silence until the run ends.

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
  `make quick-start` will do it). `make quick-start` alone is enough for the
  `environment: dev` scenarios (the default `noop-promise.yaml` and friends);
  the `environment: platform` scenarios (the `compound-*` chain) need
  `make dev-env`, which additionally registers the platform cluster as its
  own destination.
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

# drive against a different scenario, leave it in place afterwards
PERF_PROMISE=assets/noop-promise-write.yaml PERF_PROMISE_NAME=perftest PERF_KEEP=true make perf-test
```

Environment variables (with defaults):

| var | default | meaning |
|---|---|---|
| `PERF_N` | 500 | number of resource requests to create (per Promise) |
| `PERF_RUN` | `run-<ts>` | subdirectory under `test/perf/results/` |
| `PERF_CONTEXT` | `kind-platform` | kube context |
| `PERF_NAMESPACE` | `default` | namespace for RR creation |
| `PERF_TIMEOUT` | `30m` | convergence timeout |
| `PERF_BASENAME` | `perftest` | RR name prefix; RRs are `<base>-NNNNN` |
| `PERF_KEEP` | `false` | skip cleanup; leave the Promise + RRs in place after the run |
| `PERF_NO_SCRAPE` | `false` | skip the `/metrics` scraper and summary reduction (use for concurrent rig runs against the same cluster) |
| `PERF_PROMISE` | `assets/noop-promise.yaml` | path to the Promise yaml to drive |
| `PERF_PROMISE_NAME` | `perftest` | name of the applied Promise object |
| `PERF_RR_GROUP` / `PERF_RR_VERSION` / `PERF_RR_KIND` | `perf.kratix.io` / `v1alpha1` / `PerfTest` | GVK of the resource request CRD the chosen Promise creates |

Two flags exist but aren't wired into the `make perf-test` target (invoke
`go test` directly if you need them — see `perf_suite_test.go`):

| flag | default | meaning |
|---|---|---|
| `-perf.promises` (`PERF_PROMISES`) | 1 | materialise N Promise variants from `-perf.promise` as a template (name + CRD GVK suffixed by index) and drive `-perf.n` RRs against each, concurrently |
| `-perf.downstream-kinds` / `-perf.downstream-group` / `-perf.downstream-version` / `-perf.downstream-timeout` | `""` / `""` / `v1alpha1` / `5m` | after RRs converge, poll these additional Kinds until each has `N (× Promises)` items — for scenarios whose pipeline schedules a downstream resource (e.g. `compound-simpleapp-promise.yaml` → `AppAsAService`) |

## Scenarios (`assets/`)

| file | destination | what it exercises |
|---|---|---|
| `noop-promise.yaml` | `environment: dev` | baseline: single trivial configure pipeline, no Works produced |
| `noop-promise-two.yaml` | `environment: dev` | same shape, separate CRD/Promise — run two flat scenarios side by side without colliding GVKs |
| `noop-promise-write.yaml` | `environment: dev` | pipeline writes a `ConfigMap` manifest, so `WorksSucceeded` is earned rather than vacuous |
| `status-test-promise.yaml` | `environment: dev` | pipeline writes `/kratix/metadata/status.yaml` — exercises the custom RR status path |
| `dbservice-promise.yaml` | `environment: dev` | pipeline reads `spec.engine`/`size`/`replicas` from the RR and writes a larger, templated status block |
| `compound-simpleapp-promise.yaml` (`-fast`/`-slow` variants) | `environment: platform` | two-pipeline workflow; second pipeline schedules a downstream `AppAsAService` RR — pair with `-perf.downstream-kinds=AppAsAService` |
| `compound-appasaservice-promise.yaml` | `environment: platform` | downstream of SimpleApp: writes a `ConfigMap` and schedules a further downstream `Publisher` RR |
| `compound-publisher-promise.yaml` | `environment: platform` | leaf of the compound chain |

## Output

Each run creates `test/perf/results/<run>/` containing:

- `controller.log` — full tail of the controller pod during the run
- `metrics-T+NNNNNs.prom` — one Prometheus textfile snapshot per second
- `port-forward.log` — kubectl port-forward stdout/stderr
- `summary.md` — reduced markdown table (paste into findings docs/PRs)

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
by diffing the two `summary.md` files, e.g. `baseline-500` vs `patched-500`.

## When to use this rig

- Validating any perf-related change to a kratix controller (predicate
  filters, watch scope, status coalescing, MCR tuning).
- Reproducing regressions when someone reports "scale degraded".
- Generating before/after numbers for PR descriptions.

This rig is intentionally *not* in `go test ./...`. The `//go:build perf`
build tag keeps it out of normal test runs and CI; you opt in with
`make perf-test`.
