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
on the host (for the cluster's OS/arch) and synced into the running pod, no image
rebuild and no `kind load`.

Note: a live_update syncs the binary into the running pod but does not re-tag the
registry image. A genuine change to `work-creator`/`pipeline-adapter` that
pipeline pods must pull needs a full rebuild — trigger it from the Tilt UI (or
`tilt trigger`).

## Teardown

    make dev-down     # stop Tilt, keep the cluster + registry (fast restart)
    make dev-clean    # also delete the cluster + registry (full teardown)

## GitOps matrix

| Cell (`GITOPS=`) | Reconciler | State store | Notes |
|------------------|-----------|-------------|-------|
| `argo-git` (default) | Argo CD | Gitea (Git) | Local dev |
| `flux-bucket`    | Flux | MinIO (Bucket) | CI parity + bucket testing |
| `flux-git`       | Flux | Gitea (Git) | Flux + git |

`argo-bucket` is unsupported: Argo CD has no bucket source.

## Dev container (VS Code / Codespaces)

`.devcontainer/` bundles the Go toolchain and every CLI above. "Reopen in
Container" (or `devcontainer up`), then run `make dev`. Docker is provided via
docker-outside-of-docker (host socket), so `ctlptl`/`kind` create clusters on the
host daemon.

If the cluster API server is unreachable from inside the container (a known
docker-outside-of-docker caveat), point kubeconfig at the host after
`make dev-cluster`:

    kubectl config set-cluster kind-platform \
      --server=https://host.docker.internal:$(docker port platform-control-plane 6443/tcp | cut -d: -f2)

then re-run `make dev`.

## Relationship to quick-start.sh

`scripts/quick-start.sh` is unchanged and still used by CI/enterprise. This Tilt
flow is the additive, faster dev-loop alternative.
