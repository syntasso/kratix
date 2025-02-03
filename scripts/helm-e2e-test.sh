#!/usr/bin/env bash

set -eu

if [ "$STATE_STORE" == "git" ]; then
    state_store_helm_values_path="hack/destination/helm-values-gitea.yaml"
    state_store_config_path="hack/platform/gitea-install.yaml"
    job_pod_namespace="gitea"
    job_pod_labels="app.kubernetes.io/instance=gitea"
    kustomization_namespace="default"
elif [ "$STATE_STORE" == "bucket" ]; then
    state_store_helm_values_path="hack/platform/helm-values-minio.yaml"
    state_store_config_path="hack/platform/minio-install.yaml"
    job_pod_namespace="kratix-platform-system"
    job_pod_labels="run=minio"
    kustomization_namespace="kratix-worker-config"
else
    echo "No supported State Store specified"
    exit 1
fi

make distribution
make gitea-cli
./charts/scripts/generate-templates-and-crds ./distribution/kratix.yaml
export DOCKER_BUILDKIT=1

# setup platform and install GitStateStore
kind create cluster --image kindest/node:v1.27.3 --name platform --config hack/platform/kind-platform-config.yaml
make install-cert-manager
make build-and-load-kratix
make build-and-load-work-creator
helm install kratix charts/kratix/ -f "$state_store_helm_values_path"

if [ "$STATE_STORE" == "git" ]; then
    source ./scripts/utils.sh
    generate_gitea_credentials "kind-platform"
fi

kubectl --context kind-platform apply --filename "$state_store_config_path"
kubectl --context kind-platform wait --for=condition=Ready --timeout=300s -n $job_pod_namespace pod -l $job_pod_labels

# setup worker cluster and register as destination
kind create cluster --image kindest/node:v1.27.3 --name worker --config hack/destination/kind-worker-config.yaml
helm install kratix-destination charts/kratix-destination/ -f "$state_store_helm_values_path"

if [ "$STATE_STORE" == "git" ]; then
    copy_gitea_credentials "kind-platform" "kind-worker"
else
    kubectl --context kind-platform apply --filename config/samples/platform_v1alpha1_worker.yaml
fi

kubectl --context kind-worker wait --for=condition=Ready --timeout=300s -n "$kustomization_namespace" kustomization kratix-workload-resources
