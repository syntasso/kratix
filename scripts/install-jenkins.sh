#!/usr/bin/env bash

set -eu -o pipefail

output_debugging_info() {
    echo "=============== PIPELINE STATUS ==============="
    kubectl --context kind-platform get pods
    echo "=============== PIPELINE LOGS ==============="
    kubectl --context kind-platform logs --selector kratix.io/promise-name=jenkins-promise-default --container jenkins-configure-pipeline
    echo "=============== WORKER EVENTS ==============="
    kubectl --context kind-worker get events
    echo "=============== TAIL OF JENKINS LOGS ==============="
    kubectl --context kind-worker logs jenkins-dev-example --tail=50
    exit 1
}


set -x
trap 'rm -f promise.yaml resource-request.yaml' EXIT
if [ -n "$GITHUB_TOKEN" ]; then
    curl -H "Authorization: token ${GITHUB_TOKEN}" \
     -H "Accept: application/vnd.github.v3.raw" -L \
     https://api.github.com/repos/syntasso/kratix-marketplace/contents/jenkins/promise.yaml > promise.yaml
    curl -H "Authorization: token ${GITHUB_TOKEN}" \
     -H "Accept: application/vnd.github.v3.raw" -L \
     https://api.github.com/repos/syntasso/kratix-marketplace/contents/jenkins/resource-request.yaml > resource-request.yaml
else
    curl -L https://raw.githubusercontent.com/syntasso/kratix-marketplace/main/jenkins/promise.yaml > promise.yaml
    sleep 5 # Wait for promise resource to be created
    curl -L https://raw.githubusercontent.com/syntasso/kratix-marketplace/main/jenkins/resource-request.yaml > resource-request.yaml
fi

kubectl --context kind-platform apply --filename promise.yaml
sleep 5 # Wait for promise resource to be created
kubectl --context kind-platform apply --filename resource-request.yaml

loops=0
while ! kubectl --context kind-worker logs jenkins-dev-example 2>/dev/null | grep -q "Jenkins is fully up and running"; do
    if (( loops > 45 )); then
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
