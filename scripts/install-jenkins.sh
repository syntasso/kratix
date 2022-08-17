#!/usr/bin/env bash

set -eu -o pipefail

kubectl --context kind-platform apply --filename samples/jenkins/jenkins-promise.yaml
sleep 5 # Wait for promise resource to be created
kubectl --context kind-platform apply --filename samples/jenkins/jenkins-resource-request.yaml

loops=0
while ! kubectl --context kind-worker get pods | grep -q jenkins-example; do
    if (( loops > 45 )); then
        echo "timeout waiting for jenkins pod"
        exit 1
    fi
    sleep 1
    loops=$(( loops + 1 ))
done

if ! kubectl --context kind-worker wait pod jenkins-example --for=condition=ready --timeout 90s; then
    echo "================ LOGS ================"
    kubectl --context kind-worker logs jenkins-example
    echo "============== DESCRIBE =============="
    kubectl --context kind-worker describe pod/jenkins-example
    exit 1
fi