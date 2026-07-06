# Kratix Tilt Dev Environment — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give kratix a one-button local dev environment (`make dev` → Tilt) that hot-reloads controller code in ~1s and stands up a full platform (cluster, cert-manager, a GitOps reconciler, a state store, kratix, a registered destination), plus a containerized VS Code dev environment — all additive to the existing `quick-start.sh`.

**Architecture:** Three layers with clean seams — (1) a `ctlptl` cluster wired to a local `localhost:5000` registry (kills `kind load`); (2) a build layer that host-compiles the two kratix binaries and uses Tilt `live_update` to sync + restart the `manager` in-pod; (3) a single `Tiltfile` orchestration layer that reuses the existing `scripts/` and `hack/` manifests, selecting a GitOps reconciler + state store from a config matrix. A `.devcontainer/` packages the CLIs.

**Tech Stack:** Tilt (Starlark), ctlptl, kind, ko (Phase 2 only), Go 1.26, kubectl, Docker, cert-manager, Argo CD / Flux, Gitea / MinIO.

## Global Constraints

- Go toolchain: **1.26** (matches `Dockerfile`); build with `CGO_ENABLED=0`.
- kind node image: **`kindest/node:v1.33.1`** (matches `quick-start.sh` `KIND_IMAGE`).
- Controller image ref (unchanged): **`docker.io/syntasso/kratix-platform:dev`** — serves double duty as controller AND pipeline-adapter image.
- Platform context: **`kind-platform`**; kind cluster name **`platform`**.
- Controller: Deployment **`kratix-platform-controller-manager`**, namespace **`kratix-platform-system`**, container **`manager`**, entrypoint **`/manager`**.
- Pipeline-adapter image is injected via ConfigMap **`kratix-platform-pipeline-adapter-config`** key **`PIPELINE_ADAPTER_IMG`** — the dev path must convert this to a literal container env so Tilt's `match_in_env_vars` rewrites it to the local-registry ref.
- **Additive only:** do NOT modify `scripts/quick-start.sh` or the Flux code path in `scripts/install-gitops`. New assets live under `hack/dev/`, a root `Tiltfile`, `.devcontainer/`, and new `make dev*` targets.
- GitOps matrix cells (single-cluster, platform doubles as destination): `argo-git` (default), `flux-bucket`, `flux-git`. `argo-bucket` is unsupported (upstream Argo has no bucket source).
- Default topology: **single cluster** (`--single-cluster` equivalent).
- Repo uses **jj** (colocated). Commit steps use `jj commit -m "…"`. Work lives on bookmark `kratix-tilt-dev-env`.

---

## File Structure

- `hack/dev/install-tools.sh` — installs pinned CLIs (tilt, ko, ctlptl, kind, kubectl, helm, yq) via Homebrew. Sole responsibility: host tooling.
- `hack/dev/cluster.yaml` — ctlptl Registry + Cluster spec (single kind cluster + `localhost:5000` registry + port mappings). Sole responsibility: cluster/registry declaration.
- `hack/dev/Dockerfile.dev` — thin runtime image copying prebuilt binaries. Sole responsibility: dev image.
- `hack/dev/setup-statestore.sh` — installs Gitea or MinIO + credentials and waits. Reuses `scripts/utils.sh`. Sole responsibility: state-store backend bootstrap.
- `Tiltfile` — orchestration: config matrix, build+live_update, resource graph. Sole responsibility: dev-loop orchestration.
- `Makefile` — new targets `dev`, `dev-cluster`, `dev-down`, `dev-tools`. Modify only (append).
- `.devcontainer/devcontainer.json`, `.devcontainer/Dockerfile` — containerized VS Code env.
- `docs/dev-environment.md` — how to use it.

---

## Task 1: Dev tooling installer

**Files:**
- Create: `hack/dev/install-tools.sh`
- Modify: `Makefile` (append `dev-tools` target)

**Interfaces:**
- Produces: `make dev-tools` installs `tilt`, `ko`, `ctlptl`, `kind`, `kubectl`, `helm`, `yq` onto PATH.

- [ ] **Step 1: Write the verification (the check that must pass at the end)**

