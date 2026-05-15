# Kratix controller scaling analysis — 2500 namespaces, 1 request each

**Date:** 2026-05-05
**Branch:** `main` @ `6febbca4`
**Scenario:** ~2500 resource requests cluster-wide, distributed 1-per-namespace across 2500 namespaces.

This flips the failure mode from the single-namespace case. Instead of "hot list within one namespace", the pain shifts to **cluster-wide fan-out and per-namespace overhead**. See `scaling-analysis-2500-rrs.md` for the single-namespace analysis.

## What stays equally bad

### Same as single-namespace case

- **`MaxConcurrentReconciles=1`** (`cmd/main.go` + every `SetupWithManager`) — irrelevant to namespace layout. Still 2500 RRs draining through one goroutine. Same throughput cliff.
- **`reconcileAllRRs` cluster-wide List + Update** (`internal/controller/promise_controller.go:1099-1121`) — doesn't filter by namespace anyway. Still 2500 etcd writes on every Promise spec change. **Possibly worse**: in the single-namespace case you could trivially scope the List to one namespace; with 2500 namespaces you genuinely need a label selector. The fix is the same but the "narrow to namespace" shortcut isn't available.
- **`requestReconciliationOfWorksOnDestination`** (`internal/controller/work_controller.go:269-288`) — still cluster-wide. 2500 Works enqueued on every Destination event. Same storm.
- **Unconditional status writes** in `ensurePromiseIsAvailable` (`internal/controller/dynamic_resource_request_controller.go:1004-1010`) — same bug, 2500× per sweep.

## What gets *better*

The cache splits namespaced objects by namespace internally. So:

- **`getWorksStatus`** (`internal/controller/dynamic_resource_request_controller.go:680-684`) — Lists with `Namespace: rr.GetNamespace()` + label selector. With one RR per namespace, the namespace bucket has ~1 Work. Effectively O(1).
- **`fetchResourceBinding`** (`internal/controller/dynamic_resource_request_controller.go:1018-1024`) — same: per-namespace lookup against ~1 object.
- **Finding #4 from the original analysis (missing field indexers) mostly disappears** — the indexer fix matters far less in this layout. Still good hygiene, but no longer a HIGH.

## What gets *worse*

### A. Namespace-scoped objects multiply

Every namespace likely carries its own:

- ServiceAccount / Role / RoleBinding for pipeline jobs
- ConfigMap holding the destination selectors / scheduling output
- Secrets propagated to the workflow pods
- Pipeline Jobs (and their Pods) per RR reconcile

`lib/pipeline/` and `lib/workflow/` create per-namespace RBAC for the workflow service account. At 2500 namespaces that's ~10k extra cluster objects all of which the controller cache holds.

### B. Workflow Job/Pod cleanup controller churn

`internal/controller/workflow_job_pod_cleanup_controller.go` runs cluster-wide. 2500 Jobs (one per RR pipeline run) means continuous churn through that reconciler.

### C. Cache memory

controller-runtime keeps an informer-backed cache for every watched type, cluster-wide by default. 2500 namespaces × N kinds = significantly more memory than 2500 objects in one namespace, because each object carries namespace metadata + ResourceVersion tracking + per-namespace index buckets.

### D. API server throttling

kube-apiserver has per-namespace audit and admission overhead. 2500 concurrent `kubectl apply` calls hitting 2500 namespaces is a different beast from one namespace getting hammered.

### E. RBAC explosion

If Kratix sets up per-namespace `Role`/`RoleBinding` for the pipeline ServiceAccount (it does — check `lib/pipeline/`), you get 2500× the RBAC objects, each watched by the API server.

## New issues that surface in this layout

### NEW-1: Dynamic RR controller's Work watch evaluates every Work in the cluster (HIGH)

`internal/controller/promise_controller.go:1188-1203`:

