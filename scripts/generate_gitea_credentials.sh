#!/usr/bin/env bash

ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )
source "${ROOT}/scripts/utils.sh"

info "Generating Gitea credentials and namespace..."
generate_gitea_credentials

success "Gitea credentials generated"