Create `hack/dev/verify-tools.sh` inline check — but instead we assert directly. Run this now to confirm the tools are absent (baseline):

Run: `for t in tilt ko ctlptl; do command -v $t || echo "MISSING: $t"; done`
Expected: `MISSING: tilt`, `MISSING: ko`, `MISSING: ctlptl` (baseline — none installed).

- [ ] **Step 2: Write `hack/dev/install-tools.sh`**

```bash
#!/usr/bin/env bash
# Installs the CLIs needed for the Tilt-based local dev environment.
# Host use only; the devcontainer bakes these in via .devcontainer/Dockerfile.
set -euo pipefail

if ! command -v brew >/dev/null; then
  echo "Homebrew required: https://brew.sh" >&2
  exit 1
fi

# tilt-dev tap provides tilt + ctlptl
brew tap tilt-dev/tap 2>/dev/null || true

pkgs=(tilt-dev/tap/tilt tilt-dev/tap/ctlptl ko kind kubectl helm yq)
for p in "${pkgs[@]}"; do
  name="${p##*/}"
  if command -v "$name" >/dev/null; then
    echo "✓ $name already installed"
  else
    echo "Installing $name..."
    brew install "$p"
  fi
done

echo "All dev tools installed:"
tilt version && ctlptl version && ko version && kind version && kubectl version --client && helm version --short && yq --version
```

- [ ] **Step 3: Make it executable and append the Makefile target**

```bash
chmod +x hack/dev/install-tools.sh
```

Append to `Makefile` (in the dev/helper section, near `quick-start`):

```makefile
dev-tools: ## Install the CLIs needed for the Tilt-based dev environment (tilt, ko, ctlptl, ...)
	./hack/dev/install-tools.sh
```

- [ ] **Step 4: Run it and verify tools are present**

Run: `make dev-tools`
Expected: ends printing versions for tilt, ctlptl, ko, kind, kubectl, helm, yq (all resolve).

Then: `for t in tilt ko ctlptl kind; do command -v $t; done`
Expected: a path printed for each (no MISSING).

- [ ] **Step 5: Record tools in the user's dotfiles Brewfile**

Per repo convention, add `tilt`, `ctlptl`, `ko` (and tap `tilt-dev/tap`) to `~/.dotfiles` Brewfile so they persist across machines. (Manual/one-line; not part of the repo commit.)

- [ ] **Step 6: Commit**

```bash
jj commit -m "feat(dev): add dev-tools installer for tilt/ko/ctlptl"
```

---

## Task 2: ctlptl cluster + local registry

**Files:**
- Create: `hack/dev/cluster.yaml`
- Modify: `Makefile` (append `dev-cluster` target)

**Interfaces:**
- Produces: a running kind cluster named `platform` (context `kind-platform`) attached to a registry at `localhost:5000`, with the same port mappings `quick-start.sh` uses (31337/31340/31333).

- [ ] **Step 1: Baseline check (cluster absent)**

Run: `kind get clusters | grep -x platform || echo "NO PLATFORM CLUSTER"`
Expected: `NO PLATFORM CLUSTER` (or delete any stale one: `kind delete cluster --name platform`).

- [ ] **Step 2: Write `hack/dev/cluster.yaml`**

```yaml
apiVersion: ctlptl.dev/v1alpha1
kind: Registry
metadata:
  name: kratix-registry
spec:
  port: 5000
---
apiVersion: ctlptl.dev/v1alpha1
kind: Cluster
metadata:
  name: kind-platform
product: kind
registry: kratix-registry
kindV1Alpha4Cluster:
  kind: Cluster
  apiVersion: kind.x-k8s.io/v1alpha4
  nodes:
    - role: control-plane
      image: kindest/node:v1.33.1
      extraPortMappings:
        - containerPort: 31337
          hostPort: 31337
        - containerPort: 31340
          hostPort: 31340
        - containerPort: 31333
          hostPort: 31333
```

- [ ] **Step 3: Append the Makefile target**

```makefile
dev-cluster: ## Create/ensure the local kind cluster + registry (idempotent)
	ctlptl apply -f hack/dev/cluster.yaml
```

- [ ] **Step 4: Apply and verify cluster + registry**

