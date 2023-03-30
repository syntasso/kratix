#!/usr/bin/env bash

set -e

DEMO_DIR="$( cd $(dirname $0)/.. && pwd)"

echo "Creating clusters and installing Kratix"
./scripts/setup

echo "Installing Promise"
kubectl create -f app-as-a-service/promise.yaml
sleep 5

echo "Requesting resource"
kubectl apply -f app-as-a-service/resource-request.yaml

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
