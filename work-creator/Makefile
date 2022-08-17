# Image URL to use all building/pushing image targets
WC_IMG ?= syntasso/kratix-platform-work-creator:dev
# Enable buildkit for docker
DOCKER_BUILDKIT ?= 1

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
	mkdir -p ${ENVTEST_ASSETS_DIR}
	test -f ${ENVTEST_ASSETS_DIR}/setup-envtest.sh || curl -sSLo ${ENVTEST_ASSETS_DIR}/setup-envtest.sh https://raw.githubusercontent.com/kubernetes-sigs/controller-runtime/v0.7.2/hack/setup-envtest.sh
	source ${ENVTEST_ASSETS_DIR}/setup-envtest.sh;  ENVTEST_K8S_VERSION=1.24.2 fetch_envtest_tools $(ENVTEST_ASSETS_DIR); setup_envtest_env $(ENVTEST_ASSETS_DIR); CK_GINKGO_DEPRECATIONS=1.16.4 go run github.com/onsi/ginkgo/ginkgo ./test/integration/ -r  --coverprofile cover.out

kind-load-image: docker-build ## Load locally built image into KinD, use export WC_IMG=syntasso/kratix-platform-work-creator:dev
	kind load docker-image ${WC_IMG} --name platform

##@ Build

docker-build: ## Build docker image with work-creator binary
	docker build -f ../DockerfileWorkCreator --tag ${WC_IMG} ../

docker-build-and-push: ## Push multi-arch docker image with the manager.
	if ! docker buildx ls | grep -q "work-creator-image-builder"; then \
		docker buildx create --name work-creator-image-builder; \
	fi;
	docker buildx build --builder work-creator-image-builder --push --platform linux/arm64,linux/amd64 --file ../DockerfileWorkCreator --tag ${WC_IMG} ../