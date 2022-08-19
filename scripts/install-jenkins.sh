#!/usr/bin/env bash

set -eu -o pipefail

kubectl --context kind-platform apply --filename samples/jenkins/jenkins-promise.yaml
sleep 5 # Wait for promise resource to be created
kubectl --context kind-platform apply --filename samples/jenkins/jenkins-resource-request.yaml

loops=0
while ! kubectl --context kind-worker get pods | grep -q jenkins-example; do
    if (( loops > 45 )); then
        echo "timeout waiting for jenkins pod"
        echo "=============== PIPELINE STATUS ==============="
        kubectl --context kind-platform get pods
        echo "=============== PIPELINE LOGS ==============="
        kubectl --context kind-platform logs --selector kratix-promise-id=jenkins-promise-default --container xaas-request-pipeline-stage-1
        echo "=============== WORKER EVENTS ==============="
        kubectl --context kind-worker get events
        exit 1
    fi
    sleep 1
    loops=$(( loops + 1 ))
done