Run: `make dev-cluster`
Expected: ctlptl reports the registry and cluster created.

Run: `kubectl --context kind-platform get nodes`
Expected: one `platform-control-plane` node, `Ready`.

Run: `ctlptl get cluster kind-platform -o template --template '{{.status.localRegistryHosting.host}}'`
Expected: `localhost:5000` (registry wired to the cluster).

- [ ] **Step 5: Verify the registry accepts a push from the cluster's perspective**

Run: `docker pull busybox:1.36 && docker tag busybox:1.36 localhost:5000/busybox:dev && docker push localhost:5000/busybox:dev`
Expected: push succeeds (registry reachable on the host).

- [ ] **Step 6: Commit**

```bash
jj commit -m "feat(dev): ctlptl cluster + local registry spec"
```

---

## Task 3: Dev image (prebuilt binaries)

**Files:**
- Create: `hack/dev/Dockerfile.dev`
- Modify: `.gitignore` (add `/bin/manager`, `/bin/pipeline-adapter` if not already ignored)

**Interfaces:**
- Consumes: host-built `bin/manager` and `bin/pipeline-adapter`.
- Produces: an image built FROM `alpine/git` with `/manager` entrypoint and `/bin/pipeline-adapter`, suitable for `live_update` binary sync.

- [ ] **Step 1: Build the binaries on the host (verify the build commands work)**

Run: `CGO_ENABLED=0 go build -o bin/manager cmd/main.go && CGO_ENABLED=0 go build -o bin/pipeline-adapter work-creator/*.go`
Expected: exit 0; `ls -l bin/manager bin/pipeline-adapter` shows two executables.

- [ ] **Step 2: Write `hack/dev/Dockerfile.dev`**

Mirrors the runtime stage of the root `Dockerfile` but copies prebuilt binaries (no in-image `go build`):

```dockerfile
# Dev-only image: copies host-built binaries for fast Tilt live_update.
# Runtime stage mirrors the root Dockerfile (base alpine/git provides /bin/sh
# for Tilt restart_process and /usr/bin/git for the Kratix git writer).
FROM alpine/git
WORKDIR /

COPY bin/manager /manager
COPY bin/pipeline-adapter /bin/pipeline-adapter

RUN addgroup -g 65532 app && \
    adduser -D -H -u 65532 -G app appuser && \
    mkdir -p /home/appuser/.ssh && \
    chown -R 65532:65532 /home/appuser

ENV HOME=/home/appuser
USER 65532:65532

ENTRYPOINT ["/manager"]
```

- [ ] **Step 3: Ensure binaries are git-ignored**

Confirm `bin/` artifacts are ignored (append to `.gitignore` if absent):

```
/bin/manager
/bin/pipeline-adapter
```

Run: `git check-ignore bin/manager bin/pipeline-adapter`
Expected: both paths echoed (ignored).

- [ ] **Step 4: Build the image against the local registry and verify it runs**

Run: `docker build -f hack/dev/Dockerfile.dev -t localhost:5000/syntasso/kratix-platform:dev .`
Expected: build succeeds.

Run: `docker run --rm --entrypoint /manager localhost:5000/syntasso/kratix-platform:dev --help 2>&1 | head -1 || true`
Expected: the manager binary executes (prints usage/flags or starts; any output proves the binary is present and runnable).

- [ ] **Step 5: Commit**

```bash
jj commit -m "feat(dev): thin dev Dockerfile copying prebuilt binaries"
```

---

## Task 4: Tiltfile — build + live_update + kratix controller

**Files:**
- Create: `Tiltfile`

**Interfaces:**
- Consumes: `hack/dev/cluster.yaml` cluster (Task 2), `hack/dev/Dockerfile.dev` (Task 3), `distribution/kratix.yaml` (generated by `make distribution`).
- Produces: `tilt up` builds the image into the local registry, applies cert-manager and the kratix controller (with the `PIPELINE_ADAPTER_IMG` env rewritten), and hot-reloads `manager` on code change. Later tasks extend the same Tiltfile with the GitOps matrix.

- [ ] **Step 1: Ensure the distribution manifest exists (input for the Tiltfile)**

