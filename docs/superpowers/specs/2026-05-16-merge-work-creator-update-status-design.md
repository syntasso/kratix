# Merge `work-creator` and `update-status` into a single container

**Date:** 2026-05-16
**Status:** Approved, ready for implementation plan
**Issue:** Kratix maintainers — pipeline adapter has three commands (`reader`, `work-creator`, `update-status`); the latter two run in separate containers with no architectural reason for the split.

## Problem

Every Configure-workflow pipeline Job currently runs two pipeline-adapter containers back-to-back:

- `work-writer` — an **init container** running `pipeline-adapter work-creator`. Reads pipeline output from `/kratix/output`, builds a `Work` CR, creates or updates it.
- `status-writer` — the **main container** running `pipeline-adapter update-status`. Reads `status.yaml`, patches the resource's status subresource, handles workflow-control suspension/retry.

Both run after the user's pipeline containers, both use the same binary, both touch the same metadata volume. The split is historical, not architectural. It costs every Job an extra container worth of pod resources and startup overhead.

## Goal

Eliminate the separate `status-writer` main container. Run work-creation followed by status-update inside a single main container, after the user's pipeline init containers finish.

Side benefit: one fewer container in every pipeline Job pod.

## Non-goals

- Renaming the `pipeline-adapter` binary.
- Touching the `reader` init container — its lifecycle (runs *before* the user pipeline) is legitimately distinct.
- Changing the Delete-workflow path.
- Deleting the standalone `work-creator` and `update-status` subcommands in this PR. They become thin wrappers and can be removed in a follow-up after the merged version has soaked.

## Approach: shared library, three thin entry points

The pipeline-adapter binary will expose three subcommands that all delegate to the same library functions:

```
pipeline-adapter reader         → lib.Reader.Run            (unchanged)
pipeline-adapter work-creator   → lib.WorkCreator.Execute   (thin wrapper, kept for backward compat)
pipeline-adapter update-status  → lib.UpdateStatus          (thin wrapper, kept for backward compat)
pipeline-adapter run            → lib.WorkCreator.Execute + lib.UpdateStatus  (NEW; used by the factory)
```

The pipeline factory in production uses only the new `run` command. The old `work-creator` and `update-status` commands stay shippable in the binary so:

- Operators can still `kubectl exec` into a pod and invoke them manually for debugging.
- Rollback is a one-file revert of the factory change.
- No drift risk: all three commands route to the same `lib/` functions.

### Why not just chain inside `work-creator`?

A `work-creator --also-update-status` flag (or implicit env-var-driven behaviour) was considered and rejected. It makes the command's name misleading and hides important behaviour in flags. A new, explicit `run` command is clearer.

### Why not rip out the old commands now?

We could, but keeping them as 5-line wrappers around the library has near-zero cost and preserves rollback ergonomics. A follow-up PR can delete them once the new path has soaked in `main` for a release.

## Container layout

### Before (Configure workflow)

```
initContainers: [reader, ...userPipelineContainers, work-writer]
containers:     [status-writer]
```

### After

```
initContainers: [reader, ...userPipelineContainers]
containers:     [work-writer]   # now runs the merged `run` command
```

The remaining main container keeps the name `work-writer` — it minimises churn in logs and dashboards operators already watch. The Delete-workflow layout is unchanged.

## Detailed changes

### 1. Library refactor (`work-creator/lib/`)

