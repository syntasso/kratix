
# DynamicResourceRequestController state-machine map

Source: `internal/controller/dynamic_resource_request_controller.go`. Line numbers are 1-indexed against that file unless otherwise stated. Helper line refs into `lib/workflow/reconciler.go` and `lib/resourceutil/util.go` are tagged with `workflow:` and `resourceutil:` prefixes.

## 0. Controller wiring (re-entry channels)

Configured in `internal/controller/promise_controller.go:1180-1224`:

- `For(unstructuredCRD)` — self-watch fires on **every event on the RR object**, including status subresource writes. No `ResourceVersionChangedPredicate` is applied. This means **every `r.Client.Update` and every `r.Client.Status().Update` on the RR re-enqueues this RR**. This is the load-bearing assumption the existing state machine relies on.
- `Watches(&batchv1.Job{}, …)` — fires on Job events whose labels match Kratix-managed jobs for this Promise.
- `Watches(&v1alpha1.Work{}, …)` — fires on Work events whose `ResourceNameLabel` + `PromiseNameLabel` match.
- `Watches(&v1alpha1.ResourceBinding{}, …)` — fires on ResourceBinding events whose labels match.

Requeue constants (`promise_controller.go:120-122`):

- `fastRequeue    = ctrl.Result{RequeueAfter: 5 * time.Second}`
- `defaultRequeue = ctrl.Result{RequeueAfter: 15 * time.Second}`
- `slowRequeue    = ctrl.Result{RequeueAfter: 60 * time.Second}`

`r.nextReconciliation(...)` (line 971) returns `ctrl.Result{RequeueAfter: r.ReconciliationInterval}`.

---

## 1. Reconcile branch-by-branch

### B0. `WatchStopped` short-circuit
- **Lines**: 92–102
- **Condition**: `r.WatchStopped == true` (Promise being torn down).
- **Inputs**: `r.WatchStopped` only.
- **Mutations**: none.
- **Return**: `ctrl.Result{}, nil` (terminal).
- **Re-entry**: none — the informer for the RR GVK has been removed.
- **Classification**: terminal short-circuit. Not a transition.

### B1. Initial GETs + trace setup
- **Lines**: 104–145
- **Condition**: always runs after B0.
- **Inputs**: `r.Client.Get(promise)` (line 116), `r.Client.Get(rr)` (line 121). Reads `rr.Generation`.
- **Mutations**: `persistReconcileTrace` (line 142) may write trace annotations (separate annotation write via `r.Client` — see `trace_helpers.go`). This is a **meta mutation** that fires once on the first reconcile of a generation.
- **Return on error**:
  - promise GET fails → `ctrl.Result{}, err`
  - rr GET NotFound → `ctrl.Result{}, nil` (terminal)
  - rr GET other error → `defaultRequeue, nil`
  - persistReconcileTrace err → `ctrl.Result{}, err`
- **Re-entry**: error path re-enqueues via controller-runtime backoff; trace annotation write self-re-fires.
- **Classification**: meta-mutation (trace annotations). This write must stay separate because downstream code reads annotations; cannot be deferred to a terminal write.

### B2. Promise-level pause
- **Lines**: 147–152
- **Condition**: `promise.Labels[pauseReconciliationLabel] == "true"`.
- **Inputs**: `promise.Labels`.
- **Mutations**: `setPausedReconciliationStatusConditions` (helper at 648–656). Internally:
  - In-memory: `resourceutil.MarkReconciledPaused(rr)` (sets Reconciled condition to Unknown/Paused).
  - Event: `v1.EventTypeWarning, pausedReconciliationReason`.
  - Write: `r.Client.Status().Update(ctx, rr)` (line 653). **Guarded** — only fires if Reconciled is not already in the paused state. Idempotent terminal-ish.
- **Return**: `ctrl.Result{}, err-from-status-update`.
- **Re-entry**: self-watch on the status update (when it actually writes). Otherwise terminal until label flips.
- **Classification**: **status mutation** — candidate for coalescing into a single terminal write, but the branch returns immediately so this *is* effectively the terminal write for this branch. Keep as-is; just ensure the in-memory mark + write are colocated.

### B3. Resource-level pause
- **Lines**: 154–159
- **Condition**: `rr.Labels[pauseReconciliationLabel] == "true"`.
- Identical mutation/return/re-entry profile to **B2**. Same classification.

### B4. Promise label backfill
- **Lines**: 161–167
- **Condition**: `resourceLabels[PromiseNameLabel] != r.PromiseIdentifier` (RR missing the promise label).
- **Inputs**: `rr.GetLabels()`.
- **Mutations**: `setPromiseLabels` (lines 1187–1195) → `r.Client.Update(ctx, rr)`. **Meta mutation** (labels). Load-bearing: jobs/works/bindings are matched by this label and the rest of Reconcile uses it.
- **Return**: `ctrl.Result{}, nil` (after the Update) or `ctrl.Result{}, err`.
- **Re-entry**: self-watch on RR spec/meta update.
- **Classification**: **spec/meta mutation** — must stay as a separate write before any subsequent state can advance (cannot be coalesced into a terminal status write).

