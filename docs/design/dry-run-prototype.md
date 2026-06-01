# Dry-Run Prototype

This document describes the changes made in the dry-run prototype and provides step-by-step instructions for testing the feature end-to-end.

## Overview

The prototype adds dry-run support to Kratix resource pipelines. When a ResourceRequest carries the label `kratix.io/dry-run: "true"`, Kratix:

1. Injects `KRATIX_DRY_RUN=true` into every pipeline container (user containers, work-creator, status-writer).
2. Stamps the resulting Work object with `kratix.io/dry-run: "true"`.
3. Routes that Work exclusively to a Destination labelled `kratix.io/dry-run: "true"` (never to live destinations).
4. Skips status updates on the ResourceRequest.
5. Sets the `WorksSucceeded` condition with `Reason: DryRunSucceeded` instead of `WorksSucceeded`.

When the dry-run label is later removed from the ResourceRequest (i.e. the real request is applied), the controller deletes any stale dry-run Works before running the normal pipeline.

## Changes

### `api/v1alpha1/work_types.go`

Added the label constant used across all dry-run logic:

```go
DryRunLabel = KratixPrefix + "dry-run"  // "kratix.io/dry-run"
```

### `api/v1alpha1/pipeline_types.go`

Added the environment variable name injected into pipeline containers:

```go
KratixDryRunEnvVar = "KRATIX_DRY_RUN"
```

### `api/v1alpha1/pipeline_factory.go`

- Added `IsDryRun() bool` method — returns true when the ResourceRequest carries `kratix.io/dry-run: "true"`.
- `defaultEnvVars()` appends `KRATIX_DRY_RUN=true` when `IsDryRun()` is true. This covers user containers and the status-writer container (which calls `defaultEnvVars()` internally).
- `workCreatorContainer()` also appends `KRATIX_DRY_RUN=true` when dry-run is active.

### `work-creator/lib/work_creator.go`

After building the Work's labels, stamps the dry-run label when `KRATIX_DRY_RUN=true`:

```go
if os.Getenv(v1alpha1.KratixDryRunEnvVar) == "true" {
    workLabels[v1alpha1.DryRunLabel] = "true"
}
```

This label is what the Scheduler uses to route the Work to the dry-run Destination.

### `work-creator/cmd/update_status.go`

Skips all status-update logic when `KRATIX_DRY_RUN=true`:

```go
if os.Getenv(v1alpha1.KratixDryRunEnvVar) == "true" {
    fmt.Println("dry-run mode: skipping status update")
    return nil
}
```

### `internal/controller/scheduler.go`

- Detects dry-run Works by checking `kratix.io/dry-run: "true"` on the Work label.
- Overrides the destination selector to `{kratix.io/dry-run: "true"}` for dry-run Works, in both the new-placement and update-placement paths.
- Filters the dry-run Destination out of normal scheduling — a live WorkloadGroup will never be placed on a Destination labelled `kratix.io/dry-run: "true"`.

### `lib/resourceutil/util.go`

Added `DryRunWorksSucceededReason = "DryRunSucceeded"` and a helper that sets the `WorksSucceeded` condition with that reason and a dry-run-specific message.

### `internal/controller/dynamic_resource_request_controller.go`

Two additions:

1. **`updateWorksSucceededCondition`** — when all Works are ready, sets `Reason: DryRunSucceeded` (instead of `WorksSucceeded`) when the RR still carries the dry-run label.

2. **`cleanupStaleDryRunWorks`** — runs at the top of `Reconcile` (after the deletion-timestamp check). When the dry-run label has been removed, it lists all Works with labels `{promise-name, resource-name, kratix.io/dry-run: "true"}` and deletes them. Returns `true` if any were deleted, which causes an early return so the normal pipeline re-runs after the Work deletion events settle.

## How to Test

### Prerequisites

- A running Kratix platform cluster (`kind-platform` or similar).
- A worker cluster registered as a Destination.
- The prototype branch built and deployed (`feat/dry-run-prototype`).
- MinIO or another state store configured.
- A Promise installed (the Redis or PostgreSQL sample Promise works fine).

### Step 1: Create a dry-run Destination

Create a Destination that Kratix will route dry-run Works to. It needs the `kratix.io/dry-run: "true"` label and can point to the same StateStore as your live destinations (use a distinct `path` to isolate the output).

```yaml
# dry-run-destination.yaml
apiVersion: platform.kratix.io/v1alpha1
kind: Destination
metadata:
  name: dry-run
  labels:
    kratix.io/dry-run: "true"
spec:
  path: dry-run
  stateStoreRef:
    name: default
    kind: BucketStateStore
```

```bash
kubectl apply -f dry-run-destination.yaml --context kind-platform
```

Verify it is registered:

```bash
kubectl get destinations --context kind-platform
# NAME       AGE
# worker-1   10m
# dry-run    5s
```

### Step 2: Apply a dry-run ResourceRequest

Apply a ResourceRequest with `kratix.io/dry-run: "true"`. Use whichever resource type your installed Promise expects (e.g. Redis):

