# 870 — Per-Promise reconciliation interval (slice 1)

## Delivery state

| | |
|---|---|
| Stage | All 5 tasks green and reviewed; adversarial pass running; 3 whole-branch reviews and the done-gate remain |
| Issue | [#870](https://github.com/syntasso/kratix/issues/870) |
| Briefing | [issue comment](https://github.com/syntasso/kratix/issues/870#issuecomment-5315935703) |
| Design | [issue comment](https://github.com/syntasso/kratix/issues/870#issuecomment-5315889490) |
| Base | `main` @ `7b12ae65`; 0 ahead, 0 behind `origin/main` |
| Workspace | jj workspace `kratix-870` (not a git worktree — colocated repo, jj must keep working) |
| Baseline gate | Green. `make lint` 0 issues, `make test` 15 suites, codegen no-diff |
| Tasks green | 5 / 5 |
| Head | `5fc444d1`; gate re-run green on this exact SHA (lint 0 issues, 15 suites) |
| Open findings | 1 accepted gap (see Amended, item 4); 2 Minor rolled up |
| Parked questions | none |
| Release level | Unreleased — branch and PR, no tag |

## Global constraints

- **Slice 1 of 3, stacked.** Slices 2 and 3 branch off this one. Every seam this slice creates must accept
  the annotation link in slice 2 **without changing its signature or any call site**. That is the single
  most important constraint here; a resolution helper keyed on `PromiseSpec` rather than on the revision
  will force churn through the whole stack.
- Pair: Derik Evangelista + Jake Klein. Both `Co-authored-by` on every commit.
- Gate in CI's form: `make lint` (uses `.golangci-required.yml`, as CI does) and `make test`. CI also builds
  the image; run `make docker-build` once before hand-off.
- No ADR governs this. ADR0008 (Draft) owns the adjacent controls — stay consistent with it: `paused` wins
  outright, `manual-reconciliation` bypasses the interval.

## What this slice does not do

Named so a reviewer does not read them as gaps:

- No annotation layer and no propagation watches — slices 2 and 3.
- No promise `status` field for the effective interval — deferred by decision.
- No influence on drift detection (`StateStoreReconciliationInterval`) — out of scope by decision.
- No per-trigger intervals (`workflows.resource.config`) — YAGNI, additive later.

## Test strategy, and why

The controller suite is Ginkgo over a **fake client** with the workflow reconcile stubbed
(`internal/controller/suite_test.go`). That is the right level here and the reasoning should be checked, not
assumed: this slice's entire behaviour is a `ctrl.Result.RequeueAfter` value and a time comparison. It
creates no cluster resources, so the fake-client blind spots that matter elsewhere — RBAC, image
resolution, vendored container names — cannot hide anything in this diff.

The webhook check is added inside `vpromise.kb.io`, which is **already wired** for `create;update`. A unit
test on the validator therefore proves the rule; it does not prove the wiring, and it does not need to.

**No system or core test for this slice.** The acceptance criteria are about elapsed time, and the shortest
legal interval is 1 minute — a system test would either sleep past a minute or fake the clock, and in both
cases assert less than the unit tests already do. Recorded as a decision so the done-gate can challenge it
rather than discover it.

## Tasks

### Task 1 — Add the field and regenerate (mechanical, no behavioural test)

Add a reconciliation-interval field to `WorkflowConfig` in `api/v1alpha1/promise_types.go`, typed to match
`KratixConfig.ReconciliationInterval` in `cmd/main.go` so the two read the same syntax. Regenerate.

This task has **no behavioural RED and must not pretend to have one** — a struct field has no behaviour, and
a test that fails only because a symbol is missing proves nothing. It exists as a separate task so Task 2's
RED can be a genuine assertion failure rather than a compile error.

Verification: `make manifests generate` produces a CRD diff containing the new property and nothing else,
and the tree still builds. Baseline codegen was no-diff, so any other change in that diff is a signal.

The field's doc comment is the only discoverability Vanessa gets in this slice (see the briefing). It states
what the field controls and what happens when it is unset. It does not restate the type.

### Task 2 — Resolution helper

One function resolving the effective interval from **the revision governing this reconcile**, plus a
fallback. Not from `PromiseSpec`, not from the live Promise — see the Global Constraints; slice 2 adds the
annotation check inside this function and must touch nothing else.

RED must be an **assertion** failure: implement it returning the fallback unconditionally, watch a test that
expects the spec value fail on the value, then make it pass.

Assertions to prove:
- Spec value present → that value, not the fallback.
- Spec value absent → the fallback.
- A revision whose snapshot differs from the live Promise resolves to the **snapshot**. This is the one that
  encodes pinning; without it the helper could read the wrong object and every other test would still pass.

### Task 3 — Promise controller honours the resolved interval

Two call sites, and they are separate behaviours — cover both:
- The requeue (`nextReconciliation`, `promise_controller.go:679`).
- The force-run gate (`passedReconciliationInterval`, called at `:975`).

The struct field `ReconciliationInterval` stays, demoted to carrying the global default. Do not delete it.

Assertions to prove:
- A Promise declaring an interval requeues after **that** duration.
- A Promise declaring none requeues after the global default.
- The force-run gate uses the resolved value: a workflow that completed longer ago than a **short** declared
  interval forces a re-run; the same elapsed time under a **long** declared interval does not.
- Setting an interval on one Promise does not change another Promise's requeue. This is the issue's second
  acceptance scenario and it is the assertion most likely to be skipped.

Log the resolved value and where it came from at the requeue site. One log line, not a new abstraction.

### Task 4 — Resource request controller honours the resolved interval

Same shape as Task 3, against `dynamic_resource_request_controller.go` — the requeue (`:1465`) and the
force-run gate (`shouldForcePipelineRun`, called at `:223`).

**Read the interval from the revision object, not from `promise`.** `:416` overwrites `promise.Spec` from
the revision but leaves `promise`'s metadata as the live Promise's. A helper handed `promise` will look
correct, pass every test written against a Promise whose spec and revision agree, and be wrong in slice 2.
Task 2's third assertion is what catches this; make sure it is exercised through this controller too.

Assertions to prove:
- A resource request requeues after the interval its **revision** declares.
- Two resource requests of the same Promise bound to revisions declaring different intervals requeue
  differently.
- The force-run gate uses the resolved value.

### Task 5 — Admission validation

`PromiseCustomValidator` rejects a declared interval that is zero, negative, or below one minute, on both
create and update.

An unparseable value is **not** this task's problem: the field's type makes the API server reject it during
decoding, before any webhook runs. Assert that boundary rather than writing a webhook check that can never
fire — and say so in the test name.

Zero is rejected rather than treated as "never" because `shouldForcePipelineRun`
(`dynamic_resource_request_controller.go:1773`) compares `time.Since(...) <= interval`, which at zero is
always false and therefore forces a run every reconcile — the opposite of what a reader expects. "Never" is
`kratix.io/paused`.

This deliverable is a **guard**, so it needs a bite-proof in the initial dispatch: break the rule, paste the
failure, restore. Running the suite is not the proof — it passes either way, which is the whole problem.
A bite-proof that will not reproduce is a finding to report, not a step to force.

Assertions to prove: exactly-1m accepted; just-under-1m rejected; zero rejected; negative rejected; unset
accepted; rejection message names the field and the limit.

## Amended during implementation

Where this section and a task step above disagree, **this section wins**.

1. **The resolution helper is a method, not a free function.** `PromiseRevision.ReconciliationInterval(fallback)`
   in `api/v1alpha1/promiserevision_types.go`. Slice 2 adds the annotation check inside its body; no
   signature or call site changes.

2. **Task 3 grew a third fallback tier and a sentinel error.** `resolveReconciliationInterval` in
   `promise_controller.go` resolves latest revision → the live Promise's spec → the global default, and
   distinguishes "no revision marked latest yet" from a genuine List failure so an operator sees Debug
   rather than Error for the expected case. That distinction needed a sentinel, so `latestRevision` now
   wraps `errNoLatestPromiseRevisionYet` and callers use `errors.Is`. The first attempt matched on the
   error message text; that was replaced. Task 5's plan text does not mention any of this.

3. **Both controllers log a `source` field** alongside the resolved interval — `"revision"`,
   `"promiseSpec"`, `"globalDefault"` on the Promise side, and `"revision"` / `"globalDefault"` on the
   resource-request side, where no middle tier can occur. The plan asked only for "the resolved value and
   its source" at the Promise requeue site.

4. **Accepted gap — the "read from the revision, not the Promise" requirement is not covered by any test,
   and will not be until slice 2.** `dynamic_resource_request_controller.go` assigns
   `promise.Spec = promiseRevisionUsed.Spec.PromiseSpec` before every call site this slice touches, so the
   two sources hold the same value by construction and no test can distinguish them. A test that tried
   would have to fabricate a state the code cannot reach.

   This is *not* guarded by the type system, contrary to an earlier assumption recorded during delivery:
   the method's receiver prevents passing a `*Promise` to it, but
   `promise.Spec.Workflows.Config.ReconciliationInterval` is a different expression that compiles equally
   well. The current code is correct; the protection against regressing it is code review, not CI.

   **Slice 2 must close this.** There, the override is an annotation on the revision's *metadata*, which
   the `.Spec` assignment never copies — so a Promise-sourced read becomes observably wrong and the
   discriminating test becomes both possible and meaningful. Do not let slice 2 land without it.

5. **Task 4's spec count is 4, not 3**, and one of them ("does not force a re-run") passes against the
   pre-change code by construction. Disclosed rather than dressed up. Task 3 likewise has three specs that
   pass pre-change, all for the same structural reason: the old code returned the global default
   unconditionally, so anything asserting the default holds trivially.

6. **No system or core test, as planned** — but the reasoning is worth restating because it is the kind of
   decision a later reader will want to challenge: the whole behaviour is a `ctrl.Result.RequeueAfter`
   value and a time comparison, the shortest legal interval is one minute, and a system test would either
   sleep past a minute or fake the clock while asserting less than the unit tests already do.

## Known limitations, accepted deliberately

An adversarial review of the finished slice raised these. They were put to the story owner, who chose to
ship without filing issues. Recorded here because slice 2 adds a user-editable annotation to
`PromiseRevision` and whoever picks it up needs to know what is not bounded.

- **The operator cannot cap the interval a Promise author chooses.** The floor is
  `v1alpha1.MinReconciliationInterval` — a compile-time constant — while the platform *default* is
  operator-configurable via `KratixConfig`. A Promise arrives verbatim from an OCI or git artifact
  (`promiserelease_controller.go` assigns `existingPromise.Spec = promise.Spec`), so a third-party author
  can raise the workflow rate for every resource of their Promise up to 600x the platform default, and the
  operator's only remedies are uninstalling the Promise or editing a spec that `PromiseRelease` will
  revert. Nothing bounds concurrent workflow Jobs and nothing adds jitter.

- **Failure retries are flat at the interval, with no backoff.** A failing workflow with
  `reconcileAfterFailure` enabled retries every interval indefinitely. At the one-minute floor that is 60
  retries an hour against an API that is likely already throttling — the opposite of what this feature was
  asked for. Coupling retries to the interval was a deliberate decision (an interval that exempted retries
  would leave a hole exactly where failures cluster); the consequence at the floor is the cost of it.

- **The interval defeats `kratix.io/workflow-suspended`.** In both controllers the force-run gate runs
  before the suspended check, and resuming deletes the suspend label. Pre-existing behaviour, but the
  window was previously the operator's own default; a Promise author can now compress it to about a
  minute. Pipeline-*initiated* suspension is unaffected, since the completed condition is not `True`
  while a run is in progress.

- **Under `pinned` binding mode an interval is frozen in an archived revision.** Archived revisions are
  never re-synced, and `PromiseRelease` skips installation when the Promise already exists at the
  requested version — so republishing the same version with a corrected interval does nothing. The
  already-provisioned resources, which are the ones generating the API calls, keep their old cadence until
  each binding is upgraded individually. Default mode is `floating`, where the value does propagate.

## Follow-ups this slice deliberately leaves open

- The discriminating test in item 4 — **slice 2, mandatory**.
- `resolveReconciliationInterval` resolves twice per Promise reconcile (force-run gate, then requeue).
  Both are cached in-memory list scans, so the cost is low, but resolving once and threading the result
  would remove the redundancy and the small risk of the two calls seeing different cache states.
- `promise_webhook_test.go` exact-matches the rejection message only for the zero case; a negative-value
  exact-match would additionally pin how negative durations render.
- Docs: `spec.workflows.config.reconciliationInterval` needs a kratix-docs entry. The CRD field
  description is the only discoverability a Promise author gets from this slice.
