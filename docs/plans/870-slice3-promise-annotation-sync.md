# 870 slice 3 — Penny sets it once

## Delivery state

| | |
|---|---|
| Stage | Plan written; build not started |
| Tier | **standard** — ~40 production lines, raised on risk (see below). Budget 150 min |
| Issue | [#870](https://github.com/syntasso/kratix/issues/870) |
| Base | `39669e54` (slice 2 tip, PR #878, itself stacked on #877). 14 ahead / **0 behind** `main` |
| Workspace | jj workspace `kratix-870-s3` |
| Baseline gate | captured to scratchpad |
| Tasks green | 0 / 3 |
| Open findings | none |
| Parked | The briefing below could not be posted to #870 — GitHub returning 503. Post at land time. |
| Release level | Unreleased — branch and PR, no tag |

### Why standard, when size says light

Size reads light: roughly forty production lines inside `handlePromiseVersion`'s `CreateOrUpdate` mutate function. Three risks outrank it.

- **It makes the promise controller a new writer of revision annotations.** Until now the controller owned a revision's spec, labels and owner ref; annotations were the operator's. That changes here.
- **The delete arm destroys state a user set by hand.** Set-always means the latest revision's annotation mirrors the Promise's exactly, so a value Penny typed onto the latest revision is wiped on the next reconcile. That is the workflow slice 2 shipped and documented.
- **It can wedge a Promise.** Slice 2's PromiseRevision webhook rejects an out-of-policy annotation. If a Promise carries a bad value, the controller's own sync write fails admission, and with it every subsequent revision update for that Promise. Slice 2's adversarial pass recorded this as latent; this slice makes it reachable, which is why task 2 exists.

## Briefing

**Decision.** No ADR governs this; resolved live during slice 1 and unchanged since. [ADR0008 — Workflow Controls](https://github.com/syntasso/adrs/blob/main/0008_workflow_controls.md) is adjacent and **Draft**. Slice 2 fixed the gate ordering, so an elapsed interval no longer strips a suspension; keep it that way.

**Persona.** [Penny](https://app.notion.com/p/36e85a0225cc818e8989e275cd32c666), Platform Engineer, Champion, Active. This closes the friction slice 2 left her: she currently annotates *each* revision she wants to affect. After this she annotates the Promise once, and the latest revision mirrors it — surviving version bumps, because `PromiseRelease` merges annotations rather than replacing them.

*Friction it leaves her:* hand-annotating the **latest** revision stops sticking. Revisions for older versions are never re-synced, so those stay hand-pinnable. The asymmetry is deliberate and was chosen when the sync semantics were settled, but it partly walks back what slice 2's PR told her to do — task 3 owns saying so.

**Vanessa** is unaffected: her spec value still governs wherever no annotation exists.

## Global constraints

- Stacked on slice 2. The resolution chain does not change: `revision annotation → revision spec snapshot → global default`. This slice only changes **how the revision annotation gets there**.
- One key: `kratix.io/reconciliation-interval`. No other annotation is synced, copied or deleted.
- Only the revision for the Promise's current version is touched. Archived revisions keep whatever they carry.
- Gate in CI's form: `make lint` (`.golangci-required.yml`) and `make test`. Agents run the covering package only.

## Tasks

### Task 1 — The latest revision mirrors the Promise's annotation

Constraint: when the Promise carries `kratix.io/reconciliation-interval`, the revision for its current version carries the same value; when the Promise does not, the revision does not either. Mirroring means the delete arm as well as the set arm — without it there is no way to undo an override through the interface that created it.

Touch that key only. An unrelated annotation on the revision, whoever set it, survives.

Goes **red** on three assertions, and the second is the one implementations skip: a Promise with the annotation produces a revision carrying it; **removing it from the Promise removes it from the revision**; an unrelated annotation on the revision is untouched by either arm.

Also assert what must *not* happen: a revision for an older version is not modified. That is a property of which object the mutate function targets, and it is the guarantee that keeps slice 2's per-revision pinning alive.

### Task 2 — Admission rejects a bad annotation on the Promise

Constraint: the Promise validating webhook rejects `kratix.io/reconciliation-interval` values that do not parse, or fall below `MinReconciliationInterval`, on create and update. Absent stays valid.

This is not symmetry for its own sake. Without it, a Promise annotated `30s` makes the controller's own sync write fail slice 2's PromiseRevision admission — and since that write happens inside `handlePromiseVersion`, the Promise stops accepting revision updates entirely. The floor has to be enforced where the value enters, not only where it lands.

Reuse `MinReconciliationInterval` and match the rejection-message shape already used for the spec field and for the revision annotation. One floor, one constant, now three write paths.

Goes **red** on: a Promise annotated below the floor is rejected on create; one annotated with an unparseable value is rejected with a distinguishable message; exactly the floor is accepted.

### Task 3 — Claims this slice falsifies

Known now, so they are a task rather than a sweep:

- `api/v1alpha1/promiserevision_types.go` — `ReconciliationIntervalAnnotation`'s doc describes a revision-level override. It is now also mirrored from the Promise, and the mirror wins on the latest revision.
- `api/v1alpha1/promiserevision_types.go` — `MinReconciliationInterval`'s doc names three enforcement points. Task 2 adds a fourth.
- `docs/plans/870-slice2-annotation-override.md` — tells Penny to annotate revisions. True for archived ones, no longer true for the latest.
- Anything in `promise_controller.go` describing what `handlePromiseVersion`'s `CreateOrUpdate` owns — it now owns an annotation as well as spec, labels and the owner ref.

## Amended during implementation

Nothing yet. Deltas go here before hand-off; where this section and a task disagree, this section wins.
