#!/usr/bin/env bash

set -eu

ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )
source "${ROOT}/scripts/utils.sh"

KRATIX_DISTRIBUTION="${ROOT}/distribution/kratix.yaml"
MINIO_INSTALL="${ROOT}/hack/platform/minio-install.yaml"
PLATFORM_WORKER="${ROOT}/config/samples/platform_v1alpha1_worker_cluster.yaml"
GITOPS_WORKER_INSTALL="${ROOT}/hack/worker/gitops-tk-install.yaml"
GITOPS_WORKER_RESOURCES="${ROOT}/hack/worker/gitops-tk-resources.yaml"

RECREATE=false
LOCAL_IMAGES=false
VERSION=${VERSION:-"$(cd $ROOT; git branch --show-current)"}
DOCKER_BUILDKIT="${DOCKER_BUILDKIT:-1}"

MINIO_TIMEOUT="180s"

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
        'l') LOCAL_IMAGES=true ;;
        *) usage 1 ;;
      esac
    done
    shift $(expr $OPTIND - 1)

    # Always build local images when running from `dev`
    if [ "${VERSION}" = "dev" ]; then
        LOCAL_IMAGES=true
    fi
}

verify_prerequisites() {
    exit_code=0
    log -n "Looking for KinD..."
    if ! which kind > /dev/null; then
        error "KinD not found in PATH"
        exit_code=1
    else
        success_mark
    fi

    log -n "Looking for kubectl..."
    if ! which kubectl > /dev/null; then
        error "kubectl not found in PATH"
        exit_code=1
    else
        success_mark
    fi

    log -n "Looking for docker..."
    if ! which docker > /dev/null; then
        error "docker not found in PATH"
        exit_code=1
    else
        success_mark
    fi

    if [[ exit_code -gt 0 ]]; then
        log "\nPlease confirm you have the above prerequisites before re-running this script"
        exit $((exit_code))
    fi

    log -n "Looking for distribution/kratix.yaml... "
    if [ ! -f "${ROOT}/distribution/kratix.yaml" ]; then
        error " not found"
        log "\tEnsure you are on the $(info main) branch or run $(info make distribution)"
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
            log ""
            log "🚨 Please ensure there's no KinD clusters named $(info platform) or $(info worker)."
            log "You can run this script with $(info --recreate)"
            log "Or you can manually remove the current clusters by running: "
            log "\tkind delete clusters platform worker"
            exit 1
        fi
    fi
}

patch_kind_networking() {
    PLATFORM_CLUSTER_IP=`docker inspect platform-control-plane | grep '"IPAddress": "172' | awk '{print $2}' | awk -F '"' '{print $2}'`
    sed -i'' -e "s/172.18.0.2/$PLATFORM_CLUSTER_IP/g" ${ROOT}/hack/worker/gitops-tk-resources.yaml
    rm -f ${ROOT}/hack/worker/gitops-tk-resources.yaml-e
}


_build_kratix_image() {
    docker build --tag syntasso/kratix-platform:${VERSION} --quiet --file ${ROOT}/Dockerfile ${ROOT} &&
    kind load docker-image syntasso/kratix-platform:${VERSION} --name platform
}

_build_work_creator_image() {
    docker build --tag syntasso/kratix-platform-work-creator:${VERSION} --quiet --file ${ROOT}/DockerfileWorkCreator ${ROOT} &&
    kind load docker-image syntasso/kratix-platform-work-creator:${VERSION} --name platform
}

build_and_load_local_images() {
    export DOCKER_BUILDKIT

    log -n "Building and loading Kratix image locally..."
    if ! run _build_kratix_image; then
        error "Failed to build Kratix image"
        exit 1;
    fi

    log -n "Building and loading Work Creator image locally..."
    if ! run _build_work_creator_image; then
        error "Failed to build Work Creator image"
        exit 1;
    fi
}

setup_platform_cluster() {
    kubectl --context kind-platform apply --filename "${MINIO_INSTALL}"
    kubectl --context kind-platform apply --filename "${KRATIX_DISTRIBUTION}"
}

setup_worker_cluster() {
    kubectl --context kind-platform apply --filename "${PLATFORM_WORKER}"
    kubectl --context kind-worker apply  --filename "${GITOPS_WORKER_INSTALL}"
    kubectl --context kind-worker apply --filename "${GITOPS_WORKER_RESOURCES}"
}

wait_for_minio() {
    local timeout_flag="${1:-""}"
    opts=""
    if [ -z "${timeout_flag}" ]; then
        opts="--timeout=${MINIO_TIMEOUT}"
    fi
    kubectl wait pod --context kind-platform -n kratix-platform-system --selector run=minio --for=condition=ready ${opts}
}

wait_for_namespace() {
    local timeout_flag="${1:-""}"
    loops=0
    while ! kubectl --context kind-worker get namespace kratix-worker-system >/dev/null 2>&1; do
        if [ -z "${timeout_flag}" ] && (( loops > 20 )); then
            return 1
        fi
        sleep 5
        loops=$(( loops + 1 ))
    done
    return 0
}

install_kratix() {
    verify_prerequisites

    log -n "Creating platform cluster..."
    if ! run kind create cluster --name platform --image kindest/node:v1.24.0 \
        --config ${ROOT}/hack/platform/kind-minio-portforward.yaml
    then
        error "Could not create platform cluster"
        exit 1
    fi

    if ${LOCAL_IMAGES}; then
        build_and_load_local_images
    fi

    log -n "Setting up platform cluster..."
    if ! run setup_platform_cluster; then
        error " failed"
        exit 1
    fi

    patch_kind_networking

    log -n "Creating worker cluster..."
    if ! run kind create cluster --name worker --image kindest/node:v1.24.0; then
        error "Could not create worker cluster"
        exit 1
    fi

    log -n "Waiting for MinIO to be running..."
    if ! SUPRESS_OUTPUT=true run wait_for_minio; then
        log "\n\nIt's taking longer than usual for MinIO to start."
        log "You can check the platform pods to ensure there are no errors."
        log "This script will continue to wait for MinIO to come up. You can kill it with $(info "CTRL+C.")"
        log -n "\nWaiting for MinIO to be running... "
        run wait_for_minio --no-timeout
    fi

    log -n "Setting up worker cluster..."
    if ! run setup_worker_cluster; then
        error " failed"
        exit 1
    fi

    log -n "Waiting for system to reconcile... "
    if ! SUPRESS_OUTPUT=true run wait_for_namespace; then
        log "\n\nIt's taking longer than usual for the system to reconcile."
        log "You can check the pods on the platform and worker clusters for debugging information."
        log "This script will continue to wait. You can kill it with $(info "CTRL+C.")"
        log -n "\nWaiting for MinIO to be running... "
        run wait_for_namespace --no-timeout
    fi

    kubectl config use-context kind-platform >/dev/null
    success "Kratix installation is complete!"
}

main() {
    load_options $@
    install_kratix
}

main $@
