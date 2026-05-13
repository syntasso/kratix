# Kratix controller scaling analysis — 2500 resource requests, single namespace

**Date:** 2026-05-05
**Branch:** `main` @ `6febbca4`
**Scenario:** ~4000 resource requests cluster-wide, ~2500 against a single Promise in a single namespace.

You're going to feel this hard at 2500 RRs in one namespace. Single-promise tenancy is the worst case for the current design because almost every cross-controller fan-out is *unfiltered cluster-wide* and every controller runs at concurrency 1.

## Top issues, verified against the code

### 1. Every controller runs `MaxConcurrentReconciles=1` (HIGH)

`cmd/main.go` + every `SetupWithManager` — no controller calls `.WithOptions(controller.Options{MaxConcurrentReconciles: N})`. controller-runtime defaults to 1.

At 2500 RRs that means a single goroutine drains the workqueue. If each RR reconcile takes ~200 ms (List + Status update + watch chatter), one full sweep takes ~8 minutes — worse with the storms below piling on. This is the single highest-leverage fix.

**Fix:** `.WithOptions(controller.Options{MaxConcurrentReconciles: 10})` on Promise / dynamic RR / Work / WorkPlacement. The dynamic RR controller is the most urgent — it's set up at `internal/controller/promise_controller.go:1177`.

---

### 2. `PromiseReconciler.reconcileAllRRs` does an unfiltered cluster-wide List + per-item Update (HIGH)

`internal/controller/promise_controller.go:1099-1121`:

```go
err := r.Client.List(ctx, rrs)               // no namespace, no label selector
for _, rr := range rrs.Items {
    newLabels[resourceutil.ManualReconciliationLabel] = "true"
    r.Client.Update(ctx, &rr)                  // 2500 sequential etcd writes
}
```

Triggered by `shouldReconcileResources` (`promise_controller.go:807`), which fires whenever `generation != observedGeneration` **or** the `ReconcileResourcesLabel` is present on the Promise. Every Promise spec bump → 2500 etcd writes → 2500 RR watch events → 2500 RR reconciles run serially behind one goroutine.

**Fix:** narrow the List to the Promise's GVK only (already done — it's the unstructured GVK, good) but also skip the `Update` where the label is already set. Better: don't bulk-label at all; bump a Promise generation field on the RR controller's watch and let the dynamic RR controller's watch on the Promise pick it up.

---

### 3. `WorkReconciler.requestReconciliationOfWorksOnDestination` enqueues every Work on every Destination event (HIGH)

`internal/controller/work_controller.go:269-288`:

```go
allWorks := &v1alpha1.WorkList{}
err := r.Client.List(ctx, allWorks)            // cluster-wide, no filter
for _, work := range allWorks.Items {
    requests = append(requests, reconcile.Request{...})
}
```

With 2500 RRs each producing one or more Works, every Destination status tick (heartbeats, condition updates) re-enqueues thousands of Works through the single-goroutine Work controller. This is your most dangerous storm — Destinations write status frequently in healthy clusters.

**Fix:** filter Works by destination selector match before enqueuing. At minimum, gate the watch on a predicate so only label/spec changes fire it (drop status-only updates).

---

### 4. Zero field indexers for `Work` / `WorkPlacement` / `ResourceBinding` by promise/resource (HIGH)

Only 4 indexers exist (`bucketstatestore_controller.go:113`, `destination_controller.go:270`, `gitstatestore_controller.go:117`, `promiserevision_controller.go:139`). None on the hot-path types.

This means every label-selector List does an O(N) cache scan:

- `internal/controller/dynamic_resource_request_controller.go:680-684` — `getWorksStatus`: per RR reconcile → 2500 scans of 2500 Works = ~6.25M comparisons per full sweep.
- `internal/controller/dynamic_resource_request_controller.go:1018-1024` — `fetchResourceBinding`: per RR (when `PromiseUpgradeFeatFlag` is on) → another 2500 scans of N ResourceBindings.

**Fix:** register indexers in `cmd/main.go`, e.g.:

```go
mgr.GetFieldIndexer().IndexField(ctx, &v1alpha1.Work{}, "metadata.labels.kratix.io/promise-name", ...)
```