Today, `work-creator/cmd/update_status.go` defines an unexported `updateStatus` function. Move it into the `lib` package as an exported `UpdateStatus(ctx, baseDir, params, objectClient) error`. Its three helpers — `handleWorkflowControlFile`, `addWorkflowSuspendLabel`, `readStatusFile` — move into `lib` alongside it and stay unexported (they're only called by `UpdateStatus`).

`lib.WorkCreator.Execute` already exists and stays as-is.

After this refactor:

- All status-update logic lives in `lib.UpdateStatus`.
- All work-creation logic lives in `lib.WorkCreator.Execute`.
- The `cmd/` files become thin wrappers that parse flags / env vars and call the lib functions.

### 2. New `run` subcommand (`work-creator/cmd/run.go`)

```go
func runCmd() *cobra.Command {
    // Same flags as workCreatorCmd: --input-directory, --promise-name,
    // --pipeline-name, --namespace, --resource-name, --resource-namespace,
    // --workflow-type.
    // RunE:
    //   1. Build k8s client + dynamic client.
    //   2. Call lib.WorkCreator.Execute(...).
    //      On error: return immediately. Status update is skipped.
    //   3. Call lib.UpdateStatus(ctx, "/work-creator-files/metadata",
    //                             helpers.GetParametersFromEnv(),
    //                             dynamicClient.Resource(...).Namespace(...))
    //   4. Return any error.
}
```

Registered in `cmd/root.go` alongside the existing three.

### 3. Refactor existing subcommands

- `cmd/work_creator.go` — strip the duplicated client setup into a tiny helper if it isn't already; call `lib.WorkCreator.Execute`. No behaviour change.
- `cmd/update_status.go` — replace `runUpdateStatus` body with a call to `lib.UpdateStatus`. No behaviour change.

### 4. Pipeline factory (`api/v1alpha1/pipeline_factory.go`)

- Delete `workCreatorContainer()` and `statusWriterContainer()`.
- Add `postPipelineContainer(env []corev1.EnvVar) corev1.Container` that:
  - Name: `work-writer` (kept).
  - Image: `os.Getenv("PIPELINE_ADAPTER_IMG")`.
  - Command: `["/bin/pipeline-adapter"]`.
  - Args: `["run", "--input-directory", "/work-creator-files", "--promise-name", ..., "--pipeline-name", ..., "--namespace", ..., "--workflow-type", ...]`, plus optional `--resource-name` / `--resource-namespace` like today.
  - VolumeMounts: union of the two old containers' mounts:
    - `shared-output` → `/work-creator-files/input`
    - `shared-metadata` → `/work-creator-files/metadata`
    - `promise-scheduling` → `/work-creator-files/kratix-system`
  - Env: union of the two old containers' env — the OpenTelemetry trace annotations the work-writer needs, the `defaultEnvVars()` from the factory, and the `env` arg (which carries `IS_LAST_PIPELINE`).
  - SecurityContext / ImagePullPolicy / Resources: same as today.
- In `pipelineJob()`, replace the `WorkflowActionConfigure` branch:
  ```go
  case WorkflowActionConfigure:
      initContainers = append(initContainers, pipelineContainers...)
      containers = []corev1.Container{p.postPipelineContainer(env)}
  ```
  (No more `workCreatorContainer` appended to init containers.)

### 5. Tests (`api/v1alpha1/pipeline_types_test.go`)

Existing assertions to update:

- Tests that expect `work-writer` in `InitContainers` need to move it to `Containers[0]`.
- Tests that expect `status-writer` as `Containers[0]` need to either be deleted or merged with the `work-writer` assertions (e.g. the ephemeral-storage tests).
- Tests asserting on the `work-writer` args need to expect `run` as the subcommand instead of `work-creator`.
- Tests that assert on the volume mounts of either container need to assert the merged set on the new `work-writer`.

No new test cases required — the unit-test surface stays the same shape, the assertions just shift.

### 6. Docs (`work-creator/README.md`)

Add a usage example for the new `run` subcommand. Leave the existing `work-creator` and `update-status` examples in place — they still work.

## Failure semantics

| Scenario | Today | After |
|---|---|---|
| User pipeline container fails | Job fails. Work + status untouched. | Same. |
| Work-creation fails | `work-writer` init container fails → status-writer never runs. Job fails. | `work-writer` main container fails inside step 1, returns before step 2. Job fails. |
| Status-update fails | `status-writer` main container fails. Job retries main container per `RestartPolicyOnFailure`. | `work-writer` main container fails on step 2. Job retries the whole merged step. |

The third row is the only material change: a status-update failure now retries work-creation too. This is safe because work-creation is idempotent (it uses get-or-create-or-update by deterministic name + labels), and the retry cost is negligible.

## Risks

- **Init-container ordering invariants.** None identified — controllers don't read individual container statuses; they only observe Job-level conditions. (Verified by grep.)
- **External tooling that invokes `pipeline-adapter work-creator` or `update-status` directly.** We keep both subcommands, so any such tooling continues to work. The README still documents them.
- **Operator muscle memory** (looking at `status-writer` logs). Mitigated by keeping the container name `work-writer` and noting the consolidation in the PR description / changelog.
- **Telemetry / trace continuity.** Both old containers consume the same `TRACEPARENT` / `TRACESTATE` env vars from pod annotations. The merged container receives the same env, so spans should still chain correctly. Worth eyeballing trace output in a manual smoke test before merging.

## Rollout

Single PR. No feature flag — the factory either emits the new layout or the old one, and a revert is a one-file change. The library refactor is independently safe (no behaviour change) and could be split into a preceding PR if reviewers prefer.

## Follow-up (not in this PR)

- Once the merged path has soaked in `main` for a release, delete the `work-creator` and `update-status` subcommands and their `cmd/` files. Update the README accordingly.
