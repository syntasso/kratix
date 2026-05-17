# Kratix controller perf — findings to date

## TL;DR

- `MaxConcurrentReconciles` alone does not move the needle on the workloads
  measured. The headline prediction in `docs/scaling-analysis-2500-rrs.md`
  (raise MCR → ~10× throughput) **did not hold** at N=500.
- **Real bottleneck**: each no-op RR is reconciled **~20 times** before it
  settles. The dynamic RR controller's reconcile is a state machine where
  almost every branch ends in a `Status().Update()` that re-enqueues the
  same RR through its own self-watch (which has no predicate).
- **Attempted fix (custom predicate filtering status-only updates)**: cut
  reconciles per RR from 20 → 2 — a real 10× reduction in controller load.
- **But the fix surfaced a correctness concern** that needs investigation
  before shipping. See "Open questions" below.

## What we observed

### MCR=1 (baseline) vs MCR=10 — N=500

| metric | baseline (MCR=1) | patched (MCR=10) |
|---|---:|---:|
| time-to-converge | 336s | 335s |
| reconciles done | 3,605 | 4,673 |
| peak workqueue depth | 499 | 490 |
| peak longest-running goroutine | 0.085s | 0.96s |

MCR=10 ran goroutines in parallel (`longest_running` jumped 11×) and
processed ~30% more reconciles — but total work scales the same way, so
wall-clock didn't move. **The patch is doing what it says on the tin; the
workload simply doesn't benefit from it.**

### Why so much work?

```
$ kubectl logs ... | grep '"reconciliation started"' | grep -oE 'perf-baseline-500-[0-9]+' | sort | uniq -c | sort -rn | head -5
  20 perf-baseline-500-00500
  20 perf-baseline-500-00499
  20 perf-baseline-500-00498
  20 perf-baseline-500-00497
  20 perf-baseline-500-00496
```

**Every RR reconciles exactly 20 times** to settle. With no workflows. With
no external triggers. Pure self-induced churn from the controller's own
Status writes re-firing the self-watch.

Mechanism: `dynamic_resource_request_controller.go:91-327` is a long state
machine. There are ~22 callsites that issue `r.Client.Status().Update(...)`
or `r.Client.Update(...)` inside one reconcile path. Each one mutates the
RR, fires the self-watch (`promise_controller.go:1188`, `For(unstructuredCRD)`
with no predicate), and re-enqueues. Next pass advances one more state,
writes again. Repeat ~20 times.

Compare to the **Job watch** right next to it (`promise_controller.go:1190-1196`)
which *does* have a label-filter predicate. The self-watch has nothing.

### Predicate experiment

Adding a predicate that filters status-only updates dropped reconciles per
RR from 20 to 2 — confirmed by log counting. Each RR's reconcile durations
in the patched run were ~1ms, generation=1.

The patch code:

```go
// resourceRequestReconcilePredicate filters events on the dynamic RR self-watch
// so status-only updates do not re-enqueue the RR. Without it, each of the ~20
// Status().Update() calls inside one Reconcile triggers another Reconcile pass.
//
// Create, Delete, and Generic pass through unconditionally — the controller
// must observe these. Update fires only when something the reconcile actually
// reacts to has changed: generation (spec edit), finalizers (cleanup state),
// or deletion timestamp.
func resourceRequestReconcilePredicate() predicate.Predicate {
    return predicate.Funcs{
        CreateFunc:  func(event.CreateEvent) bool { return true },
        DeleteFunc:  func(event.DeleteEvent) bool { return true },
        GenericFunc: func(event.GenericEvent) bool { return true },
        UpdateFunc: func(e event.UpdateEvent) bool {
            if e.ObjectOld == nil || e.ObjectNew == nil {
                return true
            }
            if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
                return true
            }
            if !reflect.DeepEqual(e.ObjectOld.GetFinalizers(), e.ObjectNew.GetFinalizers()) {
                return true
            }
            oldDel := e.ObjectOld.GetDeletionTimestamp()
            newDel := e.ObjectNew.GetDeletionTimestamp()
            if (oldDel == nil) != (newDel == nil) {
                return true
            }
            return false
        },
    }
}
```

Wired in `promise_controller.go:~1188`:

```go
dynamicController, err := ctrl.NewControllerManagedBy(r.Manager).
    For(unstructuredCRD, builder.WithPredicates(resourceRequestReconcilePredicate())).
    WithOptions(dynamicCtrlOpts).
    Watches(...)
```

**The concerning observation**: in the patched run, the final state of the
RRs showed **zero finalizers**, where the baseline run had 3 finalizers per
RR (`work-cleanup`, `workflows-cleanup`, `delete-workflows`). Possibilities:

