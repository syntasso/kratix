#!/usr/bin/env bash
# Stops the Tilt dev env cleanly, keeping the cluster.
#
# Deletes Kratix Promises FIRST and polls until they are actually gone, so the
# controller has time to run their finalizers (delete pipelines + dependency /
# resource / CRD cleanup, including any sub-promises an aggregate Promise
# installed). If Tilt (and thus the controller) is stopped while a Promise is
# still Terminating, the finalizers never run and it is stuck forever — so if
# cleanup does not complete we REFUSE to stop Tilt and leave the controller up
# for you to investigate.
set -euo pipefail
ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/../.." &> /dev/null && pwd )
GITOPS="${1:-argo-git}"
CTX=kind-platform
TIMEOUT_SECS=360

promise_count() { kubectl --context "$CTX" get promises.platform.kratix.io --no-headers 2>/dev/null | wc -l | tr -d ' '; }

if kubectl --context "$CTX" cluster-info >/dev/null 2>&1 && \
   kubectl --context "$CTX" get crd promises.platform.kratix.io >/dev/null 2>&1; then
    if [ "$(promise_count)" != "0" ]; then
        echo "Deleting Promises; the controller will run their finalizers..."
        # --wait=false: don't rely on kubectl's batch wait (it can return before
        # custom finalizers finish); we poll until the API shows none remain.
        kubectl --context "$CTX" delete promises.platform.kratix.io --all --wait=false || true
        echo "Waiting for all Promises to be finalized and removed (up to ${TIMEOUT_SECS}s)..."
        elapsed=0
        while [ "$(promise_count)" != "0" ]; do
            if [ "$elapsed" -ge "$TIMEOUT_SECS" ]; then
                echo "" >&2
                echo "ERROR: Promises still have finalizers after ${TIMEOUT_SECS}s:" >&2
                kubectl --context "$CTX" get promises.platform.kratix.io >&2
                echo "Leaving the controller running so finalizers can still run." >&2
                echo "Investigate, or 'make dev-clean' to delete the whole cluster." >&2
                exit 1
            fi
            sleep 5
            elapsed=$((elapsed + 5))
        done
        echo "All Promises removed."
    fi
fi

echo "Stopping Tilt..."
tilt down -- --gitops="$GITOPS" || true
pkill -f "tilt up --context kind-platform" 2>/dev/null || true
