#!/usr/bin/env bash

set -eu

ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )
source "${ROOT}/scripts/utils.sh"
source "${ROOT}/scripts/install-gitops"

KRATIX_DISTRIBUTION="${ROOT}/distribution/kratix.yaml"
MINIO_INSTALL="${ROOT}/hack/platform/minio-install.yaml"
PLATFORM_WORKER="${ROOT}/config/samples/platform_v1alpha1_worker_cluster.yaml"
GITOPS_WORKER_INSTALL="${ROOT}/hack/worker/gitops-tk-install.yaml"
GITOPS_WORKER_RESOURCES="${ROOT}/hack/worker/gitops-tk-resources.yaml"

BUILD_KRATIX_IMAGES=false
RECREATE=false
GIT_REPO=false
LOCAL_IMAGES_DIR=""
VERSION=${VERSION:-"$(cd $ROOT; git branch --show-current)"}
DOCKER_BUILDKIT="${DOCKER_BUILDKIT:-1}"

WAIT_TIMEOUT="180s"
KIND_IMAGE="${KIND_IMAGE:-"kindest/node:v1.24.7"}"

usage() {
    echo -e "Usage: quick-start.sh [--help] [--recreate] [--local] [--git] [--local-images <location>]"
    echo -e "\t--help, -h            Prints this message"
    echo -e "\t--recreate, -r        Deletes pre-existing KinD Clusters"
    echo -e "\t--local, -l           Build and load Kratix images to KinD cache"
    echo -e "\t--local-images, -i    Load container images from a local directory into the KinD clusters"
    echo -e "\t--git, -g             Use Gitea as local repository in place of default local MinIO"
    exit "${1:-0}"
}

load_options() {
    for arg in "$@"; do
      shift
      case "$arg" in
        '--help')           set -- "$@" '-h'   ;;
        '--recreate')       set -- "$@" '-r'   ;;
        '--local')          set -- "$@" '-l'   ;;
        '--git')            set -- "$@" '-g'   ;;
        '--local-images')   set -- "$@" '-i'   ;;
        *)                  set -- "$@" "$arg" ;;
      esac
    done

    OPTIND=1
    while getopts "hrlgi:" opt
    do
      case "$opt" in
        'r') RECREATE=true ;;
        'h') usage ;;
        'l') BUILD_KRATIX_IMAGES=true ;;
        'i') LOCAL_IMAGES_DIR=${OPTARG} ;;
        'g') GIT_REPO=true;;
        *) usage 1 ;;
      esac
    done
    shift $(expr $OPTIND - 1)

    # Always build local images when running from `dev`
    if [ "${VERSION}" != "main" ]; then
        VERSION="dev"
    fi
    if [ "${VERSION}" = "dev" ]; then
        BUILD_KRATIX_IMAGES=true
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

_build_kratix_image() {
    docker_org=syntasso
    if ${KRATIX_DEVELOPER:-false}; then
        docker_org=syntassodev
    fi
    docker build --tag $docker_org/kratix-platform:${VERSION} --quiet --file ${ROOT}/Dockerfile ${ROOT} &&
    kind load docker-image $docker_org/kratix-platform:${VERSION} --name platform
}