Run: `make distribution && test -f distribution/kratix.yaml && echo OK`
Expected: `OK`.

- [ ] **Step 2: Write the initial `Tiltfile` (build + cert-manager + controller)**

```python
# -*- mode: Python -*-
# Kratix local dev environment.
# Spec: docs/superpowers/specs/2026-07-06-kratix-tilt-dev-env-design.md
# Usage: `make dev` (default argo-git) or `make dev GITOPS=flux-bucket`.

load('ext://restart_process', 'docker_build_with_restart')

allow_k8s_contexts('kind-platform')

IMAGE = 'docker.io/syntasso/kratix-platform:dev'

# ---- Build layer: host go build + live_update ----
local_resource(
    'go-build',
    cmd='CGO_ENABLED=0 go build -o bin/manager cmd/main.go && ' +
        'CGO_ENABLED=0 go build -o bin/pipeline-adapter work-creator/*.go',
    deps=['cmd', 'work-creator', 'api', 'lib', 'internal', 'go.mod', 'go.sum'],
    labels=['build'],
)

docker_build_with_restart(
    IMAGE,
    '.',
    dockerfile='hack/dev/Dockerfile.dev',
    entrypoint=['/manager'],
    only=['bin/manager', 'bin/pipeline-adapter'],
    live_update=[sync('bin/manager', '/manager')],
    # Rewrite the image ref wherever it appears as a literal container env value
    # (PIPELINE_ADAPTER_IMG) so pipeline pods pull from the local registry too.
    match_in_env_vars=True,
)

# ---- cert-manager (webhooks dependency) ----
local_resource(
    'cert-manager',
    cmd='kubectl --context kind-platform apply -f ' +
        'https://github.com/cert-manager/cert-manager/releases/download/v1.15.0/cert-manager.yaml && ' +
        'kubectl --context kind-platform wait --for=condition=available --timeout=180s ' +
        '-n cert-manager deployment/cert-manager deployment/cert-manager-cainjector deployment/cert-manager-webhook',
    labels=['platform'],
)

# ---- kratix controller ----
# Convert PIPELINE_ADAPTER_IMG from configMapKeyRef to a literal env value so
# Tilt's match_in_env_vars rewrites it to the local-registry ref.
objects = read_yaml_stream('distribution/kratix.yaml')
for o in objects:
    if o.get('kind') == 'Deployment' and \
       o['metadata']['name'] == 'kratix-platform-controller-manager':
        for c in o['spec']['template']['spec']['containers']:
            if c['name'] == 'manager':
                newenv = []
                for e in c.get('env', []):
                    if e['name'] == 'PIPELINE_ADAPTER_IMG':
                        newenv.append({'name': 'PIPELINE_ADAPTER_IMG', 'value': IMAGE})
                    else:
                        newenv.append(e)
                c['env'] = newenv

k8s_yaml(encode_yaml_stream(objects))

k8s_resource(
    'kratix-platform-controller-manager',
    resource_deps=['go-build', 'cert-manager'],
    labels=['platform'],
)
```

- [ ] **Step 3: Bring it up and verify the controller becomes Available**

Run: `make dev-cluster && tilt up --context kind-platform &` then wait, or run `tilt ci` for a headless one-shot:
`tilt ci --context kind-platform` (builds, applies, waits for readiness, exits 0 on success).
Expected: exits 0; Tilt log shows the image pushed to `localhost:5000`.

Run: `kubectl --context kind-platform -n kratix-platform-system get deploy kratix-platform-controller-manager -o jsonpath='{.status.conditions[?(@.type=="Available")].status}'`
Expected: `True`.

- [ ] **Step 4: Verify the pipeline-adapter env was rewritten to the local registry**

Run: `kubectl --context kind-platform -n kratix-platform-system get deploy kratix-platform-controller-manager -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].env[?(@.name=="PIPELINE_ADAPTER_IMG")].value}'`
Expected: a `localhost:5000/...kratix-platform...` ref (NOT `docker.io/...`), proving pipeline pods will pull the local build.

- [ ] **Step 5: Verify live_update hot-reload (~1s, no image rebuild)**

