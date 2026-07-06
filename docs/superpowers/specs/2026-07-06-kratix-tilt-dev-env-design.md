# Phase 1 Design — One-button kratix dev environment (Tilt + live_update + Argo)

**Date:** 2026-07-06
**Status:** Approved for planning
**Scope:** kratix repository only. The SKE umbrella is Phase 2 (separate spec).

## 1. Goal

Make the local kratix dev loop feel like one button and reload code changes in
~1 second instead of the current full `docker build` + `kind load` + deployment
rollout.

Concretely:

- `make dev` (→ `tilt up`) stands up a complete local kratix platform: cluster,
  cert-manager, a GitOps reconciler, a state store, kratix CRDs + controller, and
  a registered worker destination. The reconciler + state store are a config
  matrix (§4.4): Argo CD + Gitea is the dev default; Flux + Bucket and Flux + Git
  are selectable so this system can eventually host CI — including BucketStateStore
  tests, which only Flux supports.
- Editing controller Go code hot-reloads into the running pod in ~1s (no image
  rebuild, no `kind load`).
- The Tilt web UI is the environment dashboard: every component's health, logs,
  and readiness in one pane.
- A containerized VS Code dev environment (`.devcontainer/`) packages every CLI
  and lets a new contributor go from clone → running platform with no host
  tooling beyond Docker + an editor.
- The existing `scripts/quick-start.sh` (flux + MinIO) is **untouched**, so
  kratix CI, enterprise-kratix, and the backstage-controller keep working.

Non-goals for this phase are listed in §9.

## 2. Why this shape

The measured inner-loop cost is the controller image build. kratix packages two
binaries — `cmd/main.go` (the `manager`) and `work-creator/main.go`
(`pipeline-adapter`) — into a **single** image (`syntasso/kratix-platform:dev`,
base `alpine/git`). Every code change today triggers a full multi-stage
`docker build`, a `kind load` into each cluster, and a rollout.

Two independent levers remove most of that cost:

1. **`live_update`** syncs a rebuilt binary into the already-running pod and
   restarts the process in place — no image rebuild, no rollout. The `alpine/git`
   base has `/bin/sh`, so Tilt's `restart_process` works without changes.
2. **A shared local registry** (via `ctlptl`) means the rare full rebuild pushes
   to `localhost:5000` and kind pulls from it — eliminating `kind load` entirely.
   This is the lever that scales to the many single-binary SKE controllers in
   Phase 2.

Pure `ko` does not map cleanly to kratix's two-binaries-one-image contract, so
kratix uses host `go build` + `live_update`. `ko` becomes the standard for the
single-binary SKE controllers in Phase 2.

## 3. Architecture — three layers with clean seams

```
┌─────────────────────────────────────────────────────────────┐
│ .devcontainer/  (optional entry) — CLIs + docker + make dev   │
├─────────────────────────────────────────────────────────────┤
│ Orchestration layer — Tiltfile: resource graph, deps,         │
│   readiness, the button, the dashboard                        │
├─────────────────────────────────────────────────────────────┤
│ Build layer — host `go build` (shared cache) → thin           │
│   docker_build → live_update sync + restart_process           │
├─────────────────────────────────────────────────────────────┤
│ Cluster layer — ctlptl cluster + shared localhost:5000        │
│   registry (kills per-cluster `kind load`)                    │
└─────────────────────────────────────────────────────────────┘
```

Each layer has one job, a defined interface, and can be understood in isolation:

- **Cluster layer** — input: a declarative cluster spec. Output: a running kind
  cluster wired to a local registry, contexts set. Depends on: Docker, ctlptl,
  kind. No knowledge of kratix.
- **Build layer** — input: Go source. Output: an image tag in the local registry
  and a live-syncable binary. Depends on: Go toolchain. No knowledge of the
  cluster graph.
- **Orchestration layer** — input: the above two as building blocks. Output: a
  reconciled, healthy platform + dashboard. Owns ordering and readiness only.

## 4. Components

### 4.1 Cluster layer — `ctlptl` + local registry

- A `ctlptl` cluster spec (checked into `hack/dev/`) declaring a kind cluster
  attached to a `ctlptl` registry at `localhost:5000`.
