# Reconcile Promise workflows after failure (#862)

## Context

Kratix is intended to continuously reconcile: workflows should re-run on the
Default Reconciliation Interval the way a Kubernetes controller keeps driving
towards desired state. Today the periodic reconciliation only re-runs a
Promise's configure workflows when they last completed successfully. A Promise
whose workflow last failed is skipped and sits failed until it is manually
addressed (a spec change or the `kratix.io/manual-reconciliation: "true"`
label).

Issue #858 fixed this for **resource-request** workflows and introduced the
Kratix config field `workflows.reconcileAfterFailure` (default `true`). This
story (#862) applies the same behaviour to **Promise** workflows, reusing that
same field so behaviour is consistent across Promises and Resources.

## Key finding: most of the plumbing already exists

The config field is fully wired to the `PromiseReconciler`:

- `cmd/main.go:125` — `Workflows.ReconcileAfterFailure *bool`
- `cmd/main.go:342` — `PromiseReconciler{ ReconcileAfterFailure: getReconcileAfterFailure(kratixConfig) }`
- `internal/controller/promise_controller.go:82` — `PromiseReconciler.ReconcileAfterFailure bool`

But `r.ReconcileAfterFailure` is currently only *passed down to the dynamic
resource controllers* (`promise_controller.go:1148`, `1179`). It is never used
in the Promise's own workflow reconcile path. Closing #862 is therefore purely
a change in `promise_controller.go` (plus docs and tests). **No new config
field, no `cmd/main.go` logic change.**

## Control-flow analysis

Tracing `PromiseReconciler.Reconcile`, a **failed** Promise configure workflow
settles at the `passiveRequeue` early-return:

```go
// promise_controller.go:260
passiveRequeue, reconcileResult, err := r.reconcileDependenciesAndPromiseWorkflows(opts, promise, usPromise)
...
// promise_controller.go:265
if passiveRequeue {
	logging.Debug(logger, "reconciliation paused awaiting Promise configure workflow updates")
	return ctrl.Result{}, nil        // <-- no requeue scheduled
}
```

`reconcileConfigure` (`workflow.ReconcileConfigure`) returns `passiveRequeue =
true` for both **failed** and **in-progress** workflows. So a failed Promise
workflow returns here with `ctrl.Result{}` (no requeue) and **never reaches**
the success-only "schedule next reconciliation" guard later in `Reconcile`:

```go
// promise_controller.go:323
completedCond := promise.GetCondition(string(resourceutil.ConfigureWorkflowCompletedCondition))
if !promise.HasPipeline(v1alpha1.WorkflowTypePromise, v1alpha1.WorkflowActionConfigure) ||
	(completedCond != nil && completedCond.Status == metav1.ConditionTrue) {
	...
	return r.nextReconciliation(logger), nil   // success path only
}
return ctrl.Result{}, nil
```

This is the same trap documented for the resource controller in #858: the
retry requeue must live in the `passiveRequeue` branch, not the post-success
guard, or it is dead code for failed workflows.

Re-running is a two-link chain, exactly as in the resource fix:

1. **Wake-up** — schedule a requeue at the interval after a failure (otherwise
   nothing wakes the controller).
2. **Re-run** — when a reconcile happens after the interval, force the pipeline
   to actually re-run (set the manual-reconciliation label); otherwise the
   reconcile is a no-op because nothing changed.

## Design

### Part 1 — Force the re-run once the interval has elapsed

`passedReconciliationInterval` (`promise_controller.go:2224`) currently forces a
run only after a *successful* completion. Extend it to also fire after a failed
run when retry is enabled:

```go
func passedReconciliationInterval(completedCond *metav1.Condition, reconciliationInterval time.Duration, reconcileAfterFailure bool) bool {
	if completedCond == nil || time.Since(completedCond.LastTransitionTime.Time) <= reconciliationInterval {
		return false
	}
	return completedCond.Status == metav1.ConditionTrue ||
		(reconcileAfterFailure && promiseWorkflowCompletedWithFailure(completedCond))
}

func promiseWorkflowCompletedWithFailure(completedCond *metav1.Condition) bool {
	return completedCond != nil &&
		completedCond.Status == metav1.ConditionFalse &&
		completedCond.Reason == resourceutil.ConfigureWorkflowCompletedFailedReason
}
```

A new `metav1`-typed helper is needed because the existing
`workflowCompletedWithFailure` (in `dynamic_resource_request_controller.go`)
takes a `*clusterv1.Condition`, whereas the Promise path uses
`promise.GetCondition`, which returns `*metav1.Condition`.

Update the call site (`promise_controller.go:968`) to pass
`r.ReconcileAfterFailure`. This feeds `restartOnReconciliationInterval`
(`:2245`), which sets `ManualReconciliationLabel = "true"` and triggers the
re-run from start.

### Part 2 — Schedule the wake-up requeue after a failure

In `Reconcile`'s `passiveRequeue` branch (`promise_controller.go:265`), schedule
the periodic requeue on a genuine failure. Here the existing `clusterv1`
`workflowCompletedWithFailure` **can** be reused, because
`resourceutil.GetCondition(obj *unstructured.Unstructured, …)` returns a
`*clusterv1.Condition`, and `usPromise` is the unstructured object passed to
`reconcileConfigure`:

```go
if passiveRequeue {
	logging.Debug(logger, "reconciliation paused awaiting Promise configure workflow updates")
	// A failed Promise configure workflow settles here (ReconcileConfigure returns a
	// passive requeue) and never reaches the success guard below, so schedule the
	// periodic reconcile here to retry the run on the interval.
	if r.ReconcileAfterFailure && workflowCompletedWithFailure(
		resourceutil.GetCondition(usPromise, resourceutil.ConfigureWorkflowCompletedCondition)) {
		return r.nextReconciliation(logger), nil
	}
	return ctrl.Result{}, nil
}
```

Both parts are gated on `ReconcileAfterFailure` **and** a genuine failure
(`ConfigureWorkflowCompletedFailedReason`), so an in-progress run — which also
returns `passiveRequeue = true` but with the `PipelinesInProgressReason` reason
— is never restarted.

### Risk to verify during implementation

The Part 2 read relies on `usPromise` carrying the freshly-set failed condition
**in memory** after `reconcileConfigure` runs (the resource fix relies on the
analogous `rr` doing so). This must be confirmed by the system test (real
controller), not assumed from the mocked unit tests — the #858 memory note
records that the default unit-test mock of `reconcileConfigure` returns
`passiveRequeue` without exercising the real failed-workflow path, which is how
the original #858 bug slipped through.

### Part 3 — Documentation

Generalise the now-misleading "resource request's configure workflows" wording
to cover Promise workflows too:

- `cmd/main.go` — the `ReconcileAfterFailure` struct-field comment (`:122`).
- `config/samples/kratix-config.yaml` — the `reconcileAfterFailure` comment (`:11`).

## Testing

### Unit — `internal/controller/promise_controller_test.go`

Set `ReconcileAfterFailure: true` in the suite's `PromiseReconciler` setup.
Add a "previous configure workflow failed" context, mirroring the resource
controller's, with a `ConfigureWorkflowCompletedCondition` of
`Status=False, Reason=ConfigureWorkflowCompletedFailedReason` and a
`LastTransitionTime` older than the reconciliation interval:

- **`ReconcileAfterFailure` true (default):** re-runs the promise configure
  workflow (sets `ManualReconciliationLabel=true`) and schedules the next
  reconciliation on the interval (`RequeueAfter: ReconciliationInterval`).
- **`ReconcileAfterFailure` false:** does not set the manual-reconciliation
  label and does not schedule a periodic reconcile.

### System — `test/system/reconcile_after_failure_test.go`

Add a **separate** promise fixture (Option 1, chosen over augmenting the
existing `reconcilable` promise): a gated `promise.configure` pipeline in the
existing fixture would keep that promise `Unavailable`, breaking the resource
specs' `BeforeEach` `Should(ContainSubstring("Available"))` wait.

New fixtures under `test/system/assets/reconcile-after-failure/`:

- A promise (e.g. `promise-workflow.yaml`) with a `promise.configure` pipeline
  that fails until a gate ConfigMap (distinct name, e.g.
  `reconcile-after-failure-promise-gate`) exists, mirroring the resource
  fixture's gate container.

New specs mirroring the two existing `When` blocks, asserting against the
Promise's own `ConfigureWorkflowCompleted` condition and the promise-configure
job names:

- **`reconcileAfterFailure` true:** the failed promise workflow re-runs on the
  schedule and resumes to success once the gate ConfigMap is created; keeps
  reconciling after success (`lastTransitionTime` advances).
- **`reconcileAfterFailure` false:** the failed promise workflow is not re-run
  on the schedule, but manual reconciliation (`kratix.io/manual-reconciliation`
  label on the Promise) still triggers it.

Reuse the existing `kratix-config-retry.yaml` / `kratix-config-no-retry.yaml`
configs — the field is shared, so no new config fixtures are needed.

## Out of scope

- Suspended / paused workflow handling (explicitly out of scope per #862).
- The resource-request path (delivered in #858).
- Any new or Promise-specific config field — the shared
  `workflows.reconcileAfterFailure` is reused.

## Acceptance criteria (from #862)

```gherkin
Given a Promise whose previous workflow run failed
And the Kratix config uses the default settings
When the Default Reconciliation Interval elapses
Then the Promise's workflows are re-run

Given a Promise whose previous workflow run succeeded
And the Kratix config uses the default settings
When the Default Reconciliation Interval elapses
Then the Promise's workflows are re-run

Given a Promise whose previous workflow run failed
And the operator has disabled retry-after-failure in the Kratix config
When the Default Reconciliation Interval elapses
Then the Promise's workflows are not re-run
And the Promise can still be reconciled manually via a spec change or the kratix.io/manual-reconciliation label
```
