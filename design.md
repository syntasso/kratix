# Dry Run for Compound Promises — Design

**Status:** spike complete — feasible, working end to end
**Branch:** `feat/dry-run-prototype-crd` — **changes are uncommitted**
**Demo:** `dry-run-compound-demo-app/` (README has the full manual walkthrough)
**Audience:** @kirederik, @ChunyiLyu, @catmo-syntasso

## TL;DR

**Problem.** A compound Promise's pipeline emits resource requests for other Promises.
In dry-run mode those land in the dry-run Destination as inert files, so the preview
shows the component *request* but never what the component would actually change — which
for a compound Promise is most of the blast radius.

**Blocker.** Kratix has no notion of a compound Promise. A pipeline just writes YAML;
nothing distinguishes a component request from any other document in the output.

**Decision.** The compound workflow **labels** the component requests it emits:

```yaml
kratix.io/parent-promise-name: app
kratix.io/parent-resource-name: my-app          # this one is the detection signal
kratix.io/parent-resource-namespace: default
```

**Why this shape.** The pipeline emits byte-identical output in both modes and never
reads `KRATIX_DRY_RUN` — only Kratix's *interpretation* changes. That is what makes the
preview trustworthy: a dry run exercises the same code path a real run does. The labels
are also not dry-run-specific — they buy compound traceability in normal runs too, which
is a gap today regardless.

**Guideline worth documenting:** use `KRATIX_DRY_RUN` to suppress *side effects*, never
to change *output*.

**Mechanism.** `DryRunReconciler` scans the compound's dry-run Works for labelled
documents, raises one owned component `DryRun` each, waits, and folds their diffs into
one summary. Recursion is free — a compound component emits marked grandchildren and the
same rule applies.

## Alternatives rejected

| Option | Why not |
|---|---|
| Infer from GVK (does it match an installed Promise?) | Ambiguous by construction — a compound may legitimately ship a CR whose GVK is also served by a Promise. Needs no author change, which was its appeal. |
| Mark the platform Destination, treat "routed there" as the signal | Semantically truest, but Kratix has no first-class platform Destination — `environment: platform` is convention — so it needs a new concept plus config. |
| Pipeline emits `DryRun` CRs when `KRATIX_DRY_RUN=true` | **Destroys the diff.** The compound's own section becomes `- widget-request.yaml / + widget-dryrun.yaml` instead of `replicas: 1 → 3`, and you forfeit the same-code-path property that justifies the feature. Also still needs Kratix to act on the output, so detection doesn't go away. |

Side benefit of the label: detection is fully decoupled from routing. The demo routes
component requests to `environment: dev` rather than a platform Destination, and
detection is unaffected.

## What's implemented, and where

- `api/v1alpha1/work_types.go` — the three `kratix.io/parent-*` labels, plus
  `DryRunParentLabel` linking a component DryRun to its compound one.
- `api/v1alpha1/dryrun_types.go` — `status.components[]` (`DryRunComponentStatus`) and
  the `Pending`/`Succeeded`/`Failed` phase constants.
