#!/bin/bash

set -eu -o pipefail

echo "Removing platform-cluster Destination"
kubectl delete destination platform-cluster
echo ""

sleep 3

echo "Removing canary files from kind"
mc rm kind/kratix/platform-cluster/dependencies/kratix-canary-namespace.yaml || true
mc rm kind/kratix/platform-cluster/resources/kratix-canary-configmap.yaml || true
echo ""

echo "State of platform-cluster in MinIO:"
files=$(mc ls kind/kratix/platform-cluster -r)
if [[ -z "$files" ]]; then
  echo "No files found in MinIO for platform-cluster"
else
  echo "$files"
fi
echo ""

echo "Recreating platform-cluster Destination"
cat <<EOF | kubectl apply -f -
apiVersion: platform.kratix.io/v1alpha1
kind: Destination
metadata:
  name: platform-cluster
  labels:
    environment: platform
spec:
  stateStoreRef:
    name: default
    kind: BucketStateStore
EOF

echo ""

sleep 3

echo "State of platform-cluster in MinIO:"
files=$(mc ls kind/kratix/platform-cluster -r)
if [[ -z "$files" ]]; then
  echo "No files found in MinIO for platform-cluster"
else
  echo "$files"
fi
