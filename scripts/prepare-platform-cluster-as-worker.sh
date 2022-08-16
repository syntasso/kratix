#!/usr/bin/env bash

set -e

ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )

kubectl --context kind-platform apply -f ${ROOT}/test/integration/assets/platform_worker_cluster_1.yaml
kubectl --context kind-platform apply -f ${ROOT}/hack/worker/gitops-tk-install.yaml
kubectl --context kind-platform apply -f ${ROOT}/test/integration/assets/platform_worker_cluster_1_gitops-tk-resources.yaml
