#!/usr/bin/env bash

set -eu -o pipefail

output_debugging_info() {
    echo "=============== PIPELINE STATUS ==============="
    kubectl --context kind-platform get pods
    echo "=============== PIPELINE LOGS ==============="
    kubectl --context kind-platform logs --selector kratix-promise-id=jenkins-promise-default --container xaas-request-pipeline-stage-1
    echo "=============== WORKER EVENTS ==============="
    kubectl --context kind-worker get events
    exit 1
}

kubectl --context kind-platform apply --filename samples/jenkins/jenkins-promise.yaml
sleep 5 # Wait for promise resource to be created
kubectl --context kind-platform apply --filename samples/jenkins/jenkins-resource-request.yaml

loops=0
while ! kubectl --context kind-worker logs jenkins-example 2>/dev/null | grep -q "Jenkins is fully up and running"; do
    if (( loops > 120 )); then
        echo "jenkins never reported to be up and running"
        output_debugging_info
        exit 1
    fi
    sleep 1
    loops=$(( loops + 1 ))
done

# wait for kubernetes to report "Running"; 105 to ensure we wait more than the readiness probe takes to trigger
if ! kubectl --context kind-worker wait pod jenkins-example --for=condition=ready --timeout 105s; then
    output_debugging_info
    exit 1
fi
