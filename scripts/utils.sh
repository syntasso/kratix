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
    docker inspect platform-control-plane | grep '"IPAddress": "172' | awk -F '"' '{print $4}'
}

generate_gitea_credentials() {
    if [ ! -f "${ROOT}/bin/gitea" ]; then
        error "gitea cli not found; run `make gitea-cli` to download it"
        exit 1
    fi
    ${ROOT}/bin/gitea cert --host $(platform_destination_ip) --ca

    kubectl create secret generic gitea-credentials \
        --context kind-platform \
        --from-file=caFile=${ROOT}/cert.pem \
        --from-file=privateKey=${ROOT}/key.pem \
        --from-literal=username="gitea_admin" \
        --from-literal=password="r8sA8CPHD9!bt6d" \
        --namespace=gitea

    kubectl create secret generic gitea-credentials \
        --context kind-platform \
        --from-file=caFile=${ROOT}/cert.pem \
        --from-file=privateKey=${ROOT}/key.pem \
        --from-literal=username="gitea_admin" \
        --from-literal=password="r8sA8CPHD9!bt6d" \
        --namespace=default

    rm ${ROOT}/cert.pem ${ROOT}/key.pem
}


run() {
    SUPRESS_OUTPUT=${SUPRESS_OUTPUT:-false}
    stdout="$(mktemp)"
    stderr="$(mktemp)"
    trap "rm $stdout $stderr" EXIT

    $@ > $stdout 2>$stderr & pid=$! # Process Id of the previous running command

    spin='-\|/'

    echo -n "  "
    i=0
    while kill -0 $pid 2>/dev/null
    do
        i=$(( (i+1) %4 ))
        [[ -z ${CIRCLECI:-""} ]] && echo -ne "\b${spin:$i:1}"
        sleep .1
    done
    echo -ne "\b\b"

    wait $pid
    exit_code="$?"

    if [ "$exit_code" -eq "0" ]; then
        success_mark
    else
        if ! ${SUPRESS_OUTPUT}; then
            error_mark
            if [[ -s "$stdout" || -s "$stderr" ]]; then
                info "Combined output:"
                cat $stdout $stderr
                log
            fi
        fi
    fi

    return $exit_code
}

commit_sha() {
    git rev-list --no-merges -n 1 HEAD
}
