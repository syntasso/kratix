# Cascade perf scenario — design

Add a second perf rig scenario that drives a three-Promise cascade
(`SimpleApp → AppAsAService → EvoPublisher`) end-to-end, so we can measure
controller behaviour under a workload that exercises cross-Promise scheduling
and multi-pipeline workflows — not just the no-op single-Promise case the
current rig measures.

## Goals

- Reproducible perf signal for a realistic kratix flow: one user-facing
  Promise that schedules another Promise's RR mid-workflow, which schedules
  a third Promise's RR.
- Isolate **controller** cost from external-service cost: all pipeline
  containers are bash mocks (no network, no real CI, no real publisher
  call).
- Reuse every existing rig primitive (scraper, metrics-auth, reducer,
  flag-setting script) — the new scenario is additive, not a rewrite.

## Non-goals

- Real GitHub or publisher integration. Pipelines mock these with bash.
- Blocking dependent flows. Pipelines emit child manifests and exit
  immediately; no pipeline waits on a downstream RR.
- Generalised "scenario harness" with N pluggable workloads. Two scenarios
  is not enough to justify the abstraction; if/when we add a third, revisit
  via the rule of three.
- New controller knobs. The cascade scenario uses whichever flag-set is
  currently on the controller (operator patches via the existing
  `set-controller-flags.sh`).

## Workload shape

Per source `SimpleApp` RR the rig drives, the controller does:

```
SimpleApp/simpleapp-NNNNN
  ├─ pipeline-1 (build-via-github): bash loop, writes /kratix/metadata/status.yaml
  ├─ pipeline-2 (schedule-aaas):    reads status, writes /kratix/output/aaas.yaml
  │                                 → kratix work-creator applies it
  └─ → triggers
AppAsAService/simpleapp-NNNNN
  ├─ pipeline-1 (schedule-evopub):  writes /kratix/output/evopub.yaml
  │                                 → kratix work-creator applies it
  └─ → triggers
EvoPublisher/simpleapp-NNNNN
  └─ pipeline-1 (publish):          mock publish call (no-op)
```

Per source RR ≈ **3 RRs on the cluster**, **4 pipelines**, **4 Jobs**, **4 Pods**.
For `PERF_N=100` that's 300 RRs and 400 Pods through the controller — about
4× the no-op scenario, and the first scenario that exercises cross-Promise
scheduling under load.

All child RRs carry the label `cascade.kratix.io/source=<sourceName>` so the
driver can find a SimpleApp's transitive EvoPublisher without walking
relationships.

## Promises

A single multi-doc YAML file: `test/perf/assets/cascade-promises.yaml`
(separator: `---`). All three Promises are pre-installed by the rig at suite
start; the load is `PERF_N × SimpleApp` resource requests.

### SimpleApp Promise

- CRD: `SimpleApp` v1alpha1 in group `simpleapp.kratix.io`.
- `spec.repo` (string, unused — placeholder for realism only).
- Two sequential configure pipelines:
  - **`build-via-github`** — bash loop for `BUILD_DURATION_SECONDS` seconds
    (default 0), then writes `/kratix/metadata/status.yaml` with a fake
    `imageTag` and `buildUrl`.
  - **`schedule-aaas`** — reads `imageTag` from the status file, writes a
    fully-populated `AppAsAService` resource manifest into
    `/kratix/output/aaas.yaml` with `metadata.labels.cascade.kratix.io/source`
    set to the source RR's name.

### AppAsAService Promise

- CRD: `AppAsAService` v1alpha1 in group `aaas.kratix.io`.
- `spec.imageTag` (string).
- One configure pipeline:
  - **`schedule-evopub`** — writes an `EvoPublisher` manifest to
    `/kratix/output/evopub.yaml`, propagating the same
    `cascade.kratix.io/source` label.

### EvoPublisher Promise

- CRD: `EvoPublisher` v1alpha1 in group `evopub.kratix.io`.
- `spec.imageTag` (string).
- One configure pipeline:
  - **`publish`** — bash sleeps for `PUBLISH_DURATION_SECONDS` (default 0),
    echoes "published", exits 0. Mock for the external API call.

All pipeline containers use `busybox:1.36`. Commands are
`["sh","-c", "<heredoc>"]`. Avoid `command: ["true"]` style — the existing
rig hit a YAML coercion bug there and `["sh","-c","exit 0"]` is the
established workaround.