- (a) The patch silently broke `addFinalizers` — correctness bug.
- (b) The finalizers were added but removed before we observed.
- (c) The observation tooling was reading stale state.

Not yet disambiguated. See "Open questions".

## Patches applied (uncommitted, this branch)

### Patch 1: `controllerConcurrency` config knob (kept)

`cmd/main.go`:

```go
type ControllerConcurrency struct {
    Default                int `json:"default,omitempty"`
    Promise                int `json:"promise,omitempty"`
    DynamicResourceRequest int `json:"dynamicResourceRequest,omitempty"`
    Work                   int `json:"work,omitempty"`
    WorkPlacement          int `json:"workPlacement,omitempty"`
    ResourceBinding        int `json:"resourceBinding,omitempty"`
}

func concurrencyFor(kConfig *KratixConfig, controller string) int { ... }
```

Wired into each `SetupWithManager` via a new `MaxConcurrentReconciles` field
on each reconciler struct, defaulting to controller-runtime's default of 1
when unset. **Zero behaviour change without an opt-in ConfigMap.**

Opt-in via:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kratix
  namespace: kratix-platform-system
data:
  config: |
    workflows:
      defaultContainerSecurityContext: {}
    controllerConcurrency:
      default: 10
```

**Safe to ship on its own.** It gives operators a knob and costs nothing
when unset. On its own it does not move the wall-clock number for the
scenarios measured. Whether that's worth shipping is a judgment call — it
*would* matter at higher N with more diverse reconcile costs, just not for
what we drove.

### Patch 2: Self-watch predicate (reverted pending discussion)

The predicate function above. Currently in `promise_controller.go` but **not
wired** — the `For(...)` call no longer references it. Re-enable by changing
back to:

```go
For(unstructuredCRD, builder.WithPredicates(resourceRequestReconcilePredicate())),
```

## Open questions

1. **Did the predicate actually break finalizer-add, or was the observation
   wrong?** The reconcile's early-return pattern (`return ctrl.Result{}, r.Client.Status().Update(ctx, rr)`)
   assumes "I made a change, I'll get re-invoked, and on the next pass my
   change will be visible." When we filter the re-enqueues that assumption
   may break — specifically the `setPromiseLabels` → finalizers-missing →
   `addFinalizers` sequence depends on the *second* reconcile observing the
   first's label write.

2. **If the predicate is correct**, are there *other* parts of the codebase
   that rely on self-watch re-enqueues as an implicit synchronisation
   mechanism? Grep for `r.Client.Status().Update` and `r.Client.Update`
   followed by `return ctrl.Result{}, nil` — those are the suspect
   callsites.

3. **The Promise controller has the same pattern.** We only patched the
   *dynamic RR* controller. If we go further with this approach, Promise's
   own `SetupWithManager` (`promise_controller.go:1757`) probably needs the
   same predicate.

4. **Should we pursue option B (coalesce status writes) instead?**
   - **Option A (predicate)** masks the symptom: re-enqueues are filtered,
     but the reconcile still issues 22 separate Update API calls per RR per
     converge cycle. At 2500 RRs that's 55k writes against the apiserver.
   - **Option B (coalesce)** restructures the reconcile so it computes the
     desired status in memory and writes once at the end. ~200-500 lines of
     refactor, but eliminates both the re-enqueue storm *and* the apiserver
     write storm. Higher review burden, lower long-term risk.

   The predicate finding *strengthens* the case for B because we now have
   evidence the 22 writes per RR are real and observable.

## Files touched (uncommitted on this branch)

- `cmd/main.go` — `ControllerConcurrency` struct + `concurrencyFor()` helper, wired into each `SetupWithManager` call
- `internal/controller/promise_controller.go` — `MaxConcurrentReconciles` + `DynamicRRMaxConcurrentReconciles` fields, `WithOptions(...)` on both Promise and dynamic-RR builders, **plus** the predicate function (defined but **not currently wired**)
- `internal/controller/work_controller.go` — `MaxConcurrentReconciles` field, `WithOptions(...)`
- `internal/controller/workplacement_controller.go` — `MaxConcurrentReconciles` field, `WithOptions(...)`
- `internal/controller/resourcebinding_controller.go` — `MaxConcurrentReconciles` field, `WithOptions(...)`

## Useful commands for verifying any future approach

```bash
# Per-controller queue depth live
kubectl -n kratix-platform-system port-forward deploy/kratix-platform-controller-manager 8080:8080 &
curl -fsS http://localhost:8080/metrics | awk '/^workqueue_depth\{/'

# Reconciles per RR
kubectl -n kratix-platform-system logs deploy/kratix-platform-controller-manager \
  | grep '"reconciliation started"' \
  | grep -oE '<name-pattern>-[0-9]+' \
  | sort | uniq -c | sort -rn | head
```
