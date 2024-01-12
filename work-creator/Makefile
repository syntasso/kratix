# Image URL to use all building/pushing image targets
WC_IMG_NAME ?= syntasso/kratix-platform-pipeline-adapter
WC_IMG_VERSION ?= dev
WC_IMG_TAG ?= ${WC_IMG_NAME}:${WC_IMG_VERSION}
WC_IMG_MIRROR ?= syntassodev/kratix-platform-pipeline-adapter:dev
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
	go run ${GINKGO} ./pipeline/ -r  --coverprofile cover.out

kind-load-image: docker-build ## Load locally built image into KinD, use export WC_IMG=syntasso/kratix-platform-pipeline-adapter:dev
	kind load docker-image ${WC_IMG_TAG} --name platform
	kind load docker-image ${WC_IMG_MIRROR} --name platform

##@ Build

docker-build: ## Build docker image with work-creator binary
	docker build -f ../Dockerfile.pipeline-adapter --tag ${WC_IMG_TAG} --tag ${WC_IMG_NAME}:latest ../
	docker build -f ../Dockerfile.pipeline-adapter --tag ${WC_IMG_MIRROR} ../

docker-build-and-push: ## Push multi-arch docker image with the manager.
	if ! docker buildx ls | grep -q "work-creator-image-builder"; then \
		docker buildx create --name work-creator-image-builder; \
	fi;
	docker buildx build --builder work-creator-image-builder --push --platform linux/arm64,linux/amd64 --file ../Dockerfile.pipeline-adapter --tag ${WC_IMG_TAG} --tag ${WC_IMG_NAME}:latest ../
	docker buildx build --builder work-creator-image-builder --push --platform linux/arm64,linux/amd64 --file ../Dockerfile.pipeline-adapter --tag ${WC_IMG_MIRROR} ../
