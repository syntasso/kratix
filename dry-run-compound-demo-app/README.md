# Compound Promise dry-run demo

Run everything from this directory. All commands target the `platform` kind cluster
and the `default` namespace.

## What you should end up seeing

A single summary containing the compound Promise's diff **and** its components':

```
## Compound request: `app` / `demo-app`
### Pipeline: `app-configure`
    app-configmap.yaml, app-deployment.yaml,
    postgres-request.yaml, ingressroute-request.yaml
---
## Component request: `postgres` / `demo-app-db`
### Pipeline: `postgres-configure`
    postgres-statefulset.yaml, postgres-service.yaml
---
## Component request: `ingress` / `demo-app-ingress`
### Pipeline: `ingress-configure`
    ingress.yaml
```

The component sections are the feature. Without them you would only see that the
component *requests* changed, not that postgres would restate its StatefulSet with
new storage.

## Prerequisites

**Kratix built from this branch**, with the compound changes. If you haven't already:

```bash
cd ../ && make build-and-load-kratix && cd -
kubectl rollout status deployment -n kratix-platform-system kratix-platform-controller-manager
```

**Pipeline image.** All three Promises use `syntasso/test-bundle-image:v0.1.0` (alpine +
yq + kubectl). If it isn't in the cluster:

```bash
cd ../test/core/assets/workflows/ && docker build -t syntasso/test-bundle-image:v0.1.0 . && cd -
kind load docker-image syntasso/test-bundle-image:v0.1.0 --name platform
```

**Gitea state store.** `kubectl get gitstatestore default` should be Ready.

## Step 1 — dry-run Destination

```bash
kubectl apply -f 00-dry-run-destination.yaml
kubectl get destinations
```

Wait for `dry-run` to be `READY=True`. This Destination is mandatory: without it a
dry-run Work is never placed and the DryRun hangs with an empty status instead of
failing.

## Step 2 — install the three Promises

```bash
kubectl apply -f 01-postgres-promise.yaml
kubectl apply -f 02-ingress-promise.yaml
kubectl apply -f 03-app-promise.yaml

kubectl get promises -w
```

Wait for all three to reach `Available`. If `app` sticks at `Unavailable`, another
Promise is probably already using the name — see Cleanup below.

Install all three before previewing anything. A component request whose Promise isn't
installed is skipped with a log line, and appears only as a file in the compound diff.

## Step 3 — confirm nothing exists yet

```bash
kubectl get apps,postgres,ingressroutes
# No resources found
```

## Step 4 — Scenario A: preview a CREATE

```bash
kubectl apply -f 04-dry-run-create.yaml
kubectl get dryruns -w
```

Go to Gitea

## Step 5 — create it for real

```bash
kubectl delete -f 04-dry-run-create.yaml
kubectl apply -f 05-app-request.yaml
kubectl get apps -w
```

Delete the DryRun first — a `DryRun` is single-shot, so the reconciler ignores it once
`Completed` is set.

Wait until `demo-app` reports `Reconciled`.

```bash
kubectl apply -f 06-component-requests.yaml
kubectl get postgres,ingressroutes -w
```

Wait for both to be `Reconciled`.

## Step 6

```bash
kubectl apply -f 07-dry-run-update.yaml
kubectl get dryruns -w
```

Read the summary the same way as step 4.

## Step 7 — Scenario C: a component that never completes

Independent of steps 3–6. Tests what a **partial** summary looks like when one
component works and another never finishes.

```bash
kubectl apply -f 09-failing-component-promise.yaml
kubectl apply -f 10-failing-compound-promise.yaml
kubectl get promises -w        # wait for broken + flaky-app -> Available

kubectl apply -f 11-dry-run-failing.yaml
kubectl get dryruns -w
```

`flaky-app` emits three documents: its own ConfigMap, a `postgres` component request
that succeeds, and a `broken` component request whose pipeline exits 1 on purpose.

**Expected timeline.** This one is slow by design — be patient rather than assuming
it's wedged:

| When | What |
|---|---|
| ~30s | both component DryRuns appear; postgres reports `Completed=True` |
| ~30s+ | the `broken` pipeline pod enters `CrashLoopBackOff` and stays there |
| 10 min | the compound gives up waiting and writes a **partial** summary |

A pipeline that exits non-zero does **not** produce a clean failure. It crash-loops, so
`WorksSucceeded` is never set at all — and since the reconciler only fails on
`WorksSucceeded=False`, an absent condition means it waits instead. Hence the timeout
rather than an immediate error.

Watch it happen:

```bash
kubectl get pods | grep broken
kubectl logs -l kratix.io/promise-name=broken --tail=5 --all-containers
kubectl get dryruns -o custom-columns=\
'NAME:.metadata.name,COMPLETED:.status.conditions[0].status,REASON:.status.conditions[0].reason'
```

**What to check in the summary:**

- the compound's own diff renders normally — all three files, added
- the postgres component section renders a normal diff
- the broken component section *says* it did not complete, rather than being silently
  absent

**And what to check afterwards** — the more interesting part:

```bash
kubectl get dryrun -l kratix.io/dry-run-parent=failing-preview \
  -o custom-columns='NAME:.metadata.name,STATUS:.status.conditions'
kubectl get pods | grep broken
```

The `broken` component DryRun should still have an **empty status**, still requeueing
every 10s, with its pod still crash-looping — while the compound reports
`Completed=True`. Nothing cleans either up until you delete `11-dry-run-failing.yaml`.
The compound-level timeout protects the compound, not the component. That is the
underlying bug, unfixed, now inherited by every component.

Cleanup for this scenario:

```bash
kubectl delete -f 11-dry-run-failing.yaml
kubectl delete -f 10-failing-compound-promise.yaml -f 09-failing-component-promise.yaml
```

## Gotchas worth knowing

- **A `DryRun` runs once.** `Completed` is terminal — the reconciler returns
  immediately if the condition exists, whatever its status. Delete and re-apply to
  re-run.
- **Namespace must match.** A DryRun must live in the same namespace as the request it
  references. Resource Works are created in the request's own namespace and the
  reconciler lists both dry-run and live Works there. Get this wrong and you get an
  empty summary that still reports `Completed=True`.
- **`spec.resource` is the spec body only** — no `apiVersion`/`kind`/`metadata`.
- **Components whose Promise isn't installed are skipped**, with a log line. There is
  no pipeline to preview, so the request appears only as a file in the compound diff.
- **A crash-looping component pipeline stalls the whole preview** for 10 minutes, then
  the compound run writes a partial summary marking that component as incomplete. If a
  preview seems stuck, check for failing pipeline pods:
  `kubectl get pods | grep kratix-`.
- **Output nests per request.** The dry-run Destination uses `nestedByMetadata`, so
  concurrent previews cannot collide. A flat Destination (`filepath.mode: none`) is
  easier to browse but every preview writes into one directory, so two previews of
  different requests of the same Promise overwrite each other's files — including the
  summary — with no warning.
- **Nothing cleans up previewed output.** The Destination is `cleanup: none`, so files
  from deleted DryRuns stay in the repo indefinitely. Left alone that grows without
  bound; a real deployment would want the DryRun's deletion to remove its output.
- **The diff is Works vs Works** — what Kratix *would* write against what it *last*
  wrote, not against what is currently in the state store. Hand-edits to the repo are
  invisible to a dry run.