With `tilt up` running: touch a controller source file (e.g. add a log line in `cmd/main.go`), save.
Run (watch Tilt UI or): `tilt logs kratix-platform-controller-manager --context kind-platform`
Expected: Tilt shows a `live_update` "Copying files… Restarting process" cycle (seconds), NOT a "Building Dockerfile" line; the pod is not recreated (`kubectl get pod` AGE unchanged, RESTARTS via process restart).

- [ ] **Step 6: Commit**

```bash
jj commit -m "feat(dev): Tiltfile with live_update build + kratix controller"
```

---

## Task 5: Tiltfile — GitOps matrix (state store + reconciler + destination)

**Files:**
- Create: `hack/dev/setup-statestore.sh`
- Modify: `Tiltfile` (append the matrix + statestore + destination resources)

**Interfaces:**
- Consumes: `--gitops` Tilt arg (`argo-git|flux-bucket|flux-git`), existing `scripts/install-gitops`, `scripts/register-destination`, `scripts/utils.sh`, `hack/platform/gitea-install.yaml`, `hack/platform/minio-install.yaml`, `config/samples/platform_v1alpha1_{git,bucket}statestore.yaml`.
- Produces: a fully reconciled single-cluster platform for the selected matrix cell, with the platform registered as a destination.

- [ ] **Step 1: Write `hack/dev/setup-statestore.sh`**

Reuses `scripts/utils.sh` helpers (Gitea cert/creds) rather than reimplementing them:

```bash
#!/usr/bin/env bash
# Installs the state-store backend for the dev env.
# Usage: setup-statestore.sh git|bucket
set -euo pipefail
ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/../.." &> /dev/null && pwd )
source "${ROOT}/scripts/utils.sh"

STORE="${1:?usage: setup-statestore.sh git|bucket}"
CTX="kind-platform"

if [ "$STORE" = "git" ]; then
    make -C "$ROOT" gitea-cli
    generate_gitea_credentials "$CTX"
    kubectl --context "$CTX" apply -f "${ROOT}/hack/platform/gitea-install.yaml"
    kubectl --context "$CTX" wait pod -n gitea --selector app=gitea --for=condition=ready --timeout=180s
    kubectl --context "$CTX" wait job -n gitea gitea-create-repository --for=condition=Complete --timeout=180s
else
    make -C "$ROOT" minio-cli
    kubectl --context "$CTX" apply -f "${ROOT}/hack/platform/minio-install.yaml"
    kubectl --context "$CTX" wait pod -n kratix-platform-system --selector run=minio --for=condition=ready --timeout=180s
    kubectl --context "$CTX" wait job -n default minio-create-bucket --for=condition=Complete --timeout=180s
fi
```

Run: `chmod +x hack/dev/setup-statestore.sh`

- [ ] **Step 2: Append the matrix + resources to the `Tiltfile`**

Insert near the top (after `allow_k8s_contexts`), the config parsing:

```python
# ---- GitOps matrix ----
config.define_string('gitops')
_cfg = config.parse()
_gitops = _cfg.get('gitops', 'argo-git')
_matrix = {
    'argo-git':    ('argo', 'git'),
    'flux-bucket': ('flux', 'bucket'),
    'flux-git':    ('flux', 'git'),
}
if _gitops not in _matrix:
    fail('unknown gitops=%s (argo-git|flux-bucket|flux-git); argo-bucket is unsupported upstream' % _gitops)
PROVIDER, STORE = _matrix[_gitops]
```

Append at the end (after the `k8s_resource('kratix-platform-controller-manager', ...)`):

