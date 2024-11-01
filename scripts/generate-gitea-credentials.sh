#!/usr/bin/env bash

platform_destination_ip() {
    docker inspect platform-control-plane | yq ".[0].NetworkSettings.Networks.kind.IPAddress"
}

generate_gitea_credentials() {
    giteabin=$(which gitea)
    if [ -z "$giteabin" ]; then
        echo "gitea cli not found; download here: https://docs.gitea.com/installation/install-from-binary" > /dev/stderr
        exit 1
    fi
    local context="${1:-kind-platform}"
    $giteabin cert --host "$(platform_destination_ip)" --ca

    kubectl create namespace gitea --context "${context}" || true

    tmpdir=$(mktemp -d)

    kubectl create secret generic gitea-credentials \
        --context "${context}" \
        --from-file=caFile="${tmpdir}/cert.pem" \
        --from-file=privateKey=${tmpdir}/key.pem \
        --from-literal=username="gitea_admin" \
        --from-literal=password="r8sA8CPHD9!bt6d" \
        --namespace=gitea \
        --dry-run=client -o yaml | kubectl apply --context "${context}" -f -

    kubectl create secret generic gitea-credentials \
        --context "${context}" \
        --from-file=caFile=${tmpdir}/cert.pem \
        --from-file=privateKey=${tmpdir}/key.pem \
        --from-literal=username="gitea_admin" \
        --from-literal=password="r8sA8CPHD9!bt6d" \
        --namespace=default \
        --dry-run=client -o yaml | kubectl apply --context "${context}" -f -

    rm ${tmpdir}/cert.pem ${tmpdir}/key.pem
    rmdir ${tmpdir}
}

echo "Generating Gitea credentials and namespace..."
generate_gitea_credentials kind-platform

echo "Gitea credentials generated"