### B5. PromiseRevision resolution (feature-flagged)
- **Lines**: 175–190
- **Condition**: `r.PromiseUpgradeFeatFlag == true`.
- **Inputs**: `getPromiseRevisionToUse` (line 1076–1099). Internally lists `ResourceBindingList` and `PromiseRevisionList`. Reads `rr.status.promiseVersion`. No writes.
- **Mutations**: emits an Event only (no API writes).
- **Return on error**: `ctrl.Result{}, err`.
- **Re-entry**: n/a (no write).
- **Classification**: pure read.

### B6. Delete path
- **Lines**: 192–195
- **Condition**: `rr.DeletionTimestamp != 0`.
- **Inputs/Mutations**: delegates to `deleteResources` (lines 767–839). See **§2 Delete sub-branches** below.
- **Return**: passthrough.
- **Re-entry**: see §2.

### B7. Promise cannot fulfil resources
- **Lines**: 197–199
- **Condition**: `r.promiseCannotFulfilResourceRequests(promise)` — true iff `!*CanCreateResources` OR Promise's `PromiseAvailable` condition is False.
- **Inputs**: `promise` status, `r.CanCreateResources`.
- **Mutations**: `ensurePromiseIsUnavailable` (1014–1026):
  - If `!IsPromiseMarkedAsUnavailable(rr)`: in-memory `MarkPromiseConditionAsNotAvailable` (writes `status.message = "Pending"` and the condition), then `r.Client.Status().Update`.
  - Else: returns `slowRequeue, nil` with no write.
- **Return**:
  - First time: `ctrl.Result{}, statusUpdateErr` — re-enters via self-watch on status update.
  - Subsequent: `slowRequeue, nil` — re-enters via 60s timer.
- **Classification**: **status mutation** — already terminal for the branch (no further work), idempotent guard. Fine as-is.