_build_work_creator_image() {
    docker_org=syntasso
    if ${KRATIX_DEVELOPER:-false}; then
        docker_org=syntassodev
    fi
    docker build --tag $docker_org/kratix-platform-pipeline-adapter:${VERSION} --quiet --file ${ROOT}/Dockerfile.pipeline-adapter ${ROOT} &&
    kind load docker-image $docker_org/kratix-platform-pipeline-adapter:${VERSION} --name platform
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

patch_repository() {
    if ${GIT_REPO}; then
        sed "s/^\(.*--repository-type=\).*$/\1git/g"
    else
        sed "s/^\(.*--repository-type=\).*$/\1s3/g"
    fi
}

patch_image() {
    if ${KRATIX_DEVELOPER:-false}; then
        sed "s_syntasso/kratix_syntassodev/kratix_g"
    else
        cat
    fi
}

setup_platform_cluster() {
    if ${GIT_REPO}; then
        kubectl --context kind-platform apply --filename "${ROOT}/hack/platform/gitea-install.yaml"
    else
        kubectl --context kind-platform apply --filename "${MINIO_INSTALL}"
    fi
    cat ${KRATIX_DISTRIBUTION} | patch_repository | patch_image | kubectl --context kind-platform apply --filename -
}

setup_worker_cluster() {
    kubectl --context kind-platform apply --filename "${PLATFORM_WORKER}"

    install_gitops kind-worker worker-cluster-1
}

wait_for_gitea() {
    kubectl wait pod --context kind-platform -n gitea --selector app=gitea --for=condition=ready ${opts}
}

wait_for_minio() {
    kubectl wait pod --context kind-platform -n kratix-platform-system --selector run=minio --for=condition=ready ${opts}
}

wait_for_local_repository() {
    local timeout_flag="${1:-""}"
    opts=""
    if [ -z "${timeout_flag}" ]; then
        opts="--timeout=${WAIT_TIMEOUT}"
    fi
    if ${GIT_REPO}; then
        wait_for_gitea
    else
        wait_for_minio
    fi
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

load_kind_image() {
    pushd "${LOCAL_IMAGES_DIR}" > /dev/null
    kind_image=$(ls kindest*.tar)
    load_output=$(docker load --input ${kind_image} | tail -1)

    if [[ "${load_output}" =~ "Loaded image ID" ]]; then
        image_id=$(echo "${load_output}" | cut -d":" -f 3 | tr -d " ")
        image_tag="$(echo "${kind_image%.tar}" | sed "s/__/\//g")"
        docker tag "$image_id" "$image_tag"
    fi
    popd > /dev/null
}

load_images() {
    ps_ids=()
    local cluster="$1"
    pushd ${LOCAL_IMAGES_DIR} > /dev/null
    for image_tar in $(ls *.tar | grep -v kindest); do
        kind load image-archive --name "$cluster" "$image_tar" &
        ps_ids+=("$!")
    done
    popd > /dev/null

    for ps_id in "${ps_ids[@]}"; do
        wait $ps_id
    done
}

install_kratix() {
    verify_prerequisites

    if [ -d "${LOCAL_IMAGES_DIR}" ]; then
        log -n "Loading KinD images... "
        if ! run load_kind_image; then
            error "Failed to load KinD image"
            exit 1;
        fi
    fi

    log -n "Creating platform cluster..."
    if ! run kind create cluster --name platform --image $KIND_IMAGE \
        --config ${ROOT}/hack/platform/kind-platform-config.yaml
    then
        error "Could not create platform cluster"
        exit 1
    fi

    if ${BUILD_KRATIX_IMAGES}; then
        build_and_load_local_images
    fi

    if [ -d "${LOCAL_IMAGES_DIR}" ]; then
        log -n "Loading images in platform cluster..."
        if ! run load_images platform; then
            error "Failed to load images in platform cluster"
            exit 1;
        fi
    fi

    log -n "Setting up platform cluster..."
    if ! run setup_platform_cluster; then
        error " failed"
        exit 1
    fi

    log -n "Creating worker cluster..."
    if ! run kind create cluster --name worker --image $KIND_IMAGE \
        --config ${ROOT}/hack/worker/kind-worker-config.yaml
    then
        error "Could not create worker cluster"
        exit 1
    fi

    log -n "Waiting for local repository to be running..."
    if ! SUPRESS_OUTPUT=true run wait_for_local_repository; then
        log "\n\nIt's taking longer than usual for the local repository to start."
        log "You can check the platform pods to ensure there are no errors."
        log "This script will continue to wait for the local repository to come up. You can kill it with $(info "CTRL+C.")"
        log -n "\nWaiting for the local repository to be running... "
        run wait_for_local_repository --no-timeout
    fi

    if [ -d "${LOCAL_IMAGES_DIR}" ]; then
        log -n "Loading images in worker cluster..."
        if ! run load_images worker; then
            error "Failed to load images in worker cluster"
            exit 1;
        fi
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
        log -n "\nWaiting for local repository to be running... "
        run wait_for_namespace --no-timeout
    fi

    kubectl config use-context kind-platform >/dev/null

    success "Kratix installation is complete!"

    if ${KRATIX_DEVELOPER:-false}; then
        echo "If you are following the docs available at kratix.io, make sure to set the following environment variables:"
        echo "export PLATFORM=kind-platform"
        echo "export WORKER=kind-worker"
    fi

}

main() {
    load_options $@
    install_kratix
}


if [ "$0" = "${BASH_SOURCE[0]}" ]; then
    main $@
fi
