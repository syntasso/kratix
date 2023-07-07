#!/usr/bin/env bash

set -eu -o pipefail

output_debugging_info() {
    echo "=============== PIPELINE STATUS ==============="
    kubectl --context kind-platform get pods
    echo "=============== PIPELINE LOGS ==============="
    kubectl --context kind-platform logs --selector kratix-promise-id=jenkins-promise-default --container xaas-configure-pipeline-stage-1
    echo "=============== WORKER EVENTS ==============="
    kubectl --context kind-worker get events
    echo "=============== TAIL OF JENKINS LOGS ==============="
    kubectl --context kind-worker logs jenkins-dev-example --tail=50
    exit 1
}

kubectl --context kind-platform apply --filename https://raw.githubusercontent.com/syntasso/kratix-marketplace/migration/jenkins/promise.yaml
sleep 5 # Wait for promise resource to be created
kubectl --context kind-platform apply --filename https://raw.githubusercontent.com/syntasso/kratix-marketplace/migration/jenkins/resource-request.yaml

loops=0
while ! kubectl --context kind-worker logs jenkins-dev-example 2>/dev/null | grep -q "Jenkins is fully up and running"; do
    if (( loops > 30 )); then
        echo "jenkins never reported to be up and running"
        output_debugging_info
        exit 1
    fi
    sleep 10
    echo "=============== JENKINS STATUS AND LOGS ==============="
    kubectl --context kind-worker get pod jenkins-dev-example || true
    kubectl --context kind-worker logs jenkins-dev-example --tail=10 || true
    echo "===============          END            ==============="
    loops=$(( loops + 1 ))
done

# wait for kubernetes to report "Running"; 105 to ensure we wait more than the readiness probe takes to trigger
if ! kubectl --context kind-worker wait pod jenkins-dev-example --for=condition=ready --timeout 300s; then
    output_debugging_info
    exit 1
fi