### B8. Promise became available again
- **Lines**: 201–206
- **Condition**: `IsPromiseMarkedAsUnavailable(rr) == true` (and B7 condition no longer holds, so we got here).
- **Mutations**: `ensurePromiseIsAvailable` (1028–1034) — **unconditionally** marks PromiseAvailable=True and calls `Status().Update`. Note: not guarded; runs every reconcile that lands here until the condition no longer marks unavailable. (Because `MarkPromiseConditionAsAvailable` flips it to True, next reconcile won't satisfy B8.)
- **Return**: `ctrl.Result{}, nil` on success, `ctrl.Result{}, err` on error.
- **Re-entry**: self-watch on status update.
- **Classification**: **status mutation** — could be coalesced with later status writes if we restructured, but currently it returns immediately. Treat as transition write.

### B9. Add missing finalizers
- **Lines**: 208–214
- **Condition**: `resourceutil.FinalizersAreMissing(rr, r.getRRFinalizers())`. Finalizers are `workFinalizer, removeAllWorkflowJobsFinalizer, runDeleteWorkflowsFinalizer` (+ `resourceBindingFinalizer` if feature-flag).
- **Mutations**: `addFinalizers` (shared.go:140–149) → `r.Client.Update(ctx, rr)`. **Spec/meta mutation.**
- **Return**: `fastRequeue, nil` on conflict; otherwise `ctrl.Result{}, err`.
- **Re-entry**: self-watch on RR spec update; or 5s timer on conflict.
- **Classification**: **spec/meta mutation** — cannot be deferred; finalizers must be persisted before delete handling, work creation, etc., can proceed safely.

### B10. ResourceBinding upsert (feature-flagged)
- **Lines**: 216–222
- **Condition**: `r.PromiseUpgradeFeatFlag == true`.
- **Inputs**: `rr`, `promise`.
- **Mutations**: `updateResourceBinding` (375–433) calls `controllerutil.CreateOrUpdate` on a `ResourceBinding` object. This writes an **external object**, not the RR itself.
- **Return**: returns through (does **not** return from Reconcile on success); only errors return `ctrl.Result{}, err`.
- **Re-entry**: changes on the ResourceBinding fire the binding Watches re-enqueue, but Reconcile continues in this pass regardless.
- **Classification**: **external object mutation** — independent of RR self-watch. Already non-terminating; no change needed.

### B11. Force re-run on reconciliation interval
- **Lines**: 224–232 (block also computes `completedCond` and `forcePipelineRun`).
- **Condition**: `shouldForcePipelineRun(completedCond, r.ReconciliationInterval)` AND `rr.Labels[WorkflowRunFromStartLabel] != "true"`.
  - `shouldForcePipelineRun`: completed condition exists, Status=True, and `time.Since(LastTransitionTime) > ReconciliationInterval`.
- **Mutations**: `restartOnReconciliationInterval` (976–996) — if `forcePipelineRun && notManualReconcile(rr)`, calls `updateManualReconcileToTrue` (998–1004) → sets `rr.Labels[ManualReconciliationLabel] = "true"` then `r.Client.Update(ctx, rr)`. **Spec/meta mutation.**
- **Return**: `ctrl.Result{}, nil` after the label update (the `restarted` bool short-circuits Reconcile). `ctrl.Result{}, err` on failure.
- **Re-entry**: self-watch on RR spec update.
- **Classification**: **spec/meta mutation** — load-bearing, must persist before next pass picks up `manual-reconciliation=true`. Cannot be deferred.

### B12. Unpause (HasReconcilePausedCondition)
- **Lines**: 234–241
- **Condition**: `resourceutil.HasReconcilePausedCondition(rr)` — Reconciled condition currently Paused. Reached only when B2/B3 no longer fire (label removed), so we need to actively clear the paused state.
- **Mutations**:
  1. `ensureWorkflowRunsFromStart(ctx, r.Client, rr)` (shared.go:128–137) → sets `Labels[WorkflowRunFromStartLabel]="true"`, deletes `WorkflowSuspendedLabel`, `r.Client.Update`. **Spec/meta.**
  2. In-memory `resourceutil.MarkReconciledPending(rr, "Unpaused")`.
  3. `r.Client.Status().Update(ctx, rr)` (line 240). **Status write.**
- **Return**: `ctrl.Result{}, statusUpdateErr` (or earlier err from step 1).
- **Re-entry**: self-watch on either of the two writes; the spec update will fire reconcile, the status update will fire reconcile.
- **Classification**: **mixed**:
  - Step 1 (label flip) — **spec/meta**, must precede further reconciliation that reads the label. Cannot be deferred.
  - Step 3 (Reconciled=Pending) — **status mutation**. *Could* be deferred to a terminal coalesced write in a redesigned single-pass reconciler — but here the function returns immediately so it's already terminal-for-this-branch. The double-write is the main inefficiency.

### B13. Generate pipeline resources (pure compute)
- **Lines**: 243–246
- **Condition**: always (after B12 not firing).
- **Inputs**: `promise.GenerateResourcePipelines(WorkflowActionConfigure, rr, logger)` — pure.
- **Mutations**: none. Errors → `ctrl.Result{}, err`.

### B14. `ensureConfigureWorkflowStatus`
- **Lines**: 248–250 (helper at 435–460).
- **Condition**: whenever `status.workflows` counter differs from `len(pipelineResources)` OR pipeline phase slice needs initialising (`ensureRRKratixWorkflowStatusIsSetup` at 747–765).
- **Inputs**: `rr.status.workflows`, `rr.status.kratix.workflows.pipelines`.
- **Mutations**: in-memory `SetStatus("workflows", count)` and/or `ResetPipelineStatusToPending`. If anything changed → `r.Client.Status().Update(ctx, rr)` (line 456).
- **Return**: on `updated || err` → `ctrl.Result{}, err`. Falls through if nothing changed.
- **Re-entry**: self-watch on status update.
- **Classification**: **status mutation** — explicitly the write called out in `docs/perf-rig-findings.md`. **Prime candidate for coalescing** in the new design. Note: this write is a *pre-execute* status sync; the next branch (workflow execution) reads `status.kratix.workflows.pipelines` to determine what's pending. So if the pipelines slice is missing/stale, the workflow code may misbehave. The coalesce design needs to either: (a) compute pending pipelines from the live in-memory rr without requiring it be persisted, or (b) keep this as a separate write but only when truly necessary.

### B15. `reconcileSuspendedWorkflow`
- **Lines**: 252–258 (helper at 462–527).
- **Condition**: `isWorkflowSuspended(rr)` — RR has `WorkflowSuspendedLabel=true`.
- **Sub-branches inside the helper**:

  - **B15a. Manual reconcile OR generation drift while suspended** (476–500):
    - In-memory: set `Labels[WorkflowRunFromStartLabel]="true"`, delete `WorkflowSuspendedLabel`.
    - Write 1: `r.Client.Update(ctx, rr)` (488). **Spec/meta.**
    - GET fresh copy (493).
    - In-memory: `ResetPipelineStatusToPending(updatedRR, pipelineResources)`.
    - Write 2: `r.Client.Status().Update(ctx, updatedRR)` (499). **Status.**
    - Returns `shouldRequeue=true, result=nil, err`. Top-level returns `ctrl.Result{}, err`.
    - Re-entry: self-watch on either write.
    - Classification: **mixed** — label flip cannot be deferred (workflow code reads it); status reset is dependent on the spec write being visible (hence the GET). This is the most refactor-resistant sub-branch.

  - **B15b. NextRetryAt has passed** (511–519):
    - In-memory: delete `WorkflowSuspendedLabel`.
    - Write: `r.Client.Update(ctx, rr)` (518). **Spec/meta.**
    - Returns `shouldRequeue=true, result=nil, err`. Top-level returns `ctrl.Result{}, err`.
    - Re-entry: self-watch on spec update.
    - Classification: **spec/meta**.

  - **B15c. NextRetryAt is in the future** (520–524 + 526):
    - Returns `shouldRequeue=true, result=&{RequeueAfter: until(nextRetryAt)}`, with err from `setWorkflowSuspendedStatusCondition`.
    - `setWorkflowSuspendedStatusCondition` (658–668): if Reconciled cond is not already Suspended, in-memory mark and `r.Client.Status().Update`.
    - Top-level returns `*requeueResult, err` (line 255).
    - Re-entry: RequeueAfter timer.
    - Classification: **status mutation** (guarded), then genuine timer wait.

  - **B15d. Plain suspended, no retry, no spec drift** (526):
    - Same as B15c minus the requeue timer: returns `shouldRequeue=true, result=nil`, err from `setWorkflowSuspendedStatusCondition`.
    - Top-level returns `ctrl.Result{}, err`.
    - Re-entry: self-watch on the (guarded) status update; once Reconciled is Suspended, no further writes until label flips → effectively a **genuine wait** for the label flip.
    - Classification: **status mutation** then wait.

### B16. `reconcileConfigure` (workflow engine)
- **Lines**: 260–283.
- **Condition**: reached when workflow is not suspended and pipelines exist.
- **Inputs**: builds `workflow.Opts`; `pipelineResources`.
- **Mutations** (inside `workflow.ReconcileConfigure`, see `workflow:154-183`):
  - `workflow:reconcileWorkflowStatus` (258–312):
    - In-memory `SetStatus("workflowsSucceeded"|"workflowsFailed", n)`, `MarkCurrentPipelineAsSucceeded/Failed`, `ResetPipelineStatusToPending`, `MarkCurrentPipelineAs(phase)`.
    - Write: `opts.client.Status().Update(opts.ctx, opts.parentObject)` (307). **Status mutation on the RR.** Returns `passiveRequeue=true` after this write.
  - `workflow:setFailedConditionAndEvents` (375–393):
    - In-memory `MarkConfigureWorkflowAsFailed`, `MarkReconciledFailing`, then calls `reconcileWorkflowStatus` again which writes status.
  - `workflow:createConfigurePipeline` (598–626):
    - May call `removeManualReconciliationLabel` (628–631) or `removeWorkflowRestartLabel` (633–636), both via `removeLabel` (638–647) → `r.Client.Update`. **Spec/meta mutation on the RR.**
    - `deleteResources(...)` and `applyResources(...)` → **external object mutations** (Jobs and ancillary objects).
    - Calls `setPipelineStartingStatus` (649–696) which conditionally writes RR status with Reconciled=Pending(WorkflowPending), ConfigureWorkflowCompleted=False(Running), pipeline phase=Running, and `status.message="Pending"`. Write at line 690. **Status mutation.**
  - `workflow:handleCurrentPipelineJob` (331–373):
    - May call `suspendJob` → **external Job patch**.
    - On success (no failure, no manual, etc.), returns `false, cleanup(opts, ...)`.
  - `workflow:cleanup` (533–595):
    - Calls `cleanupJobs` (deletes old Jobs) and may also delete unused Works — **external object mutations**. May write `Status().Update` on Works (not on the RR).

- **Return from top-level Reconcile**:
  - Error from `reconcileConfigure` → `ctrl.Result{}, err`.
  - `passiveRequeue == true` → `ctrl.Result{}, nil` (waits for external Job/RR-status event).
  - Otherwise falls through to B17.
- **Re-entry**:
  - Status writes inside `reconcileWorkflowStatus` and `setPipelineStartingStatus` fire RR self-watch.
  - Label writes (`removeManualReconciliationLabel`, `removeWorkflowRestartLabel`) fire RR self-watch.
  - Created Jobs fire the Job watch on completion.
- **Classification per write inside this branch**:
  - `reconcileWorkflowStatus` Status().Update → **status mutation**. Returns `passiveRequeue=true` to wait for external event; *could* be coalesced into a terminal RR write in single-pass design, **but the function then short-circuits and skips pipeline-create logic on the same pass** (see condition at workflow:174). The state machine assumes the next pass picks up the new counter and proceeds. To coalesce safely the new design must allow status changes to propagate without short-circuiting.
  - `removeLabel` Update → **spec/meta mutation**, cannot be deferred to terminal write because the workflow logic relies on the label being absent on subsequent passes.
  - `setPipelineStartingStatus` Status().Update → **status mutation**. Already partially coalesced (sets `updated` bool, writes once). Candidate for full coalesce with B14 / B17.
  - Job/Work creates and deletes → external; independent.

### B17. ObservedGeneration sync
- **Lines**: 285–287
- **Condition**: `rr.GetGeneration() != resourceutil.GetObservedGeneration(rr)`.
- **Mutations**: `updateObservedGeneration` (1174–1179) — in-memory `SetStatus("observedGeneration", generation)` + `r.Client.Status().Update`.
- **Return**: `ctrl.Result{}, statusUpdateErr`.
- **Re-entry**: self-watch on status update.
- **Classification**: **status mutation** — semantically important: downstream consumers use observedGeneration. **Could be coalesced** into a terminal RR write, *provided* the coalesced write reliably fires when generation changed (don't lose the update).

### B18. No pipeline configured (cleanup-only branch)
- **Lines**: 289–291
- **Condition**: `!promise.HasPipeline(WorkflowTypeResource, WorkflowActionConfigure)`.
- **Mutations**: `cleanupWorkflowCountersAndExecution` (734–745). If counters are non-zero or pipeline slice present → in-memory `SetStatus(...)` zeroes, remove `status.kratix.workflows.pipelines`, then `r.Client.Status().Update`. Guarded — only writes if needed.
- **Return**: `r.nextReconciliation(logger), cleanupErr` = `{RequeueAfter: ReconciliationInterval}, err`.
- **Re-entry**: timer-based.
- **Classification**: **status mutation** (guarded). Candidate for coalesce.

### B19. `ensureResourceStatus` — the big status aggregator
- **Lines**: 293–299 (helper at 328–352).
- **Inputs**: lists `WorkList` filtered by labels (via `getWorksStatus`, 670–708).
- **Calls `generateResourceStatus`** (529–541) which orchestrates four in-memory mutators and returns a single "did anything change" bool:
  - `updateWorksSucceededCondition` (543–577) — in-memory `MarkResourceRequestAsWorks{Failed|Misplaced|Pending|Succeeded}` based on Work list. Also fires Events. Returns `bool`.
  - `updateReconciledCondition` (579–617) — in-memory `MarkReconciled{Pending|Failing|True}` based on the just-set WorksSucceeded + ConfigureWorkflowCompleted conditions. Returns `bool`.
  - `generateWorkflowsCounterStatus` (710–732) — in-memory `SetStatus("workflows", "workflowsSucceeded", "workflowsFailed", ...)`. Returns `bool`.
  - `updatePromiseVersionStatus` (619–646) — in-memory `SetStatus(promiseVersion, resourceBindingVersion)`. Returns `bool`.
- **Mutations**: single `r.Client.Status().Update(ctx, rr)` at line 349 if any of the four returned true. **Already coalesced.**
- **Return**: `ctrl.Result{}, nil` if `statusUpdated`; `ctrl.Result{}, err` on error; falls through if nothing changed.
- **Re-entry**: self-watch on the status update; if nothing changed, falls through into B20.
- **Classification**: **status mutation, already partially coalesced**. This is the model for the rest of the refactor. Note: it triggers a self-reenqueue on every change just to flow into B20+ on the next pass — that round-trip is what the single-pass design is trying to eliminate. The four sub-mutators are all pure in-memory; the only API write is the single Status().Update.

### B20. ResourceBinding version status (feature-flagged)
- **Lines**: 301–306
- **Condition**: `r.PromiseUpgradeFeatFlag == true` and not short-circuited by B19.
- **Mutations**: `updateResourceBindingVersionStatus` (354–373) — GETs the ResourceBinding, then if `LastAppliedVersion != desiredVersion`, sets it in-memory and `r.Client.Status().Update` on the **binding** (not the RR). Guarded.
- **Return**: `ctrl.Result{}, err` only on error; falls through.
- **Re-entry**: changes on the ResourceBinding trigger the ResourceBinding watch on the RR.
- **Classification**: **external object mutation**. Independent of RR self-watch.

### B21. Workflow completed successfully → write lastSuccessfulTime
- **Lines**: 308–317
- **Condition**: `workflowsCompletedSuccessfully(workflowCompletedCondition)` AND `shouldUpdateLastSuccessfulConfigureWorkflowTime(...)`.
  - `shouldUpdate...` (1213–1222): the last-transition-time differs from either `status.lastSuccessfulConfigureWorkflowTime` (legacy) or `status.kratix.workflows.lastSuccessfulConfigureWorkflowTime`.
- **Mutations**: `updateLastSuccessfulConfigureWorkflowTime` (1224–1231) — in-memory `SetStatus("lastSuccessfulConfigureWorkflowTime", ts)` + `SetKratixWorkflowsStatus(...)` then `r.Client.Status().Update`.
- **Return**:
  - Inner `shouldUpdate==true` → `ctrl.Result{}, statusUpdateErr` (after writing).
  - Workflow completed but no update needed → `r.nextReconciliation(logger), nil`.
- **Re-entry**: self-watch on status update; or timer-based via `nextReconciliation`.
- **Classification**: **status mutation** — guarded. Candidate for coalesce with B19 / B22.

### B22. Promise version status update (feature-flagged, terminal write)
- **Lines**: 319–323
- **Condition**: `r.PromiseUpgradeFeatFlag == true` AND `updatePromiseVersionStatus(...)` returns true (the in-memory mutator at 619–646 reports the status changed).
- **Mutations**: in-memory `SetStatus(promiseVersion, resourceBindingVersion)`. Write: `r.Client.Status().Update(ctx, rr)` at line 321.
- **Return**: `ctrl.Result{}, statusUpdateErr`.
- **Re-entry**: self-watch on status update.
- **Classification**: **status mutation**. Note: this is **a duplicate of one of the sub-mutators in B19's `generateResourceStatus`** (`updatePromiseVersionStatus`). The logic exists in two places because B19 short-circuits and returns `ctrl.Result{}, nil` on the write, so B22 only fires when B19 didn't write. This is precisely the kind of double-execution the refactor wants to eliminate — both calls to `updatePromiseVersionStatus` mutate the same fields based on the same inputs.

### B23. Terminal fall-through
- **Lines**: 325
- **Condition**: nothing else fired.
- **Return**: `ctrl.Result{}, nil`. Genuine terminal — waits for external watches (Job/Work/Binding) or a manual label flip.

---

## 2. Delete sub-branches (called from B6 via `deleteResources`)

### D1. All RR finalizers cleared → final cleanup
- **Lines**: 768–775
- **Mutations**: if `PromiseUpgradeFeatFlag`, `ensureResourceBindingRemoved` (841–860) — `r.Client.Delete` on the ResourceBinding (idempotent on NotFound). External mutation.
- **Return**: `ctrl.Result{}, nil` (terminal — kube-apiserver garbage-collects the RR).
- **Re-entry**: none.

### D2. Run-delete-workflows finalizer present
- **Lines**: 782–810
- **Mutations**:
  - `reconcileDelete(jobOpts)` → calls `workflow.ReconcileDelete` (`workflow:69-120`). May call `applyResources` (Jobs — external) or `suspendJob` (external patch). On `ErrDeletePipelineFailed`, **in-memory** `MarkDeleteWorkflowAsFailed` and `r.Client.Status().Update(rr)` (line 794). **Status mutation.**
  - After delete pipeline completes: `controllerutil.RemoveFinalizer` + `r.Client.Update(rr)` (806). **Spec/meta mutation** (finalizer removal).
- **Return**:
  - Error → `ctrl.Result{}, err`.
  - Requeue (job still running) → `defaultRequeue, nil`.
  - Finalizer removed → `ctrl.Result{}, nil`.
- **Re-entry**: Job watch on job completion; self-watch on finalizer removal.
- **Classification**: spec/meta + external + status (status only on failure path).

### D3. Work finalizer present
- **Lines**: 812–818
- **Mutations**: `deleteWork` (893–924):
  - Lists works; if none → `RemoveFinalizer` + `r.Client.Update`. **Spec/meta.**
  - Else: ensure trace annotations on each work (`r.Client.Update` on works — external) and `r.Client.Delete` on each work (external).
- **Return**: `fastRequeue, nil` after the call (regardless), or `ctrl.Result{}, err` on error.
- **Re-entry**: 5s timer or Work watch.

### D4. Remove-all-workflow-jobs finalizer present
- **Lines**: 820–826
- **Mutations**: `deleteWorkflows` (926–963):
  - Lists Jobs matching labels (and legacy labels); deletes them (external).
  - If everything deleted → `RemoveFinalizer` + `r.Client.Update(rr)`. **Spec/meta.**
- **Return**: `fastRequeue, nil` or `ctrl.Result{}, err`.
- **Re-entry**: Job watch or 5s timer.

### D5. ResourceBinding finalizer present (feature-flagged)
- **Lines**: 828–836
- **Mutations**: `deleteResourceBinding` (862–891):
  - GET binding. If NotFound → `RemoveFinalizer` + `r.Client.Update(rr)`. **Spec/meta.**
  - Else `r.Client.Delete(binding)`. External.
- **Return**: `fastRequeue, nil` or `ctrl.Result{}, err`.
- **Re-entry**: 5s timer or ResourceBinding watch.

### D6. Fallthrough
- **Line**: 838
- Returns `fastRequeue, nil`. Should only hit if no finalizers matched any branch (transient race).

---

## 3. Write-callsite inventory

### 3.1 `r.Client.Status().Update(ctx, rr)` on the RR — 14 callsites
(All mutate the RR's status subresource. All currently self-re-enqueue.)

| # | Line | Branch | Caller | Classification | Coalesce candidate? |
|---|------|--------|--------|---------------|---------------------|
| 1 | 240 | B12 | Reconcile inline (after `MarkReconciledPending("Unpaused")`) | status | Yes — paired with prior spec/meta write |
| 2 | 321 | B22 | Reconcile inline (after `updatePromiseVersionStatus`) | status | **Yes — duplicate of B19 sub-mutator** |
| 3 | 349 | B19 | `ensureResourceStatus` (already coalesces 4 in-memory mutators) | status | Already coalesced; merge with others |
| 4 | 369 | B20 | `updateResourceBindingVersionStatus` — **on the ResourceBinding, not RR** | external | n/a |
| 5 | 456 | B14 | `ensureConfigureWorkflowStatus` | status | **Yes — flagged in `docs/perf-rig-findings.md`** |
| 6 | 499 | B15a | `reconcileSuspendedWorkflow` (after GET-fresh-copy) | status | Hard — depends on prior spec write |
| 7 | 653 | B2/B3 | `setPausedReconciliationStatusConditions` | status | Branch is terminal; keep |
| 8 | 665 | B15c/B15d | `setWorkflowSuspendedStatusCondition` | status | Branch is terminal/waits; keep |
| 9 | 742 | B18 | `cleanupWorkflowCountersAndExecution` | status | Yes — coalesce with terminal write |
| 10 | 794 | D2 | `deleteResources` error path | status | No — diagnostic write on failure |
| 11 | 1022 | B7 | `ensurePromiseIsUnavailable` | status | Branch terminal; keep |
| 12 | 1033 | B8 | `ensurePromiseIsAvailable` | status | Yes — coalesce |
| 13 | 1178 | B17 | `updateObservedGeneration` | status | **Yes — high-value coalesce target** |
| 14 | 1230 | B21 | `updateLastSuccessfulConfigureWorkflowTime` | status | Yes — coalesce |

Plus, inside `lib/workflow/reconciler.go`:

| # | Line | Branch | Caller | Classification | Coalesce candidate? |
|---|------|--------|--------|---------------|---------------------|
| W1 | workflow:307 | B16 | `reconcileWorkflowStatus` (workflow counters + pipeline phase) | status on RR | Hard — drives passiveRequeue semantics |
| W2 | workflow:690 | B16 | `setPipelineStartingStatus` (Reconciled=Pending, ConfigureWorkflowCompleted=Running, phase=Running) | status on RR | Yes — overlap with B14, B19 |

### 3.2 `r.Client.Update(ctx, rr)` on the RR — 6 callsites (spec/meta, mostly load-bearing)

| # | Line | Branch | Caller | What it mutates | Can defer? |
|---|------|--------|--------|-----------------|------------|
| 1 | 488 | B15a | `reconcileSuspendedWorkflow` | labels (WorkflowRunFromStart=true, remove WorkflowSuspended) | **No** — workflow code reads on next pass |
| 2 | 518 | B15b | `reconcileSuspendedWorkflow` | labels (remove WorkflowSuspended) | No |
| 3 | 806 | D2 | `deleteResources` | finalizers (remove runDeleteWorkflows) | No — drives next delete sub-branch |
| 4 | 874 | D5 | `deleteResourceBinding` | finalizers (remove resourceBinding) | No |
| 5 | 902 | D3 | `deleteWork` | finalizers (remove workFinalizer) | No |
| 6 | 957 | D4 | `deleteWorkflows` | finalizers (remove removeAllWorkflowJobs) | No |
| 7 | 1003 | B11 | `updateManualReconcileToTrue` | labels (set ManualReconciliation=true) | No — workflow reads on next pass |
| 8 | 1190 | B4 | `setPromiseLabels` | labels (PromiseName) | No — gates all downstream selectors |
| 9 | shared.go:136 | B12 | `ensureWorkflowRunsFromStart` | labels | No |
| 10 | shared.go:148 | B9 | `addFinalizers` | finalizers | No |
| 11 | workflow:642 | B16 | `removeLabel` (manual + run-from-start) | labels | No |

(Earlier grep said ~6; counting the helpers across files brings it to ~11. All are spec/meta and load-bearing — none can be deferred to a terminal RR write.)

### 3.3 External-object writes (independent of RR self-watch)

- `r.Client.Status().Update(resourceBinding)` at line 369 (B20).
- `controllerutil.CreateOrUpdate(resourceBinding)` at line 382 (B10).
- `r.Client.Delete(resourceBinding)` at lines 856, 882 (D1, D5).
- `r.Client.Update/Delete(work)` inside `deleteWork` (D3).
- `r.Client.Delete(job)` inside `deleteWorkflows` and `workflow.cleanup` (D4, B16).
- `applyResources(...)` / `deleteResources(...)` inside `workflow.createConfigurePipeline` (B16) — Jobs and ancillary objects.
- `suspendJob` patch inside `workflow.handleCurrentPipelineJob` / `workflow.ReconcileDelete` (B16, D2).

---

## 4. Coalesce decision table

Reading the table in section 3.1 and the branch returns: in a single-pass redesign, the following status writes can be combined into one terminal `Status().Update` at the end of Reconcile:

| Sub-mutation | Currently writes at | Coalesce safety |
|--------------|--------------------|--------|
| `MarkReconciledPending("Unpaused")` (B12) | line 240 | Safe — paired spec write at shared.go:136 stays |
| `ensureConfigureWorkflowStatus` workflows counter + pipeline slice init (B14) | line 456 | Mostly safe — but downstream `workflow.ReconcileConfigure` reads `status.kratix.workflows.pipelines` — verify the in-memory rr is the same object passed in |
| `ensureResourceStatus` aggregator (B19) | line 349 | Safe — already aggregated; just don't write, accumulate |
| `updateResourceBindingVersionStatus` (B20) | line 369 | **No** — it's a write on the binding, not the RR; cannot coalesce |
| `cleanupWorkflowCountersAndExecution` (B18) | line 742 | Safe |
| `ensurePromiseIsAvailable` (B8) | line 1033 | Safe |
| `updateObservedGeneration` (B17) | line 1178 | Safe — high value (generation observation is the canonical "I saw your spec" marker) |
| `updateLastSuccessfulConfigureWorkflowTime` (B21) | line 1230 | Safe |
| `updatePromiseVersionStatus` (B22) | line 321 | Safe — and *delete the duplicate in B19* |
| `setPipelineStartingStatus` (workflow:690) | workflow:690 | Conditional — its current placement runs *after* `applyResources` (Job create); if you coalesce, ensure status writes still happen even when Reconcile returns early for `passiveRequeue` from B16 |

The following status writes are inside branches that **return immediately and wait for external events** — they are *not* state-machine transitions and should remain as-is:

- B2/B3 paused (terminal until label flips).
- B7 promise-unavailable (timer/external).
- B15c/B15d suspended (timer/wait).
- D2 delete-failed (diagnostic, on error path).

The following are **spec/meta writes** (line table 3.2) and **must remain as separate `Update` calls before the workflow logic reads them**:

- All finalizer additions/removals.
- All label flips (`PromiseNameLabel`, `ManualReconciliationLabel`, `WorkflowRunFromStartLabel`, `WorkflowSuspendedLabel`).
- Trace annotation persistence (B1, `persistReconcileTrace`).

---

## 5. Key invariants the refactor must preserve

1. **`generation` vs `observedGeneration`** — `updateObservedGeneration` (B17) currently returns after writing. If coalesced into a terminal write, ensure the new design still bumps observedGeneration on every reconcile that successfully observes a new generation, and that downstream consumers (CLI, dashboards) see the bump.
2. **`status.kratix.workflows.pipelines` slice is read inside `workflow.ReconcileConfigure`** via `resourceutil.GetSuspendedPipelineIndex` (workflow:204) and elsewhere. `ensureConfigureWorkflowStatus` (B14) initialises this. In a single-pass design, the slice must be present in the in-memory `rr` before `reconcileConfigure` runs, but the API write can be deferred — verify both sites operate on the same `rr` object reference.
3. **`reconcileWorkflowStatus` returns `passiveRequeue=true` and short-circuits** in the existing flow. Its self-write at workflow:307 currently triggers a re-pass that picks up the new counters and falls through. In the refactor, either preserve the short-circuit or guarantee that the same-pass continuation reads the updated in-memory `rr`.
4. **`updatePromiseVersionStatus` is invoked twice** (B19 sub-call at line 538 and B22 at line 320). The version status logic is idempotent so both calls produce the same answer, but the duplication is a code smell. The refactor should call it once.
5. **`setPausedReconciliationStatusConditions` is guarded** — it checks the existing Reconciled condition and only writes if different. The refactor should adopt this idempotency pattern for all coalesced status updates so empty rounds don't trigger API writes.
6. **In-memory condition helpers in `lib/resourceutil/util.go`** (`MarkReconciledTrue`, `MarkResourceRequestAsWorksSucceeded`, `MarkConfigureWorkflowAsRunning`, etc.) only mutate the in-memory `rr.Object`. They never write to the API. The actual write is always a separate `Status().Update` callsite. This is the structural assumption that makes coalescing viable.
