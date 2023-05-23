#!/usr/bin/env bash

ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )

set -eu

cd $ROOT
source "$ROOT/scripts/utils.sh"

git checkout main
git merge --no-ff --no-edit dev

export VERSION="$(commit_sha)"

make distribution

mkdir -p distribution/single-cluster

cat distribution/kratix.yaml <(echo "---") \
    hack/worker/gitops-tk-install.yaml <(echo "---") \
    hack/platform/minio-install.yaml > distribution/single-cluster/install-all-in-one.yaml

cat config/samples/platform_v1alpha1_worker_cluster.yaml <(echo "---") \
    config/samples/platform_v1alpha1_bucketstatestore.yaml <(echo "---") \
    hack/worker/gitops-tk-resources-single-cluster.yaml > distribution/single-cluster/config-all-in-one.yaml


