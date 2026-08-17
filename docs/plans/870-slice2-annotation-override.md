# 870 slice 2 — Penny's annotation override

## Delivery state

| | |
|---|---|
| Stage | Plan written; build not started |
| Tier | **light** — ~75 production lines, no contract move |
| Issue | [#870](https://github.com/syntasso/kratix/issues/870) |
| Base | `f7055364` (slice 1 tip, PR #877). 0 commits between it and my head; it is 7 ahead / **0 behind** `main` |
| Workspace | jj workspace `kratix-870-s2` |
| Baseline gate | captured to scratchpad; slice 1 left it green |
| Tasks green | 0 / 5 |
| Open findings | none |
| Parked | The briefing cannot be posted to #870 — GitHub API returning 503. Post when it recovers. |
| Release level | Unreleased — branch and PR, no tag |

### Why light, and the one thing the predicate misses

The PromiseRevision webhook goes from no-op to enforcing, which looks like a contract move. It is not: the key is **new**, so nothing that currently succeeds starts failing, and no CRD schema changes. Slice 1 added a real CRD field; this adds none.

What the tier predicate does *not* capture is blast radius. The watch in task 3 is ~40 lines but fans out across the estate, and the promise controller writes the latest revision on **every** reconcile — a predicate-less watch is an estate-wide storm on a timer. Light tier still runs the adversarial pass, so it is covered; recorded so step 8 can decide whether the predicate needs a blast-radius column.

## Briefing

**Decision.** No ADR governs this; resolved live against `syntasso/adrs` during slice 1. [ADR0008 — Workflow Controls](https://github.com/syntasso/adrs/blob/main/0008_workflow_controls.md) is adjacent and **Draft**, so not binding, but this slice must stay consistent with it: `paused` wins outright, `manual-reconciliation` bypasses the interval.

**Persona.** This is [Penny's](https://app.notion.com/p/36e85a0225cc818e8989e275cd32c666) slice — Platform Engineer, Champion, Status: Active. Slice 1 served Vanessa; Penny got nothing from it and had to fork a Promise to retune it.

*Goal it serves:* her page says "standardise how teams consume infrastructure while **allowing legitimate exceptions**", and lists reconciliation among what she focuses on. This slice is the exception mechanism.

*Friction it leaves her:* she must annotate **each revision** she wants to affect — annotating the Promise does nothing until slice 3 syncs it to the latest revision. On a Promise with several live versions that is several annotations, and no single place shows her the effective interval (the status field was deferred). Deliberate, and the cost of the slice boundary.

**Vanessa** is unaffected: her spec value still governs wherever Penny has not overridden it.

## Global constraints

- Stacked on slice 1. Slice 3 branches off this one.
- The chain becomes `revision annotation → revision spec snapshot → global default`, resolved **inside** `PromiseRevision.ReconciliationInterval` with no signature or call-site change. That constraint is why slice 1 keyed the helper on the revision.
- The annotation key is `kratix.io/reconciliation-interval`.
- `MinReconciliationInterval` applies to the annotation exactly as it does to the spec value. One floor, one constant.
- Gate in CI's form: `make lint` (`.golangci-required.yml`) and `make test`. Agents run the covering package only.

## Tasks

### Task 1 — The annotation becomes the top link

Constraint: `PromiseRevision.ReconciliationInterval(fallback)` consults the annotation before the spec snapshot. Signature unchanged; no call site changes. A value that fails to parse, or falls below the floor, falls through to the next link rather than propagating — the read path cannot reject, only decline. Make declining visible the way the existing below-minimum case is.

Goes **red** on: a revision whose annotation declares one interval and whose spec snapshot declares a different one resolves to the annotation's value. Assert on the value.

Also prove: annotation absent → spec snapshot; annotation unparseable → spec snapshot; annotation below the floor → spec snapshot, not the global default (the next link, not the last one — an implementation that skips straight to the fallback passes a weaker test).

### Task 2 — Admission validates the annotation

Constraint: `PromiseRevisionCustomValidator`'s create and update paths reject an annotation that does not parse as a Go duration, or that is below `MinReconciliationInterval`. Absent is valid. Reuse the floor constant and match the rejection-message shape the Promise webhook already uses for this field.

Goes **red** on: a revision annotated below the floor is rejected on create. Assert the rejection, and separately assert exactly-the-floor is accepted.

Both entry points must enforce it — the two validators are separate functions today, unlike the Promise webhook where both route through one.

### Task 3 — A revision annotation change reaches the Promise

Constraint: changing the annotation takes effect on the **Promise's own workflows** without waiting out the old interval. Watch `PromiseRevision`, map a revision to **its own** Promise, and enqueue it. The existing watch maps a revision to the Promises that *require* its Promise — a different relationship; do not extend it.

**Resource requests are deliberately not fanned out.** They pick the new interval up at their next natural reconcile — up to the *old* interval later — and an operator who wants it now sets `kratix.io/reconcile-resources` on the Promise, which is the existing mechanism for exactly this. This narrows an earlier decision that said the change should reach resources immediately: doing so needs a durable observed-value mirror so a controller can tell the annotation changed, and that was judged too much for this slice. Say so in the PR; a lag nobody documented is a bug report.

The enqueue must fire only when the annotation's value actually changes. The promise controller creates-or-updates the latest revision on every reconcile, so a predicate that fires on any revision write enqueues every Promise on a timer.

Goes **red** on: the predicate returns false when a revision is written with its annotation unchanged, and true when the value differs. Assert both directions — a predicate stuck returning true passes a one-sided test.

Expose the mapper and predicate through `export_test.go` rather than reaching for the manager; that file already carries this pattern.

### Task 4 — The discriminating test slice 1 owed

Slice 1 could not prove the controllers read the interval from the revision rather than from `promise`, because `promise.Spec` is assigned from the revision snapshot before every call site, so the two agree by construction. The annotation changes that: it lives on the revision's **metadata**, which that assignment never copies.

Goes **red** on: a resource request whose revision carries the annotation, and whose live Promise carries neither the annotation nor a matching spec value, requeues at the annotation's interval. An implementation reading from `promise` fails this.

Prove it discriminates by making the controller read from `promise` and watching this fail. This is the test slice 1's plan marked mandatory for slice 2; when it is green, strike that entry from slice 1's plan.

### Task 5 — Claims this slice falsifies

Not a sweep at the end — these are known now, so they are a task. Each needs its current text checked and corrected where the change makes it wrong:

- `api/v1alpha1/promiserevision_types.go` — `ReconciliationInterval`'s doc describes only the snapshot and the floor. The annotation becomes the first link.
- `api/v1alpha1/promiserevision_types.go` — `MinReconciliationInterval`'s doc names the Promise webhook and the read path as the two enforcement points. A third arrives in task 2.
- `internal/controller/promise_controller.go` — `resolveReconciliationInterval`'s doc had an annotation-override clause **removed** during slice 1 because it described something that did not exist. It exists after task 1; restore an accurate version.
- `internal/controller/dynamic_resource_request_controller.go` — the requeue log's `source` values do not include an annotation case.
- `docs/plans/870-reconciliation-interval.md` — the "Known limitations" entry recording the untestable revision-vs-promise seam is closed by task 4.

## Amended during implementation

Nothing yet. Deltas go here before hand-off; where this section and a task disagree, this section wins.