```python
# ---- state-store backend (Gitea or MinIO) ----
local_resource(
    'statestore-backend',
    cmd='./hack/dev/setup-statestore.sh %s' % STORE,
    labels=['gitops'],
)

# ---- StateStore CR (patched for kind networking on git) ----
if STORE == 'git':
    _store_cmd = ("sed \"s/172.18.0.2/$(docker inspect platform-control-plane " +
                  "| yq '.[0].NetworkSettings.Networks.kind.IPAddress')/g\" " +
                  "config/samples/platform_v1alpha1_gitstatestore.yaml " +
                  "| kubectl --context kind-platform apply -f - && " +
                  "kubectl --context kind-platform wait gitstatestore default --for=condition=Ready --timeout=180s")
else:
    _store_cmd = ("kubectl --context kind-platform apply -f config/samples/platform_v1alpha1_bucketstatestore.yaml && " +
                  "kubectl --context kind-platform wait bucketstatestore default --for=condition=Ready --timeout=180s")

local_resource(
    'statestore-cr',
    cmd=_store_cmd,
    resource_deps=['kratix-platform-controller-manager', 'statestore-backend'],
    labels=['gitops'],
)

# ---- register the platform as a destination (installs the reconciler) ----
# register-destination -> install-gitops honours GITOPS_PROVIDER; --git selects git store.
_reg_flags = '--git' if STORE == 'git' else ''
local_resource(
    'register-destination',
    cmd='GITOPS_PROVIDER=%s ./scripts/register-destination ' % PROVIDER +
        '--name platform-cluster --context kind-platform ' +
        '--platform-context kind-platform %s && ' % _reg_flags +
        'kubectl --context kind-platform wait destination platform-cluster --for=condition=Ready --timeout=300s',
    resource_deps=['statestore-cr'],
    labels=['gitops'],
)
```

- [ ] **Step 3: Verify the default cell (`argo-git`) reaches READY**

Run: `tilt ci -- --gitops=argo-git` (headless; builds + applies + waits for every resource).
Expected: exits 0.

Run: `kubectl --context kind-platform wait destination platform-cluster --for=condition=Ready --timeout=10s`
Expected: `condition met`.

Run: `kubectl --context kind-platform -n argocd get deploy`
Expected: argocd deployments present (Argo installed on the platform).

- [ ] **Step 4: Verify a sample Promise reconciles end-to-end (smoke test)**

Run: `kubectl --context kind-platform apply -f config/samples/promise.yaml 2>/dev/null || kubectl --context kind-platform apply -f samples/jenkins/promise.yaml`
(Use any Promise sample present in the repo; confirm the path first with `ls config/samples samples 2>/dev/null`.)
Expected: the Promise's CRD appears: `kubectl --context kind-platform get crd | grep -i <promise-kind>` returns a row within ~60s, proving the controller + work pipeline + reconciler flow works.

- [ ] **Step 5: Verify the bucket cell (`flux-bucket`) reaches READY**

Run: `tilt down -- --gitops=argo-git || true` then `tilt ci -- --gitops=flux-bucket`
Expected: exits 0.

Run: `kubectl --context kind-platform get bucketstatestore default -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'`
Expected: `True` (proves BucketStateStore coverage — the CI-migration cell — works with Flux).

- [ ] **Step 6: Commit**

```bash
jj commit -m "feat(dev): Tiltfile gitops matrix (argo-git/flux-bucket/flux-git)"
```

---

## Task 6: Makefile one-button UX + docs

**Files:**
- Modify: `Makefile` (append `dev`, `dev-down`; `GITOPS` var)
- Create: `docs/dev-environment.md`

**Interfaces:**
- Consumes: Tasks 2–5.
- Produces: `make dev` (the button) and `make dev-down` (teardown).

- [ ] **Step 1: Append the Makefile targets**

```makefile
GITOPS ?= argo-git

dev: dev-cluster ## One-button local kratix dev env via Tilt. Override cell: make dev GITOPS=flux-bucket
	tilt up -- --gitops=$(GITOPS)

dev-down: ## Tear down the Tilt dev env and the local cluster+registry
	tilt down -- --gitops=$(GITOPS) || true
	ctlptl delete -f hack/dev/cluster.yaml || true
```

- [ ] **Step 2: Verify the button from cold**