```yaml
# my-redis-dry-run.yaml
apiVersion: marketplace.kratix.io/v1alpha1
kind: Redis
metadata:
  name: my-redis
  namespace: default
  labels:
    kratix.io/dry-run: "true"
spec:
  size: small
```

```bash
kubectl apply -f my-redis-dry-run.yaml --context kind-platform
```

### Step 3: Observe the pipeline Job

Wait for the pipeline Job to appear and inspect its containers:

```bash
kubectl get jobs -n kratix-platform-system --context kind-platform -w
```

Once a Job is running, inspect its environment variables:

```bash
JOB=$(kubectl get pods -n kratix-platform-system --context kind-platform \
  -l kratix.io/resource-name=my-redis \
  -o jsonpath='{.items[0].metadata.name}')

kubectl describe pod "$JOB" -n kratix-platform-system --context kind-platform \
  | grep -A2 KRATIX_DRY_RUN
```

Expected output — every container (user containers, `work-writer`, and the main `status-writer`) should show:

```
KRATIX_DRY_RUN:  true
```

### Step 4: Check the status-writer logs

```bash
kubectl logs "$JOB" -n kratix-platform-system --context kind-platform \
  -c status-writer 2>/dev/null || \
kubectl logs "$JOB" -n kratix-platform-system --context kind-platform
```

You should see:

```
dry-run mode: skipping status update
```

### Step 5: Verify the Work is labelled and routed correctly

```bash
kubectl get works -n kratix-platform-system --context kind-platform \
  -l kratix.io/resource-name=my-redis \
  -o yaml | grep -A5 labels
```

The Work should carry `kratix.io/dry-run: "true"`.

Check that a WorkPlacement was created for the `dry-run` Destination and **not** for `worker-1`:

```bash
kubectl get workplacements -n kratix-platform-system --context kind-platform \
  -l kratix.io/resource-name=my-redis
# NAME                        DESTINATION
# my-redis-<hash>             dry-run
```

```bash
# Confirm worker-1 has no new WorkPlacement for this resource
kubectl get workplacements -n kratix-platform-system --context kind-platform \
  -l kratix.io/resource-name=my-redis,kratix.io/target-destination-name=worker-1
# (no resources)
```

### Step 6: Check the dry-run condition on the ResourceRequest

Once the Work is ready, the ResourceRequest should get `WorksSucceeded=True` with `Reason: DryRunSucceeded`:

```bash
kubectl get redis my-redis -n default --context kind-platform \
  -o jsonpath='{.status.conditions[?(@.type=="WorksSucceeded")]}' | jq .
```

Expected:

```json
{
  "type": "WorksSucceeded",
  "status": "True",
  "reason": "DryRunSucceeded",
  "message": "Dry-run completed: outputs written to dry-run destination"
}
```

### Step 7: Inspect the dry-run output

Check the state store at the `dry-run` path. With MinIO:

```bash
# Port-forward MinIO if needed
kubectl port-forward -n kratix-platform-system svc/minio 9000:9000 --context kind-platform &

# List the dry-run bucket path
mc ls minio/kratix/dry-run/ --recursive
```

You should see the files the pipeline would normally write to `worker-1`, now written under the `dry-run` path instead. Nothing is written to `worker-1`.

### Step 8: Promote to a real request (label removal)

Now simulate approval — remove the dry-run label and re-apply:

```bash
kubectl label redis my-redis -n default kratix.io/dry-run- --context kind-platform
```

Watch the controller logs:

```bash
kubectl logs -n kratix-platform-system deploy/kratix-platform-manager --context kind-platform \
  -f | grep -i "dry-run"
```

You should see:

```
removing stale dry-run works  count=1
```

After the stale Works are deleted, the controller re-runs the normal pipeline:

- A new Job appears **without** `KRATIX_DRY_RUN=true`.
- A new Work appears **without** `kratix.io/dry-run: "true"`.
- A new WorkPlacement targets `worker-1`.
- The `WorksSucceeded` condition is updated to `Reason: WorksSucceeded`.

```bash
kubectl get redis my-redis -n default --context kind-platform \
  -o jsonpath='{.status.conditions[?(@.type=="WorksSucceeded")]}' | jq .
# "reason": "WorksSucceeded"
```

## Known Prototype Limitations

- **Stale `DryRunSucceeded` reason**: After the dry-run label is removed and the normal pipeline completes, the `WorksSucceeded` condition transitions back to `Reason: WorksSucceeded` only if the condition was not already `Status: True`. If a dry-run completed before label removal, the `Reason` stays `DryRunSucceeded` until the Work is deleted and re-created by the normal pipeline — which happens, just not atomically. In practice this resolves itself but there is a brief window of inconsistency.

- **No output format defined**: Dry-run output is written to the state store in the same format as normal output. A future iteration could write a diff or summary instead.

- **No manual trigger**: Dry-run is triggered solely by the label on the ResourceRequest. A future design might support a kubectl plugin or API endpoint.

- **Imperative actions**: Pipeline containers that make API calls or have other side effects will still execute in dry-run mode. It is the Promise writer's responsibility to gate those calls on `KRATIX_DRY_RUN`.
