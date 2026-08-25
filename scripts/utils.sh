RED=$'\033[1;31m'
GREEN=$'\033[1;32m'
BLUE=$'\033[1;34m'
NOCOLOR=$'\033[0m'
VERBOSE=false
ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )

log() {
    echo -e $@
}

success() {
    echo -e "${GREEN}$@${NOCOLOR}"
}

success_mark() {
    success " ✓"
}

error_mark() {
    error " ✗"
}

info() {
    echo -e "${BLUE}$@${NOCOLOR}"
}

error() {
    echo -e "${RED}$@${NOCOLOR}"
}

platform_destination_ip() {
    local platform_cluster_name="${1:-platform}"
    docker inspect ${platform_cluster_name}-control-plane | yq ".[0].NetworkSettings.Networks.kind.IPAddress"
}

generate_gitea_credentials() {
    giteabin="${ROOT}/bin/gitea"
    if which gitea > /dev/null; then
        giteabin="$(which gitea)"
    fi

    if [ ! -f "${giteabin}" ]; then
        error "gitea cli not found; run 'make gitea-cli' to download it"
        exit 1
    fi
    local context="${1:-kind-platform}"
    platform_cluster_name=${context#*-}
    $giteabin cert --host "$(platform_destination_ip ${platform_cluster_name})" --ca

    kubectl create namespace gitea --context ${context} || true

    kubectl create secret generic gitea-credentials \
        --context "${context}" \
        --from-file=caFile=${ROOT}/cert.pem \
        --from-file=privateKey=${ROOT}/key.pem \
        --from-literal=username="gitea_admin" \
        --from-literal=password="r8sA8CPHD9!bt6d" \
        --namespace=gitea \
        --dry-run=client -o yaml | kubectl apply --context ${context} -f -

    kubectl create secret generic gitea-credentials \
        --context "${context}" \
        --from-file=caFile=${ROOT}/cert.pem \
        --from-file=privateKey=${ROOT}/key.pem \
        --from-literal=username="gitea_admin" \
        --from-literal=password="r8sA8CPHD9!bt6d" \
        --namespace=default \
        --dry-run=client -o yaml | kubectl apply --context ${context} -f -

    kubectl create namespace flux-system --context ${context} || true
    kubectl create secret generic gitea-credentials \
        --context "${context}" \
        --from-file=caFile=${ROOT}/cert.pem \
        --from-file=privateKey=${ROOT}/key.pem \
        --from-literal=username="gitea_admin" \
        --from-literal=password="r8sA8CPHD9!bt6d" \
        --namespace=flux-system \
        --dry-run=client -o yaml | kubectl apply --context ${context} -f -

    rm ${ROOT}/cert.pem ${ROOT}/key.pem
}

copy_gitea_credentials() {
    local fromCtx="${1:-kind-platform}"
    local toCtx="${2:-kind-worker}"
    local targetNamespace="${3:-default}"

    set +e
    kubectl create namespace ${targetNamespace} --context ${toCtx} || true
    set -e
    kubectl get secret gitea-credentials --context ${fromCtx} -n gitea -o yaml | \
        yq 'del(.metadata["namespace","creationTimestamp","resourceVersion","selfLink","uid"])' | \
        kubectl apply --namespace ${targetNamespace} --context ${toCtx} -f -
}

# run executes a command, showing a spinner while it works and its output if it
# fails. There is deliberately no way to silence it: a command whose output is
# hidden is a command you cannot debug from a CI log.
#
# In CI the output is streamed live rather than buffered, so a command that hangs
# still shows progress: buffered output prints nothing until the command exits.
# There is no spinner in CI anyway, so there is nothing to interleave with.
run() {
    local stream=false
    if [ -n "${CI:-}" ] && [ "${CI}" != "false" ]; then
        stream=true
    fi

    stdout="$(mktemp)"
    stderr="$(mktemp)"
    trap "rm $stdout $stderr" EXIT

    if ${stream}; then
        # tee to the terminal as well as the capture files, so a long-running or
        # hanging command shows progress as it happens.
        { $@ 2>&1 | tee "$stdout"; exit "${PIPESTATUS[0]}"; } & pid=$!
    else
        $@ > $stdout 2>$stderr & pid=$! # Process Id of the previous running command
    fi

    spin='-\|/'

    echo -n "  "
    i=0
    while kill -0 $pid 2>/dev/null
    do
        i=$(( (i+1) %4 ))
        [[ -z ${CIRCLECI:-""} ]] && ! ${stream} && echo -ne "\b${spin:$i:1}"
        sleep .1
    done
    echo -ne "\b\b"

    wait $pid
    exit_code="$?"

    if [ "$exit_code" -eq "0" ]; then
        success_mark
    else
        error_mark
        # When streaming, the output has already been shown; repeating it just
        # doubles the noise around the actual error.
        if ! ${stream} && [[ -s "$stdout" || -s "$stderr" ]]; then
            info "Combined output:"
            cat $stdout $stderr
            log
        fi
    fi

    return $exit_code
}

commit_sha() {
    git rev-list --no-merges -n 1 HEAD
}
