#!/usr/bin/env bash
# Starts the Tilt dev environment in the background, waits until every resource
# is ready, then prints a READY banner and hands the terminal back. Tilt keeps
# running (detached) so controller code changes still hot-reload; stop it with
# `make dev-down`.
# Usage: up.sh [gitops-cell]   (default: argo-git)
set -euo pipefail
ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/../.." &> /dev/null && pwd )
GITOPS="${1:-argo-git}"
LOG="${ROOT}/.tilt-dev.log"

# Detect an existing Tilt server by its API (not process name): if one is
# already serving, reuse it; otherwise start a fresh one in the background.
if tilt get uiresource >/dev/null 2>&1; then
    echo "Tilt is already running — reusing it (http://localhost:10350)."
else
    echo "Starting Tilt in the background (logs: ${LOG}) ..."
    nohup tilt up --context kind-platform -- --gitops="${GITOPS}" > "${LOG}" 2>&1 &
    tilt_pid=$!
    # Wait for the API to accept connections; fail fast if Tilt dies (e.g. the
    # port is already taken) instead of later timing out against a stale server.
    ready=false
    for _ in $(seq 1 60); do
        if tilt get uiresource >/dev/null 2>&1; then ready=true; break; fi
        if ! kill -0 "${tilt_pid}" 2>/dev/null; then break; fi
        sleep 1
    done
    if [ "${ready}" != true ]; then
        echo "✗ Tilt failed to start. Last log lines:" >&2
        tail -8 "${LOG}" >&2
        exit 1
    fi
fi

echo "Waiting for all resources to become ready (first run pulls images; can take a few minutes) ..."
if ! tilt wait --for=condition=Ready uiresource --all --timeout=900s; then
    echo "" >&2
    echo "✗ Timed out waiting for resources. Open http://localhost:10350 or run: tail -f ${LOG}" >&2
    exit 1
fi

echo ""
echo "============================================================"
echo "✅  Kratix platform READY   (gitops: ${GITOPS})"
echo "------------------------------------------------------------"
echo "  Dashboard  : http://localhost:10350"
if [[ "${GITOPS}" == argo-* ]]; then
    echo "  ArgoCD UI  : https://localhost:31380  (argoadmin / argoadmin)"
fi
echo "  Use it     : kubectl --context kind-platform get promises"
echo "  Hot-reload : edit controller code -> synced into the pod (~1s)"
echo "  Logs       : tail -f ${LOG}"
echo "  Stop       : make dev-down   (keep cluster, fast restart)"
echo "               make dev-clean  (delete cluster + registry)"
echo "============================================================"
