# Pod TTL Cleanup Plan

## Clarifications and Assumptions
No blocking clarifying questions are required before drafting this plan.

Assumptions used:
1. `podTTLSecondsAfterFinished` is a top-level Kratix Config property (inside `data.config`).
2. TTL cleanup targets workflow Pods in terminal state, regardless of whether the Job ended in success or failure.
3. If TTL is not set, no new pod-TTL behavior is applied. Existing Job cleanup by `numberOfJobsToKeep` is unchanged.

## 1. Proposed UX

### User-facing config
Add a new optional Kratix Config property:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kratix
  namespace: kratix-platform-system
data:
  config: |
    podTTLSecondsAfterFinished: 60s
    workflows:
      jobOptions:
        defaultBackoffLimit: 4
```

### Behavior
- When `podTTLSecondsAfterFinished` is set to a positive duration (for example `60s`):
  - Kratix automatically deletes workflow Pods once they have been in a terminal state for at least that duration.
  - Job objects remain managed exactly as today (no change to Job lifecycle strategy).
- When `podTTLSecondsAfterFinished` is omitted:
  - Kratix does not run pod TTL cleanup.
  - Existing behavior (including `numberOfJobsToKeep`-based Job cleanup) remains unchanged.
- Invalid or non-positive values:
  - `<= 0` is treated as disabled and logged.
  - Malformed duration values should be surfaced clearly as config errors.

### Operational UX expectations
- The cleanup is automatic; users do not need extra labels/annotations on Promises or Resource Requests.
- TTL is global (cluster-wide Kratix config), matching the existing Kratix Config UX.
- Logs include pod cleanup decisions (`deleted`, `not-yet-expired`, `ttl-disabled`) for diagnosability.

## 2. High-Level Design

### A. Config plumbing
1. Extend `KratixConfig` in `cmd/main.go` with `podTTLSecondsAfterFinished` (duration type).
2. Add helper/defaulting logic similar to `getNumJobsToKeep`:
   - return `nil`/disabled when not set.
   - normalize invalid non-positive values to disabled with warning logs.
3. Thread the value into controller structs:
   - `PromiseReconciler`
   - `DynamicResourceRequestController`
4. Thread into `workflow.Opts` via `workflow.NewOpts(...)`.

### B. Workflow cleanup logic
1. Extend `lib/workflow/reconciler.go` cleanup flow with pod cleanup:
   - keep current Job cleanup and Work cleanup unchanged.
   - add `cleanupPods(...)` invoked from `cleanup(...)`.
2. Pod selection strategy:
   - list Pods in workflow namespace using existing workflow labels (same label model used for Jobs).
   - ensure Pods are Kratix-managed workflow Pods (label-based filter).
3. Pod eligibility:
   - only terminal Pods are candidates (`Succeeded` and `Failed` to satisfy "job status should not matter").
   - compute completion timestamp from container `terminated.finishedAt` (fallback to pod timestamp if needed).
   - delete when `now - completedAt >= podTTLSecondsAfterFinished`.
4. Track the soonest future expiry among non-expired terminal Pods.

### C. Requeue behavior (critical for 60s TTL)
Current default reconciliation interval is long (10h), so TTL cleanup needs explicit requeue support.

Design:
1. Return/compute `nextPodCleanupAfter` from workflow cleanup when there are terminal Pods not yet expired.
2. In Promise and DynamicResourceRequest reconciliations, if `nextPodCleanupAfter` exists, return `ctrl.Result{RequeueAfter: nextPodCleanupAfter}` (or min with existing periodic interval).
3. This ensures a pod with `60s` TTL is cleaned up shortly after 60s even without external events.

### D. RBAC and manifests
1. Add controller RBAC permissions for Pods (`get`, `list`, `watch`, `delete`) via kubebuilder markers.
2. Regenerate manifests so `config/rbac/role.yaml` and chart/distribution outputs include pod permissions.

### E. Testing strategy

#### Unit tests
- `lib/workflow/reconciler_test.go`:
  - TTL configured + terminal pod older than TTL -> pod deleted.
  - TTL configured + pod younger than TTL -> pod kept and next requeue suggested.
  - TTL not configured -> no pod deletion by TTL logic.
  - cleanup not gated by Job success/failure outcome.
- Controller tests:
  - Promise and Dynamic controllers honor pod-cleanup requeue timing when TTL is set.

#### Config tests
- Add `cmd/main_test.go` coverage for parsing/defaulting/validation of `podTTLSecondsAfterFinished`.

#### System tests
- Extend system tests with the two acceptance scenarios:
  - TTL set to `60s`: workflow pods are deleted after TTL.
  - TTL absent: workflow pods are not TTL-deleted by Kratix.

### F. Non-goals / constraints
- Do not enable Kubernetes Job `ttlSecondsAfterFinished` (would delete Jobs, conflicting with current Kratix workflow tracking).
- Do not change existing Job retention semantics (`numberOfJobsToKeep`).
