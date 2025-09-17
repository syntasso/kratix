#!/usr/bin/env bash

set -eux

kind delete clusters --all

if [ "$STATE_STORE" == "git" ]; then
    platform_helm_values_path="hack/platform/helm-values-gitea.yaml"
    state_store_install_path="hack/platform/gitea-install.yaml"
    job_pod_namespace="gitea"
    job_pod_labels="app.kubernetes.io/instance=gitea"
elif [ "$STATE_STORE" == "bucket" ]; then
    platform_helm_values_path="hack/platform/helm-values-minio.yaml"
    state_store_install_path="hack/platform/minio-install.yaml"
    job_pod_namespace="kratix-platform-system"
    job_pod_labels="run=minio"
else
    echo "No supported State Store specified"
    exit 1
fi

make distribution
make gitea-cli
./charts/scripts/generate-templates-and-crds ./distribution/kratix.yaml
export DOCKER_BUILDKIT=1

echo "setup platform and install StateStore"
kind create cluster --image kindest/node:v1.27.3 --name platform --config hack/platform/kind-platform-config.yaml
make install-cert-manager
make build-and-load-kratix
make build-and-load-work-creator
helm install kratix charts/kratix/ -f "$platform_helm_values_path" --wait

if [ "$STATE_STORE" == "git" ]; then
    source ./scripts/utils.sh
    generate_gitea_credentials "kind-platform"
fi

echo "install statestore in platform cluster"
kubectl --context kind-platform apply --filename "$state_store_install_path"
kubectl --context kind-platform wait --for=condition=Ready --timeout=300s -n $job_pod_namespace pod -l $job_pod_labels

echo "create worker cluster"
kind create cluster --image kindest/node:v1.27.3 --name worker --config hack/destination/kind-worker-config.yaml
echo "helm install kratix-destination"

extra_args="--path worker-1"
if [ "$STATE_STORE" == "git" ]; then
    extra_args="--path .\/destinations\/dev --git"
fi

./scripts/install-gitops --context kind-worker --platform-cluster-name platform $extra_args

if [ "$STATE_STORE" == "git" ]; then
    copy_gitea_credentials "kind-platform" "kind-worker"
else
    kubectl --context kind-platform apply --filename config/samples/platform_v1alpha1_worker.yaml
fi

kubectl --context kind-worker wait --for=condition=Ready --timeout=300s -n flux-system kustomization kratix-worker-resources

echo "helm e2e test setup completed; now verify Jenkins"
./scripts/install-jenkins.sh
