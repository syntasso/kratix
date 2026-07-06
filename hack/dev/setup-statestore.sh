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