- `internal/controller/dryrun_controller.go` — `componentRequests` (find labelled docs
  in the compound's Works), `promisesByGVK` (resolve doc → Promise),
  `ensureComponentDryRuns` (create owned children), `componentDryRuns` +
  `pendingComponentDryRuns` (wait), `componentSummaryBody` (nest a child's diff),
  `syncComponentStatus` / `componentStatuses` / `componentsSucceededCondition`.
- `work-creator/lib/reader.go` — on `NotFound`, present the intended name from
  `resourceRequestRef` instead of falling back to the ephemeral request. Without this a
  brand-new compound request emits components named `kratix-dry-run-…`, which is the
  main pull-request case.

Single-resource dry runs are byte-identical to before — the `## Compound request:`
wrapper and deeper pipeline headings appear only when components exist. Full unit suite
passes (15 suites). No new tests were written.

## Status contract

`Completed` = the run finished. Stays `True` even when a component failed, because a
partial summary is still worth reading.

`ComponentsSucceeded` = the preview is *complete*. Set only for compound runs; `False`
with reason `ComponentDryRunFailed` or `ComponentDryRunIncomplete`.

**Gate on `ComponentsSucceeded`.** Gating on `Completed` approves previews that silently
omit a component. The summary markdown says so, but markdown is for humans and the gate
reads status. Flipping `Completed` to `False` instead would conflate "produced nothing"
with "produced a useful partial preview".

```yaml
components:
  - promise: broken     request: flaky-demo-broken  phase: Failed
    message: "A Configure Pipeline has failed: broken-configure"
  - promise: postgres   request: flaky-demo-db      phase: Succeeded
conditions:
  - type: Completed            status: "True"   reason: SummaryWritten
  - type: ComponentsSucceeded  status: "False"  reason: ComponentDryRunFailed
    message: "Preview is incomplete; these components failed: broken/flaky-demo-broken"
```

## Two bugs found — don't re-learn these

**The reconciler watched the wrong condition.** It only failed on
`WorksSucceeded=False`. A failed pipeline writes no Works, so `WorksSucceeded` is
*absent*, not False — the dry run waited forever with a completely empty `.status`.
Kratix reports the failure fine on `ConfigureWorkflowCompleted`
(`reason: ConfigureWorkflowFailed`). Reading it fixes three things at once: the component
reports its failure, the compound proceeds in seconds instead of waiting out the timeout,
and the component's own reconcile stops requeueing forever behind a crash-looping pod.
Caveat: failures still take ~6 min to surface, because Job backoff must exhaust first.

**Cache race on component creation.** `CreateOrUpdate` then immediately listing the
children returns nothing — the cache-backed client hasn't caught up — so the compound
wrote a componentless summary and marked itself `Completed`, which is terminal, so it
never recovered. Fixed by comparing components ensured against components listed and
requeueing on a mismatch. Any production version needs the same guard.

## Decisions already made

- Components are emitted **declaratively**, via Works. Imperative creation is out of
  scope.
- **Partial results** over all-or-nothing; per-component status makes the gap visible.
  PR gating happens outside Kratix.
- Output format unchanged — same markdown, extended with a section per Promise.
- Requiring the label is acceptable; Amaryllis adjust their testing Promise.
- Dry-run Destination uses **`nestedByMetadata`**. Flat (`filepath.mode: none`) is easier
  to browse but every preview writes into one directory, so previews silently overwrite
  each other's files including the summary. Cost of nesting: paths contain the
  deterministic-but-unguessable ephemeral request name, so read the summary Work from the
  API rather than browsing for it.

## Open / next, roughly in priority order

1. **Does Amaryllis's compound emit components declaratively?** If a pipeline creates
   them with `kubectl`, there is nothing to intercept and a dry run would create *real*
   requests. This is the one unknown that could invalidate the approach for them.
2. **No tests.** The cache race is exactly the kind of thing only an integration test
   catches.
3. **Two API additions want an ADR** — the `kratix.io/parent-*` labels and
   `status.components[]`.
4. **Previewed output is never cleaned up.** Destination is `cleanup: none`, so files
   from deleted DryRuns accumulate without bound. Deleting a DryRun should remove its
   output. Related: a production version should give each DryRun its own output prefix —
   cheap for the summary Work we build ourselves, more invasive for pipeline output,
   which needs the scheduler to prefix dry-run Works with the owner.
5. **Genuinely hanging pipelines** are still only bounded by `componentDryRunTimeout`
   (10 min), and that protects the compound, not the component. Untested — a container
   that exits always eventually reports failure, so `ComponentDryRunIncomplete` and the
   "did not complete" summary branch have never executed.
6. **Untested paths:** recursion beyond one level (and how a per-level timeout behaves in
   a deep tree); two components of the same Promise; multi-document output files;
   components in a different namespace from the compound.
7. **Delete workflows.** The ephemeral request is garbage-collected through the normal
   deletion path, so a Promise with a delete workflow should run it — with
   `KRATIX_DRY_RUN=true` injected — when a DryRun is deleted. Read from the code, never
   observed. The only item with a path to real side effects.
8. **Minor:** duplicate reconcile on completion (the `Owns()` watch and the pending
   requeue both fire, so `writeSummary` runs twice — idempotent, but wasted work).
   Non-deterministic pipelines produce spurious diffs. Components whose Promise isn't
   installed are skipped with a log line.

## Playback for @kirederik, @ChunyiLyu, @catmo-syntasso

1. **Yes, it's possible** — one label convention plus ~400 lines in the reconciler.
2. **The label is the whole design.** Nothing else distinguishes a component request from
   any other YAML in a pipeline's output. See the alternatives table for why not GVK
   inference or Destination routing.
3. **The pipeline is identical in both modes**; only Kratix's interpretation changes.
   That's what makes the preview trustworthy, and why we didn't have the pipeline emit
   `DryRun` CRs directly.
4. **Amaryllis need a one-line pipeline change** to label the component requests they
   already emit.
5. **Failure is partial-results, and the status says so** — gate on
   `ComponentsSucceeded`, not `Completed`.
6. **We fixed a real bug on the way through:** failures used to hang forever with an
   empty status, because the reconciler watched a condition a failed pipeline never sets.
7. **Before this ships:** confirm Amaryllis emit components declaratively, add tests, get
   an ADR for the two API additions, and clean up previewed output on deletion.