- Idempotent: re-running `ctlptl apply` is a no-op if the cluster exists.
- Single-cluster by default for the dev loop (platform doubles as destination),
  matching `--single-cluster`; multi-cluster remains available via a flag/second
  spec but is not the default (faster, less RAM).
- Teardown: `ctlptl delete` (wraps `kind delete` + registry cleanup).

### 4.2 Build layer — host build + live_update

- Host `go build -o bin/manager cmd/main.go` and
  `go build -o bin/pipeline-adapter work-creator/*.go`, reusing the host Go build
  cache for fast incremental compiles.
- A thin dev `Dockerfile` (or Tilt `docker_build` with a `dockerfile_contents`
  inline) `FROM alpine/git` that copies the prebuilt binaries — no in-image Go
  build.
- `live_update`:
  - `sync bin/manager` → the pod path, then `restart_process`.
  - Full rebuild fallback triggered by changes to `go.mod`/`go.sum` or the
    Dockerfile.
- Pipeline-adapter code changes are picked up by the next pipeline run
  automatically (pipeline jobs are short-lived and pull the fresh image tag);
  no live_update path is needed for them.

### 4.3 Orchestration layer — the `Tiltfile`

- Declares one Tilt resource per platform component with explicit
  `resource_deps` and readiness probes so ordering is deterministic and visible.
- Reuses existing manifests/scripts as much as possible: `install-gitops`
  (Argo path), the Gitea install manifest, the destination-registration script,
  and the CRD/controller manifests. The Tiltfile orchestrates them; it does not
  reimplement them.
- The `tilt up` button + web UI is the deliverable "dashboard."
- A Tilt config flag (`config.define_bool`) toggles single vs multi cluster and,
  later, the SKE umbrella overlay (Phase 2 plugs in here).

### 4.4 GitOps — configurable reconciler + state store matrix

The reconciler and state store are Tilt config options
(`config.define_string`), not hardcoded, because CI will eventually run on this
system and must exercise BucketStateStores — which only Flux supports.

| Combination        | Reconciler | State store       | Purpose                          | Supported |
|--------------------|------------|-------------------|----------------------------------|-----------|
| `argo-git` (default) | Argo CD   | Gitea (Git)       | Local dev default                | ✅        |
| `flux-bucket`      | Flux       | MinIO (Bucket)    | CI parity + bucket-store testing | ✅        |
| `flux-git`         | Flux       | Gitea (Git)       | Flux + git coverage              | ✅        |
| `argo-bucket`      | Argo CD    | Bucket            | —                                | ❌ upstream: Argo has no bucket source |

- Each combination reuses the existing `scripts/install-gitops` paths
  (`--gitops-provider argo|flux`, `--git` / bucket) and the manifests in `hack/`.
  The Tiltfile selects and orchestrates them; it does not reimplement them.
- `make dev` defaults to `argo-git`. A single flag
  (`tilt up -- --gitops=flux-bucket`, or `make dev GITOPS=flux-bucket`) switches
  the matrix cell.
- `scripts/quick-start.sh` (flux + MinIO) remains the untouched default path for
  today's CI/enterprise; the config matrix is the migration target that lets CI
  move onto Tilt without losing bucket coverage.
- MinIO replacement and Argo-from-bucket remain out of scope (§9); `argo-bucket`
  is an upstream Argo limitation, not something this phase resolves.

### 4.5 Containerized VS Code dev environment (`.devcontainer/`)

- A `.devcontainer/devcontainer.json` + Dockerfile that bundles the full CLI set
  (kubectl, kind, ctlptl, tilt, ko, docker CLI, helm, yq, go) at pinned versions,
  plus recommended VS Code extensions.
- **Docker access:** docker-outside-of-docker (mount the host Docker socket) so
  `ctlptl`/`kind` create clusters on the host daemon — the reliable pattern for
  kind + devcontainers and GitHub Codespaces.
- Tilt runs inside the container; its UI is forwarded via `forwardPorts`.
- `postCreateCommand` warms caches (e.g. `go mod download`); the documented
  "button" is: open in container → `make dev`.
- This is the single source of truth for "what CLIs at what versions," shared by
  local devcontainer use and (later) Codespaces.