Tunable knobs (`BUILD_DURATION_SECONDS`, `PUBLISH_DURATION_SECONDS`) live as
`spec.containers[].env` on the relevant pipeline containers, default `"0"`
in the asset YAML:

```yaml
env:
  - name: BUILD_DURATION_SECONDS
    value: "0"
```

Setting either > 0 requires editing the asset file and reapplying — there is
no CLI flag plumbing. They exist for one-off "what if a real build cost 30s"
investigations, not for normal use.

## Driver additions

Two new functions in `test/perf/driver`, both colocated in
`driver/promise.go` and `driver/convergence.go` respectively. The existing
single-Promise / single-GVK convergence path is unchanged.

### `driver.ApplyPromises(ctx, c, path) ([]*platformv1alpha1.Promise, error)`

Reads the multi-doc YAML at `path`, splits on `\n---\n` boundaries, decodes
each document into a `Promise`, creates-or-updates each, then waits for all
of them to reach `Available=True` (uses the existing
`WaitForPromiseAvailable` helper per Promise, with a shared timeout).

### `driver.WaitForCascadeConverged(...)`

Polls every 2s. Considers the cascade converged when, for every supplied
`sourceName`, there exists a leaf RR (of `leafGVK`, in `leafNamespace`)
labelled `<labelKey>=<sourceName>` whose status has `Reconciled=True`.

Signature:

```go
func WaitForCascadeConverged(
    ctx context.Context, c client.Client,
    leafGVK schema.GroupVersionKind, leafNamespace string,
    labelKey string, sourceNames []string,
    timeout time.Duration, progress func(Progress),
) error
```

The intermediate `AppAsAService` RR is not awaited explicitly — by transitive
property, if its EvoPublisher child exists and is `Reconciled=True`,
`AppAsAService` had to reconcile to schedule it.

`Progress` is the same shape as the existing convergence helper
(`Reconciled`, `Total`, `Elapsed`).

## Reducer extension

`reduce.RunMeta` currently carries `RRNamePattern string`. Cascade summaries
need per-RR-type percentiles, so:

```go
type RunMeta struct {
    Name, Namespace string
    N               int
    WallClock       time.Duration

    // Existing single-pattern field. Used by perf_test.go.
    RRNamePattern string

    // New plural field. When non-empty, the reducer emits one
    // "Reconciles per RR" section per labelled pattern. Used by cascade_test.go.
    RRNamePatterns []NamedPattern
}

type NamedPattern struct{ Label, Pattern string }
```

Reducer rules:

- If `RRNamePatterns` is non-empty, ignore `RRNamePattern` and emit a
  `### <Label>` subsection per pattern.
- If `RRNamePatterns` is empty, preserve current behaviour exactly (one
  flat "Reconciles per RR" section based on `RRNamePattern`).

This keeps `perf_test.go` untouched.

Summary fragment for a cascade run:

```
## Reconciles per RR (from controller log)
### simpleapp
- p50: 4    p95: 5    p99: 5    max: 5    sample size: 100 RRs matched
### appasaservice
- p50: 6    p95: 7    p99: 7    max: 7    sample size: 100 RRs matched
### evopublisher
- p50: 4    p95: 4    p99: 4    max: 4    sample size: 100 RRs matched
```

The per-controller peaks table (`workqueue_depth`, `longest_running_processor`,
`reconcile_total Δ`) is unchanged — it already includes all dynamic-RR
controllers by name.

## Test file

`test/perf/cascade_test.go`, `//go:build perf`, separate `It` block in the
same `Describe`:

1. Apply the 3 Promises (`driver.ApplyPromises`).
2. Defer cleanup: delete leaf RRs by label, then `simpleapp`, `appasaservice`,
   `evopublisher` Promises in *reverse* dependency order.
3. Start the controller log tail + metrics scraper (reuses
   `startLogTail` and `scraper.NewSecure` unchanged).
4. Create `PERF_N` SimpleApp RRs (`driver.CreateAll` with the SimpleApp GVK).
5. Call `driver.WaitForCascadeConverged` with the EvoPublisher GVK + the
   `cascade.kratix.io/source` label.
