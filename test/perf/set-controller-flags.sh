#!/usr/bin/env bash
# Patch the kratix controller deployment's container args to a specified set,
# then wait for the rollout to complete.
#
# Usage: set-controller-flags.sh '<json-args-array>'
#   e.g. set-controller-flags.sh '["--metrics-bind-address=:8443","--health-probe-bind-address=:8081","--leader-elect"]'

set -euo pipefail

ARGS_JSON="${1:?missing JSON args array}"
CTX="${PERF_CONTEXT:-kind-platform}"
NS="kratix-platform-system"
DEPLOY="kratix-platform-controller-manager"

PATCH=$(printf '{"spec":{"template":{"spec":{"containers":[{"name":"manager","args":%s}]}}}}' "$ARGS_JSON")

echo "patching $DEPLOY args -> $ARGS_JSON"
kubectl --context="$CTX" -n "$NS" patch deployment "$DEPLOY" --type=strategic -p "$PATCH"
kubectl --context="$CTX" -n "$NS" rollout status deploy/"$DEPLOY" --timeout=2m
echo "current args:"
kubectl --context="$CTX" -n "$NS" get deploy "$DEPLOY" -o jsonpath='{.spec.template.spec.containers[0].args}'
echo
