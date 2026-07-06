# Dev container for Kratix: Go toolchain + the CLIs the Tilt dev env needs.
# Docker access is provided by the docker-outside-of-docker feature (host socket),
# which is the reliable pattern for kind + devcontainers/Codespaces.
FROM golang:1.26-bookworm

# TARGETARCH is populated by BuildKit (amd64|arm64) so the image builds on both
# Intel and Apple Silicon hosts.
ARG TARGETARCH
ARG KIND_VERSION=v0.32.0
ARG CTLPTL_VERSION=0.9.4
ARG TILT_VERSION=0.37.5
ARG KO_VERSION=0.19.1
ARG KUBECTL_VERSION=v1.33.1
ARG YQ_VERSION=v4.53.3
ARG HELM_VERSION=v3.16.4

RUN apt-get update && apt-get install -y --no-install-recommends \
      curl git ca-certificates bash jq && \
    rm -rf /var/lib/apt/lists/*

# kubectl/kind/yq use amd64|arm64 asset names; tilt/ctlptl/ko use x86_64|arm64.
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) ALT=x86_64 ;; \
      arm64) ALT=arm64 ;; \
      *) echo "unsupported arch: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo /usr/local/bin/kubectl \
      "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl"; \
    chmod +x /usr/local/bin/kubectl; \
    curl -fsSLo /usr/local/bin/yq \
      "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_${TARGETARCH}"; \
    chmod +x /usr/local/bin/yq; \
    curl -fsSLo /usr/local/bin/kind \
      "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-${TARGETARCH}"; \
    chmod +x /usr/local/bin/kind; \
    curl -fsSL \
      "https://github.com/tilt-dev/ctlptl/releases/download/v${CTLPTL_VERSION}/ctlptl.${CTLPTL_VERSION}.linux.${ALT}.tar.gz" \
      | tar -xzC /usr/local/bin ctlptl; \
    curl -fsSL \
      "https://github.com/tilt-dev/tilt/releases/download/v${TILT_VERSION}/tilt.${TILT_VERSION}.linux.${ALT}.tar.gz" \
      | tar -xzC /usr/local/bin tilt; \
    curl -fsSL \
      "https://github.com/ko-build/ko/releases/download/v${KO_VERSION}/ko_${KO_VERSION}_Linux_${ALT}.tar.gz" \
      | tar -xzC /usr/local/bin ko; \
    curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash -s -- --version "${HELM_VERSION}"