controller-runtime's cache only honours field indexers, not label indexers, so use a synthetic field via an extractor. Or — much simpler — name the Work and the ResourceBinding deterministically (`<promise>-<rr>-<ns>`) and use `Get()` instead of `List`+selector. `fetchResourceBinding` is the obvious target; a single resource has exactly one binding.

---

### 5. `ensurePromiseIsAvailable` writes status unconditionally (MEDIUM)

`internal/controller/dynamic_resource_request_controller.go:1004-1010`:

```go
func (r *DynamicResourceRequestController) ensurePromiseIsAvailable(...) error {
    resourceutil.MarkPromiseConditionAsAvailable(rr, logger)
    return r.Client.Status().Update(ctx, rr)   // always writes
}
```

At 2500 RRs each requeue writes to etcd → triggers another watch event → re-enters reconcile.

**Fix:** call `MarkPromiseConditionAsAvailable`, check whether `meta.SetStatusCondition` returned true, only Update if it changed.

(`setWorkflowSuspendedStatusCondition` at `:660-669` already does this correctly — model it on that.)

---

### 6. `scheduler.go:534` lists all Destinations on every Work reconcile (MEDIUM)

Unfiltered `s.Client.List(ctx, destinationList, lo)` per Work. 2500 Works × N Destinations per sweep. Cache-served so cheap, but still O(N) scan and contributes to the per-reconcile budget that compounds with #1.

**Fix:** cache the destination list at the controller level keyed by selector hash; invalidate on Destination events.

---

### 7. Promise self-watch with `EnqueueRequestsFromMapFunc` for `RequiredBy` (LOW–MEDIUM)

`internal/controller/promise_controller.go:1749-1758` — every Promise event triggers reconcile of all Promises that depend on it. Fine at low scale, but if a "platform" Promise is required-by many tenants, a single status write fans out widely. With #2 in play, each downstream Promise reconcile may then run `reconcileAllRRs` again.

---

### 8. Recent commit `6febbca4` (resource binding upgrade info) bakes in the per-RR List

Confirmed at `internal/controller/dynamic_resource_request_controller.go:1014-1034`. Each RR reconcile (with PromiseUpgrade enabled) lists ResourceBindings in its namespace by label. With 2500 RRs and `PromiseUpgradeFeatFlag=true`, that's 2500 cache scans per sweep on top of #4.

**Fix:** ResourceBinding has a 1:1 relationship with the RR — name it `<promise>-<rr-name>` and `Get()` it.

---

### 9. Status write on every reconcile when feature flag is on (LOW)

`internal/controller/dynamic_resource_request_controller.go:321-324`:

```go
if r.PromiseUpgradeFeatFlag {
    if r.updatePromiseVersionStatus(...) {
        return ctrl.Result{}, r.Client.Status().Update(ctx, rr)
    }
}
```

Gates on `updatePromiseVersionStatus` returning true, which is good — but verify that helper actually compares before returning true (didn't read it).

---

## Recommended fix order

1. **Add `MaxConcurrentReconciles: 10`** to dynamic RR, Work, WorkPlacement, Promise controllers. One-line change per controller, immediate ~10× throughput. Tune by watching CPU.
2. **Filter `requestReconciliationOfWorksOnDestination`** — return only Works whose scheduling could be affected by *this* Destination's change. Or at minimum, predicate-gate the watch so only label/spec changes fire it.
3. **Convert `fetchResourceBinding` to `Get()`** by deterministic name. Removes 2500 List scans per sweep.
4. **Add field indexers** for `Work` by `kratix.io/promise-name` + `kratix.io/resource-name` and use `MatchingFields` instead of `MatchingLabels` in `getWorksStatus`.
5. **Skip the `Update` in `reconcileAllRRs`** when the label is already present. Stops the thundering herd on every Promise reconcile.

Items 1–3 alone should take you from "broken at 2500" to "fine at 2500". Items 4–5 take you to "fine at 10k+".

---

## What I haven't dug into

- The `repository_cache.go` — sub-agent claimed it's bounded; not verified.
- Exact semantics of `updatePromiseVersionStatus` — needs a 30 s read to confirm it short-circuits cleanly.
- WorkPlacement reconcile — predicate at `workplacement_controller.go:278` already filters on generation+deletion which is good; didn't find a hot-loop issue there.
- Static analysis only — no live profiling. Worth confirming with `kubectl get --raw /metrics` on the controller and looking at `controller_runtime_reconcile_total` and `workqueue_depth` per controller.