### 4.6 Tooling prerequisites

`tilt`, `ko`, and `ctlptl` are not currently installed. The design adds:

- A `make dev-tools` target (or a `hack/dev/install-tools.sh`) that installs the
  pinned CLIs via Homebrew on host use, and bakes them into the devcontainer
  image for container use.
- Per repo convention, host-install additions are also recorded in the user's
  dotfiles Brewfile.

## 5. Dependency graph (ordered)

The reconciler and state-store nodes vary by the §4.4 matrix cell; everything
else is fixed. Shown for the `argo-git` default (Flux/bucket nodes swap in for
`flux-*` cells):

```
ctlptl cluster + registry
  → cert-manager                    (wait: deployments available)
  → reconciler: Argo CD | Flux      (wait: reconciler ready)
  → state store: Gitea | MinIO      (wait: pod ready + bootstrap job complete)
  → kratix CRDs + controller  [live_update-enabled]
                                    (wait: controller-manager available)
  → StateStore CR (Git|Bucket)
  → register worker destination
                                    (wait: destination Ready)
  → reconciler reconciling (Argo Application | Flux Kustomization)
  → READY (dashboard green)
```

## 6. Dev UX / entry points

| Command            | Effect                                                        |
|--------------------|---------------------------------------------------------------|
| `make dev`         | `ctlptl apply` (if needed) then `tilt up` — the button        |
| `tilt up`          | Same, assuming cluster exists                                 |
| `make dev-down`    | `tilt down`; optional `ctlptl delete`                         |
| Open in devcontainer | Full CLI env; then `make dev`                               |
| `scripts/quick-start.sh` | Unchanged (flux + MinIO), for CI/enterprise             |

## 7. Coexistence & back-compat

- `scripts/quick-start.sh`, `install-gitops` (flux path), the `hack/` manifests,
  and all existing `make` targets that CI/enterprise depend on are unchanged.
- New assets live under `hack/dev/`, a new `Tiltfile`, `.devcontainer/`, and new
  `make dev*` targets — additive only.
- Verification includes running the existing `make quick-start` path to confirm
  it is unaffected.

## 8. Testing / verification

- `make dev` produces a platform where a sample Promise installs and reconciles
  end-to-end via Argo (smoke test).
- Editing `cmd/main.go` reloads into the pod in ~1s (measured, live_update path
  confirmed in Tilt logs — no image rebuild line).
- `ctlptl delete` + `make dev` from cold reproduces a healthy env idempotently.
- Existing `make quick-start` (flux + MinIO) still succeeds unchanged.
- Devcontainer: open in container, `make dev` reaches READY.
- **Matrix coverage (CI migration target):** each supported §4.4 cell
  (`argo-git`, `flux-bucket`, `flux-git`) reaches READY and reconciles a sample
  Promise; `flux-bucket` is the cell that proves BucketStateStore coverage is
  preserved when CI moves onto this system.

## 9. Out of scope (Phase 2 / later)

- The SKE umbrella: ske-operator, ske-platform-manager, ske-cortex-controller,
  backstage-controller, k8s-health-agent, `ske-mcp-server`, and the Backstage
  instance from `../backstage` (with ske-plugins). These plug into the same Tilt
  graph in Phase 2, using `ko` for their single-binary images.
- Replacing MinIO with another state-store backend.
- Wiring Argo CD to reconcile from a BucketStateStore (Argo-from-bucket).
- A prebaked kind node image (cert-manager/Argo/Gitea preloaded) to cut first
  `tilt up` pull time — noted as a future speed win.

## 10. Risks / open items

- **Argo + Gitea in kind** needs cert/networking patching — reuse the logic
  already in `install-gitops`; do not reimplement.
- **Registry-qualified image refs:** with a local registry, verify the controller
  references the `pipeline-adapter` image by a tag the in-cluster runtime can
  pull from `localhost:5000`.
- **First `tilt up`** still pays cert-manager/Argo/Gitea image pulls; mitigated
  later by a prebaked node image (§9).
- **Devcontainer + kind networking:** docker-outside-of-docker chosen for
  reliability; confirm Tilt UI port-forward and kind API server reachability from
  inside the container (and in Codespaces).
