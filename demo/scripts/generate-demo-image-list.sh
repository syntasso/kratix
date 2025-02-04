#!/usr/bin/env bash

set -e

output_debugging_info() {
    echo "=============== PLATFORM STATUS ==============="
    kubectl --context kind-platform get pods -A
    kubectl --context kind-platform get kustomizations -A
    kubectl --context kind-platform get promises
    echo "=============== WORKER STATUS ==============="
    kubectl --context kind-platform get pods -A
    kubectl --context kind-worker get kustomizations -A
    kubectl --context kind-worker get events
}
trap output_debugging_info EXIT

function sync() {
  flux reconcile kustomization $1-dependencies --namespace flux-system --context $2 --with-source
  # the resources kustomization blocks on the crds kustomization being ready, so sometimes this command fails on the first try
  run "Waiting for $1-resources to reconcile" flux reconcile kustomization $1-resources --namespace flux-system --context $2 --with-source
}

sleep_time=5
function run() {
  total_timeout=0

  until ${@:2}
  do
    echo "$1"
    sleep $sleep_time
    total_timeout=$((total_timeout + sleep_time))
    if [[ "$total_timeout" == "300"  ]]; then
      echo "timedout after 300 seconds"
      exit 1
    fi
  done
}

DEMO_DIR="$( cd $(dirname $0)/.. && pwd)"
export LPASS_SLACK_URL="dontneedtherealurl"

echo "Creating KinD clusters and installing Kratix"
./scripts/setup --two-clusters

echo "Installing Promise"
kubectl create -f app-as-a-service/promise.yaml
run "Waiting promises to exist" kubectl --context kind-platform get deployments.marketplace.kratix.io
sync kratix-worker kind-worker
sync kratix-platform kind-platform

echo "Requesting resource"
kubectl apply -f app-as-a-service/resource-request.yaml
sync kratix-worker kind-worker
sync kratix-platform kind-platform

echo "Waiting for the demo app to be running"
SKIP_BROWSER=yes ./scripts/wait-and-open-browser-when-app-ready


echo "Generating \"demo-image-list\" file"
$(rm /tmp/demo-image-list || true) > /dev/null 2>&1

kubectl get pods --context kind-worker --all-namespaces -o jsonpath="{.items[*].spec.containers[*].image}" |\
  tr -s '[[:space:]]' '\n' > /tmp/demo-image-list
echo >>  /tmp/demo-image-list
kubectl get pods --context kind-worker --all-namespaces -o jsonpath="{.items[*].spec.initContainers[*].image}" |\
  tr -s '[[:space:]]' '\n' >>  /tmp/demo-image-list
echo >>  /tmp/demo-image-list
kubectl get pods --context kind-platform --all-namespaces -o jsonpath="{.items[*].spec.containers[*].image}" |\
  tr -s '[[:space:]]' '\n' >> /tmp/demo-image-list
echo >>  /tmp/demo-image-list
kubectl get pods --context kind-platform --all-namespaces -o jsonpath="{.items[*].spec.initContainers[*].image}" |\
  tr -s '[[:space:]]' '\n' >>  /tmp/demo-image-list

cat /tmp/demo-image-list | sort | uniq | grep -v "syntasso/kratix-platform" | grep -v "syntassodev/kratix-platform" > demo-image-list

echo "The \"demo-image-list\" is now up to date."
