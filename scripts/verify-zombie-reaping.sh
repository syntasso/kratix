#!/usr/bin/env bash
# Regression check for kratix issue #892: the controller-manager container
# leaked zombie `git remote-https` helper processes because its PID 1 never
# reaped orphaned processes. The fix is `tini` as PID 1 (Dockerfile
# ENTRYPOINT) *and* both deployment manifests actually invoking it —
# spec.containers[].command overrides the image ENTRYPOINT entirely, so a
# Dockerfile-only fix would have shipped as a no-op.
#
# This proves the PID-1-reaping mechanism directly against the built image:
# it launches a process that deliberately orphans a child (standing in for
# `/manager` orphaning a `git remote-https` helper — exercising the real git
# code path needs a live cluster; see the "git" flavour of the system-test CI
# matrix, or the manual kubectl-based repro in issue #892, for that), once
# the way the *old* manifests ran it (no init) and once the way the *fixed*
# manifests run it (tini wrapping the app). One image build covers both,
# since tini is installed in the image either way; only the runtime
# `--entrypoint` override changes.
#
# Usage (from the repo root):
#   docker build -t kratix:verify-892 .
#   scripts/verify-zombie-reaping.sh kratix:verify-892 plain   # old manifests: expect a zombie (exit 1)
#   scripts/verify-zombie-reaping.sh kratix:verify-892 tini    # fixed manifests: expect none (exit 0)
set -euo pipefail

IMAGE="${1:?usage: $0 <image> plain|tini}"
MODE="${2:?usage: $0 <image> plain|tini}"

# Orphans a `sleep 3`: the subshell backgrounds it and exits immediately, so
# the still-running sleep reparents to whichever process is PID 1 in this
# container. Once it exits (3s later) it becomes a zombie unless PID 1 reaps it.
ORPHAN_SCRIPT='(sleep 3 &); sleep 20'

case "$MODE" in
  plain)
    CID=$(docker run -d --entrypoint /bin/sh "$IMAGE" -c "$ORPHAN_SCRIPT")
    ;;
  tini)
    CID=$(docker run -d --entrypoint /sbin/tini "$IMAGE" -- /bin/sh -c "$ORPHAN_SCRIPT")
    ;;
  *)
    echo "unknown mode: $MODE (want plain|tini)" >&2
    exit 2
    ;;
esac

cleanup() { docker rm -f "$CID" >/dev/null 2>&1 || true; }
trap cleanup EXIT

# Give the orphaned sleep time to finish (3s) and, if nothing reaps it,
# settle into a zombie.
sleep 6

zombies=$(docker exec "$CID" sh -c 'cat /proc/*/status 2>/dev/null | grep -c "^State:.*Z" || true')

echo "mode=$MODE zombies=$zombies"
if [ "$zombies" -gt 0 ]; then
  echo "FAIL: orphaned process was not reaped (mode=$MODE)"
  docker exec "$CID" sh -c 'cat /proc/*/status 2>/dev/null | grep -B2 "^State:.*Z"' || true
  exit 1
fi
echo "PASS: orphaned process was reaped (mode=$MODE)"
