#!/usr/bin/env bash

set -eux

kind delete clusters --all

if [ "$STATE_STORE" == "git" ]; then
    platform_helm_values_path="hack/platform/helm-values-gitea.yaml"
    state_store_install_path="hack/platform/gitea-install.yaml"
    job_pod_namespace="gitea"
    job_pod_labels="app.kubernetes.io/instance=gitea"
elif [ "$STATE_STORE" == "bucket" ] || [ "$STATE_STORE" == "bucket-defaults" ]; then
    platform_helm_values_path="hack/platform/helm-values-bucket.yaml"
    state_store_install_path="hack/platform/seaweedfs-install.yaml"
    job_pod_namespace="seaweedfs"
    job_pod_labels="run=seaweedfs"
else
    echo "No supported State Store specified"
    exit 1
fi

make distribution
make gitea-cli
./charts/scripts/generate-templates-and-crds ./distribution/kratix.yaml
export DOCKER_BUILDKIT=1

echo "setup platform and install StateStore"
kind create cluster --image kindest/node:v1.33.1 --name platform --config hack/platform/kind-platform-config.yaml
make install-cert-manager
make build-and-load-kratix
# bucket-defaults installs the chart with its DEFAULT values, i.e. no -f. It is the only run
# that exercises the published chart's own statestore defaults: STATE_STORE=bucket overrides
# them with hack/platform/helm-values-bucket.yaml, so that case stays green even if the chart
# itself still points at the old state store.
#
# `if` and not `[ ... ] && helm_values_args=()`: the && form evaluates to 1 whenever the
# condition is false, which under set -e kills the script silently if the line ever ends up
# last in a block. `if` returns 0 when no branch runs.
helm_values_args=(-f "$platform_helm_values_path")
if [ "$STATE_STORE" == "bucket-defaults" ]; then
    helm_values_args=()
fi
# ${arr[@]+...} and not "${arr[@]}": bash 3.2 aborts on an empty array under set -u
helm install kratix charts/kratix/ ${helm_values_args[@]+"${helm_values_args[@]}"} --wait

if [ "$STATE_STORE" == "git" ]; then
    source ./scripts/utils.sh
    generate_gitea_credentials "kind-platform"
fi

echo "install statestore in platform cluster"
kubectl --context kind-platform apply --filename "$state_store_install_path"
kubectl --context kind-platform wait --for=condition=Ready --timeout=300s -n $job_pod_namespace pod -l $job_pod_labels

echo "create worker cluster"
kind create cluster --image kindest/node:v1.33.1 --name worker --config hack/destination/kind-worker-config.yaml
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
