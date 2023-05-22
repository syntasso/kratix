#!/usr/bin/env bash

set -e

ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )

GIT_REPO=false

usage() {
    echo -e "Usage: quick-start.sh [--help] [--git]"
    echo -e "\t--help, -h\t Prints this message"
    echo -e "\t--git, -g\t Use Gitea as local repository in place of default local MinIO"
    exit "${1:-0}"
}

load_options() {
    for arg in "$@"; do
      shift
      case "$arg" in
        '--help')     set -- "$@" '-h'   ;;
        '--git')      set -- "$@" '-g'   ;;
        *)            set -- "$@" "$arg" ;;
      esac
    done

    OPTIND=1
    while getopts "hg" opt
    do
      case "$opt" in
        'h') usage ;;
        'g') GIT_REPO=true;;
        *) usage 1 ;;
      esac
    done
    shift $(expr $OPTIND - 1)
}

prepare_cluster() {
  local yqOpts='.metadata.name = "platform" | .metadata.labels.environment = "platform"'
  local resourcesFile="${ROOT}/hack/worker/gitops-tk-resources.yaml"

  kubectl --context kind-platform apply -f ${ROOT}/hack/worker/gitops-tk-install.yaml

  if ${GIT_REPO}; then
    resourcesFile="${ROOT}/hack/worker/gitops-tk-resources-git.yaml"
    yqOps="${yqOpts} | .spec.stateStoreRef.kind = \"GitStateStore\""
  fi

  sed 's/worker-cluster-1/platform/g' $resourcesFile  |
    kubectl --context kind-platform apply -f -

  yq "${yqOpts}" $ROOT/config/samples/platform_v1alpha1_worker_cluster.yaml |
    kubectl --context kind-platform apply -f -
}

main() {
    load_options $@
    prepare_cluster
}

if [ "$0" = "${BASH_SOURCE[0]}" ]; then
    main $@
fi
