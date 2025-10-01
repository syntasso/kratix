# Image URL to use all building/pushing image targets
PIPELINE_ADAPTER_IMG_NAME ?= docker.io/syntasso/kratix-platform-pipeline-adapter
PIPELINE_ADAPTER_IMG_VERSION ?= dev
PIPELINE_ADAPTER_IMG_TAG ?= ${PIPELINE_ADAPTER_IMG_NAME}:${PIPELINE_ADAPTER_IMG_VERSION}
PIPELINE_ADAPTER_IMG_MIRROR ?= docker.io/syntassodev/kratix-platform-pipeline-adapter:dev
PIPELINE_ADAPTER_QUICKSTART_TAG ?= docker.io/syntasso/kratix-platform-pipeline-adapter-quickstart:latest
# Enable buildkit for docker
DOCKER_BUILDKIT ?= 1

GINKGO = github.com/onsi/ginkgo/v2/ginkgo

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

ENVTEST_ASSETS_DIR=$(shell pwd)/../testbin
.PHONY: test
test: ## Run tests.
	go run ${GINKGO} ./lib/ -r  --coverprofile cover.out