Run: `make dev-down; make dev &` — wait for Tilt to report all resources green (or in another shell `tilt wait --for=condition=Ready uiresource --all --timeout=600s` if available; otherwise watch the UI at http://localhost:10350).
Expected: cluster created, all resources green, controller Available, destination Ready.

- [ ] **Step 3: Verify teardown is clean and idempotent**

Run: `make dev-down && make dev-down`
Expected: first run deletes cluster+registry; second run is a no-op (the `|| true` guards make it idempotent), no error exit.

- [ ] **Step 4: Write `docs/dev-environment.md`**

```markdown
# Local dev environment (Tilt)

One-button local Kratix for development. Hot-reloads controller code in ~1s.

## Prerequisites

    make dev-tools    # installs tilt, ctlptl, ko, kind, kubectl, helm, yq

Requires Docker running.

## Usage

    make dev                        # default: Argo CD + Gitea (git state store)
    make dev GITOPS=flux-bucket     # Flux + MinIO (bucket state store — CI parity)
    make dev GITOPS=flux-git        # Flux + Gitea

Open the Tilt dashboard at http://localhost:10350 to see every component's
health and logs. Edit controller code and save — the `manager` binary is rebuilt
on the host and synced into the running pod (no image rebuild, no `kind load`).

## Teardown

    make dev-down

## GitOps matrix

| Cell (`GITOPS=`) | Reconciler | State store | Notes |
|------------------|-----------|-------------|-------|
| `argo-git` (default) | Argo CD | Gitea (Git) | Local dev |
| `flux-bucket`    | Flux | MinIO (Bucket) | CI parity + bucket testing |
| `flux-git`       | Flux | Gitea (Git) | Flux + git |

`argo-bucket` is unsupported: Argo CD has no bucket source.

## Relationship to quick-start.sh

`scripts/quick-start.sh` is unchanged and still used by CI/enterprise. This Tilt
flow is the additive, faster dev-loop alternative.
```

- [ ] **Step 5: Commit**

```bash
jj commit -m "feat(dev): make dev/dev-down one-button targets + docs"
```

---

## Task 7: Containerized VS Code dev environment

**Files:**
- Create: `.devcontainer/devcontainer.json`
- Create: `.devcontainer/Dockerfile`

**Interfaces:**
- Consumes: everything above (the container just runs `make dev`).
- Produces: opening the repo in a devcontainer yields all CLIs and a working `make dev` via docker-outside-of-docker.

- [ ] **Step 1: Write `.devcontainer/Dockerfile`**

```dockerfile
# Dev container for Kratix: Go toolchain + the CLIs the Tilt dev env needs.
# Docker access is provided by the docker-outside-of-docker feature (host socket),
# which is the reliable pattern for kind + devcontainers/Codespaces.
FROM golang:1.26-bookworm

ARG KIND_VERSION=v0.29.0
ARG CTLPTL_VERSION=0.8.42
ARG TILT_VERSION=0.35.0
ARG KO_VERSION=0.18.0

RUN apt-get update && apt-get install -y --no-install-recommends \
      curl git ca-certificates bash jq && \
    rm -rf /var/lib/apt/lists/*

# kubectl
RUN curl -fsSLo /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/v1.33.1/bin/linux/amd64/kubectl" && \
    chmod +x /usr/local/bin/kubectl

# yq
RUN curl -fsSLo /usr/local/bin/yq \
      "https://github.com/mikefarah/yq/releases/latest/download/yq_linux_amd64" && \
    chmod +x /usr/local/bin/yq

# kind
RUN curl -fsSLo /usr/local/bin/kind \
      "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64" && \
    chmod +x /usr/local/bin/kind

# ctlptl
RUN curl -fsSL \
      "https://github.com/tilt-dev/ctlptl/releases/download/v${CTLPTL_VERSION}/ctlptl.${CTLPTL_VERSION}.linux.x86_64.tar.gz" \
      | tar -xzC /usr/local/bin ctlptl

# tilt
RUN curl -fsSL \
      "https://github.com/tilt-dev/tilt/releases/download/v${TILT_VERSION}/tilt.${TILT_VERSION}.linux.x86_64.tar.gz" \
      | tar -xzC /usr/local/bin tilt

# ko
RUN curl -fsSL \
      "https://github.com/ko-build/ko/releases/download/v${KO_VERSION}/ko_${KO_VERSION}_Linux_x86_64.tar.gz" \
      | tar -xzC /usr/local/bin ko

# helm
RUN curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
```

- [ ] **Step 2: Write `.devcontainer/devcontainer.json`**

```jsonc
{
  "name": "kratix-dev",
  "build": { "dockerfile": "Dockerfile" },
  "features": {
    "ghcr.io/devcontainers/features/docker-outside-of-docker:1": {}
  },
  // Tilt UI (10350) + the kind port mappings used by the platform cluster.
  "forwardPorts": [10350, 31337, 31340, 31333],
  "postCreateCommand": "go mod download",
  "customizations": {
    "vscode": {
      "extensions": [
        "golang.go",
        "tilt-dev.tiltfile",
        "ms-kubernetes-tools.vscode-kubernetes-tools"
      ]
    }
  }
}
```

- [ ] **Step 3: Verify the image builds and tools resolve inside it**

Run: `docker build -t kratix-devcontainer .devcontainer`
Expected: build succeeds.

Run: `docker run --rm kratix-devcontainer bash -c 'tilt version && ctlptl version && ko version && kind version && kubectl version --client && go version'`
Expected: every version prints (all CLIs present, Go 1.26).

- [ ] **Step 4: Verify end-to-end in-container (kind reachability)**

Open the repo in VS Code → "Reopen in Container" (or `devcontainer up --workspace-folder .` with the devcontainer CLI). Inside the container:
Run: `make dev-tools >/dev/null 2>&1 || true; make dev GITOPS=argo-git`
Expected: cluster comes up on the host daemon; all Tilt resources reach green.

**If the API server is unreachable from inside the container** (known DooD caveat), rewrite the kubeconfig server host after `dev-cluster`:
Run: `kubectl config set-cluster kind-platform --server=https://host.docker.internal:$(docker port platform-control-plane 6443/tcp | cut -d: -f2)`
Then re-run `make dev`. Document whichever proves necessary in `docs/dev-environment.md`.
Expected: `kubectl --context kind-platform get nodes` returns the node from inside the container; `make dev` reaches READY.

- [ ] **Step 5: Commit**

```bash
jj commit -m "feat(dev): devcontainer with pinned CLIs (docker-outside-of-docker)"
```

---

## Self-Review

**Spec coverage:**
- §4.1 ctlptl cluster + registry → Task 2. ✅
- §4.2 host build + live_update → Tasks 3, 4. ✅
- §4.3 Tiltfile orchestration + config flag → Tasks 4, 5. ✅
- §4.4 GitOps matrix (argo-git/flux-bucket/flux-git, argo-bucket rejected) → Task 5 (+ fail() guard). ✅
- §4.5 devcontainer (docker-outside-of-docker, forwarded Tilt UI, CLI source of truth) → Task 7. ✅
- §4.6 tooling prereqs (make dev-tools + dotfiles Brewfile) → Task 1. ✅
- §5 dependency graph (cert-manager → reconciler/store → kratix → StateStore CR → register destination) → Tasks 4–5 via resource_deps. ✅
- §6 UX entry points (make dev/dev-down) → Task 6. ✅
- §7 coexistence (additive only; quick-start.sh untouched) → Global Constraints + no task edits those files. ✅
- §8 testing incl. matrix coverage + live_update measurement + quick-start unaffected → Tasks 4–7 verification steps; add explicit quick-start regression check below. ✅
- §10 risks: pipeline-adapter registry ref → Task 4 Steps 2/4 (env literal + match_in_env_vars); Argo+Gitea kind patching reused → Task 5; devcontainer kind networking → Task 7 Step 4. ✅

**Added for §8 completeness — quick-start regression (run once before finishing):**
Run: `make quick-start` on a clean machine (or CI) and confirm it still succeeds unchanged. This is a gate, not a code change; note the result in the PR.

**Placeholder scan:** No TBD/TODO/"handle appropriately". Sample-Promise path in Task 5 Step 4 is explicitly "confirm the path first with `ls`" because the sample filename varies by repo state — this is a deliberate discovery step, not a placeholder.

**Type/name consistency:** `kind-platform` context, `platform` cluster, `kratix-platform-controller-manager` deployment, `PIPELINE_ADAPTER_IMG`, image `docker.io/syntasso/kratix-platform:dev`, resource names (`go-build`, `cert-manager`, `statestore-backend`, `statestore-cr`, `register-destination`) are used consistently across Tasks 4–6. `--gitops` values match the matrix in Tasks 5–6 and docs.