6. Stop the scraper, run `reduce.Reduce` with the 3-pattern `RRNamePatterns`
   list.

The `It` block targets results dir `test/perf/results/<PERF_RUN>/` exactly
like the existing test. Convergence and reducer logic differ; everything
else is identical.

## Makefile

Add a sibling target to `perf-test`:

```make
.PHONY: perf-test-cascade
perf-test-cascade: fmt vet ## Run the cascade perf scenario
	PERF_N=$${PERF_N:-100} PERF_RUN=$${PERF_RUN:-run-$$(date +%s)} \
	go test -tags=perf -timeout=60m ./test/perf/... \
		-args -perf.n=$${PERF_N} -perf.run=$${PERF_RUN} \
		-perf.context=$${PERF_CONTEXT:-kind-platform} \
		-perf.namespace=$${PERF_NAMESPACE:-default} \
		-perf.timeout=$${PERF_TIMEOUT:-30m} \
		-perf.basename=$${PERF_BASENAME:-simpleapp} \
		-ginkgo.focus=cascade
```

The existing `perf-test` target needs to keep running only the no-op spec
once cascade is added. Disambiguation uses Ginkgo labels:

- Existing no-op `It` block gets `Label("noop")`. `perf-test` adds
  `--ginkgo.label-filter=noop`.
- Cascade `It` block gets `Label("cascade")`. `perf-test-cascade` adds
  `--ginkgo.label-filter=cascade`.

Label-based selection avoids string-match brittleness on spec descriptions.

## What we choose NOT to do

- **No real publisher mock pod.** A single in-cluster mock-publisher Service
  serving 100+ concurrent pipeline requests would itself become a
  coincidental bottleneck. The bash `sleep` mock is deliberate.
- **No blocking pipelines.** Pipeline-2 of SimpleApp does not wait for its
  AppAsAService child to converge. Cascade convergence is observed at the
  *driver* level, not enforced by the workflow. This matches kratix's normal
  idiom.
- **No new perf rig flags.** All tuning (`BUILD_DURATION_SECONDS`,
  `PUBLISH_DURATION_SECONDS`) lives as env vars on pipeline containers — set
  to `0` by default in the YAML, tunable later by editing the asset file if
  someone wants to model slow CI / slow publish.
- **No status-coalescing for the cascade.** The intermediate AppAsAService
  RR's `Reconciled=True` is implicit (only checked transitively via the leaf).
  Adding explicit two-stage convergence ("AAaS reconciled, then EvoPub
  reconciled") buys nothing for perf measurement; the leaf check subsumes it.

## Open questions

None — every option has been pinned. Tracker for future scope:

- If a third scenario is needed, refactor towards a scenario-parametric
  driver before adding more test files.
- If pipeline-2 of SimpleApp ever needs to *wait* for its child, that's a
  fundamentally different perf scenario (blocking dependencies) and gets
  its own design.

## Test plan

1. Run on a fresh kind cluster (the rig's standard reset path).
2. `PERF_N=100 PERF_RUN=cascade-100 make perf-test-cascade` with the
   `MCR=20 + QPS=200/400 + predicate` configuration (the established best
   config).
3. Verify `summary.md` shows three "Reconciles per RR" subsections, one
   per Promise, all with sample size 100.
4. Verify controller log shows reconciles for `simpleapp-NNNNN`,
   `appasaservice-NNNNN`, and `evopublisher-NNNNN` in roughly that order.
5. Verify `kubectl get evopublishers -l cascade.kratix.io/source` returns
   100 entries, all with `Reconciled=True`.
6. Re-run with `MCR=1` flags-off to confirm the cascade also converges (it
   just takes longer) — sanity check that the test isn't flag-coupled.

## Files touched / added

- **NEW** `test/perf/assets/cascade-promises.yaml`
- **NEW** `test/perf/cascade_test.go`
- **MOD** `test/perf/driver/promise.go` — add `ApplyPromises`
- **MOD** `test/perf/driver/convergence.go` — add `WaitForCascadeConverged`
- **MOD** `test/perf/reduce/reduce.go` — `RRNamePatterns` path
- **MOD** `test/perf/perf_test.go` — add `Label("noop")` to existing `It`
- **MOD** `Makefile` — `perf-test-cascade` target
- **MOD** `test/perf/README.md` — short paragraph on the cascade scenario