```go
Watches(
    &v1alpha1.Work{},
    handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
        work := obj.(*v1alpha1.Work)
        rrName, labelExists := work.Labels[v1alpha1.ResourceNameLabel]
        if !labelExists || work.Labels[v1alpha1.PromiseNameLabel] != promise.GetName() {
            return nil
        }
        ...
    }),
),
```

The map function fires for **every Work in the cluster**, then filters by label. With 2500 namespaces × ~3 Works per RR = 7500 Works, every Work status update runs this map function 7500× across cascade events. It returns nil for non-matching but you're still paying evaluation cost on every event.

**Fix:** add a predicate to the Watch so non-matching Works never reach the map function:

```go
Watches(&v1alpha1.Work{}, ...,
    builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
        l := obj.GetLabels()
        return l[v1alpha1.PromiseNameLabel] == promise.GetName()
    })),
)
```

Same fix applies to the ResourceBinding watch at `:1205-1215`.

### NEW-2: Job watch is cluster-wide (MEDIUM)

`internal/controller/promise_controller.go:1180-1186` — Jobs watched cluster-wide with a `ManagedByLabel == "kratix"` predicate. Good predicate, but at 2500 namespaces with active pipelines, Job churn is high. Each Job event runs `r.jobEventHandler(promise)`. Worth verifying that handler isn't doing per-event Lists.

### NEW-3: Per-namespace ServiceAccount/Role/RoleBinding creation (LOW–MEDIUM)

Pipeline workflow setup creates RBAC in each namespace. 2500 namespaces means 2500× RBAC creation, each running through admission. With 1 RR per namespace there's no intra-namespace contention, but if the namespace itself was just created, you may hit ordering issues where the SA isn't ready when the Job starts. Worth instrumenting.

### NEW-4: Informer cold-start time (HIGH on restart)

On controller restart, controller-runtime hydrates caches by Listing every watched type cluster-wide. With 2500 namespaces × ~8 kinds Kratix watches (RR, Work, WorkPlacement, ResourceBinding, Job, Promise, Destination, ConfigMap), startup is now 20k+ object Lists. Liveness probe timeouts on restart become a real risk.

**Fix:** consider `cache.Options{ByObject: ...}` to scope which namespaces are watched if you have a logical tenant boundary, or split the deployment by Promise.

### NEW-5: Namespace deletion sweeps (HIGH during offboarding)

When a namespace is deleted, the API server cascades deletes through every namespaced object. The Kratix finalizers on RR / Work / WorkPlacement / Job all fire roughly simultaneously across many namespaces if a tenant offboarding script deletes batches. The single-goroutine controllers will choke. Compounds with the `MaxConcurrentReconciles=1` issue.

## Recommended fix order (this layout)

1. **`MaxConcurrentReconciles: 10+`** — same as the single-namespace case, still #1.
2. **Add predicate to the dynamic RR controller's Work watch** (`promise_controller.go:1188`) so the map function isn't evaluated for every Work in the cluster. Same for the ResourceBinding watch at `:1205`.
3. **Filter `requestReconciliationOfWorksOnDestination`** — same as before, still important.
4. **Skip the `Update` in `reconcileAllRRs`** when label already present — stops thundering herd.
5. **Investigate cache scoping** for restart performance (NEW-4).

The single-namespace-specific fixes (field indexers, deterministic naming for `fetchResourceBinding`) drop in priority. The cluster-wide watch fixes become more urgent.

## What I haven't dug into

- `lib/pipeline/` and `lib/workflow/` — the actual RBAC/SA creation pattern per namespace.
- `workflow_job_pod_cleanup_controller.go` — likely contains its own scaling characteristics worth a separate look.
- The actual API server / etcd metrics under load — this is static analysis only.

## Mixed real-world layout

Real production is rarely "all in one namespace" or "all in separate namespaces". For a mixed layout (e.g. 200 namespaces, 10–50 RRs each):

- All five single-namespace fixes matter.
- All five 2500-namespace fixes matter.
- The watch predicate fixes (NEW-1) and concurrency fix have the highest universal payoff.

If your actual ratio is known, the prioritisation can be tightened further.
