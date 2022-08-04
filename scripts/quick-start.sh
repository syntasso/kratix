#!/usr/bin/env bash

set -eu

ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )

KRATIX_DISTRIBUTION="${ROOT}/distribution/kratix.yaml"
MINIO_INSTALL="${ROOT}/hack/platform/minio-install.yaml"
PLATFORM_WORKER="${ROOT}/config/samples/platform_v1alpha1_worker_cluster.yaml"
GITOPS_WORKER_INSTALL="${ROOT}/hack/worker/gitops-tk-install.yaml"
GITOPS_WORKER_RESOURCES="${ROOT}/hack/worker/gitops-tk-resources.yaml"

source "${ROOT}/scripts/utils.sh"

RECREATE=false
LOCAL_IMAGES=false
WAIT="0s"

usage() {
    echo -e "Usage: quick-start.sh [--help] [--recreate] [--local]"
    echo -e "\t--help, -h\t Prints this message"
    echo -e "\t--recreate, -r\t Deletes pre-existing KinD Clusters"
    echo -e "\t--local, -l\t Build and load Kratix images to KinD cache"
    exit "${1:-0}"
}

load_options() {
    for arg in "$@"; do
      shift
      case "$arg" in
        '--help')     set -- "$@" '-h'   ;;
        '--recreate') set -- "$@" '-r'   ;;
        '--local')    set -- "$@" '-l'   ;;
        *)            set -- "$@" "$arg" ;;
      esac
    done

    OPTIND=1
    while getopts "hrl" opt
    do
      case "$opt" in
        'r') RECREATE=true ;;
        'h') usage ;;
        'l') LOCAL_IMAGES=true; WAIT="0s" ;;
        *) usage 1 ;;
      esac
    done
    shift $(expr $OPTIND - 1)
}

verify_prerequisites() {
    log -n "Looking for KinD..."
    if ! which kind > /dev/null; then
        error "kind not found in PATH"
        log "Please install KinD before running this script"
        exit 1
    fi
    success_mark

    log -n "Looking for kubectl..."
    if ! which kubectl > /dev/null; then
        error "kubectl not found in PATH"
        log "Please install kubectl before running this script"
        exit 1
    fi
    success_mark

    log -n "Looking for docker..."
    if ! which kubectl > /dev/null; then
        error "docker not found in PATH"
        log "Please install docker before running this script"
        exit 1
    fi
    success_mark

    if ${RECREATE}; then
        log -n "Deleting pre-existing clusters..."
        run kind delete clusters platform worker
    else
        log -n "Verifying no clusters exist..."
        if kind get clusters 2>&1 | grep --quiet --regexp "platform\|worker"; then
            error_mark
            log "Please ensure there's no KinD clusters named $(info platform) or $(info worker)."
            log "You can remove them clusters by running: "
            log "\tkind delete clusters platform worker"
            log "You can also run this script with $(info --recreate)"
            exit 1
        fi
    fi
}

patch_kind_networking() {
    PLATFORM_CLUSTER_IP=`docker inspect platform-control-plane | grep '"IPAddress": "172' | awk '{print $2}' | awk -F '"' '{print $2}'` 
    sed -i'' -e "s/172.18.0.2/$PLATFORM_CLUSTER_IP/g" ${ROOT}/hack/worker/gitops-tk-resources.yaml
    rm -f ${ROOT}/hack/worker/gitops-tk-resources.yaml-e
}

build_and_load_local_images() {
    (
        docker build --tag syntasso/kratix-platform:dev .
        kind load docker-image syntasso/kratix-platform:dev --name platform
    ) &

    (
        docker build --tag syntasso/kratix-platform-work-creator:dev --file DockerfileWorkCreator
        kind load docker-image syntasso/kratix-platform-work-creator:dev --platform
    ) &

    wait
}

setup_platform_cluster() {
    kubectl --context kind-platform apply --filename "${KRATIX_DISTRIBUTION}"
    kubectl --context kind-platform apply --filename "${MINIO_INSTALL}"
}

setup_worker_cluster() {
    kubectl --context kind-platform apply --filename "${PLATFORM_WORKER}"
    kubectl --context kind-worker apply  --filename "${GITOPS_WORKER_INSTALL}"
    kubectl --context kind-worker apply --filename "${GITOPS_WORKER_RESOURCES}"
}

wait_for_namespace() {
    loops=0
    set -x
    while ! kubectl --context kind-worker get namespace kratix-worker-system >/dev/null 2>&1; do
        if (( loops > 20 )); then
            exit 1
        fi
        sleep 5
        loops=$(( loops + 1 ))
    done
    success_mark
}

install_kratix() {
    verify_prerequisites

    log -n "Creating platform cluster..."
    if ! run kind create cluster --name platform \
        --config ${ROOT}/hack/platform/kind-minio-portforward.yaml \
        --wait ${WAIT}
    then
        log "\tCould not create platform cluster"
    fi

    if ${LOCAL_IMAGES}; then
        log -n "Building and loading images locally..."
        run build_and_load_local_images
    fi

    log -n "Setting up platform cluster..."
    run setup_platform_cluster

    patch_kind_networking

    log -n "Creating worker cluster..."
    if ! run kind create cluster --name worker; then
        log "\tCould not create worker cluster"
    fi

    log -n "Setting up worker cluster..."
    run setup_worker_cluster

    log -n "Waiting for system to reconcile... "
    if ! run wait_for_namespace; then
        log "\tSomething went wrong with your Kratix installation."
        log "\tCheck the pods on the platform and worker clusters for debugging information."
        exit 1
    fi

    kubectl config use-context kind-platform >/dev/null
    success "Kratix installation is complete!"
}

main() {
    load_options $@
    install_kratix
}

main $@
