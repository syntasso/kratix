# Version to use for building/releasing artifacts
VERSION ?= dev
# Image URL to use all building/pushing image targets
IMG_NAME ?= docker.io/syntasso/kratix-platform
IMG_VERSION ?= ${VERSION}
IMG_TAG ?= ${IMG_NAME}:${IMG_VERSION}
IMG_MIRROR ?= syntassodev/kratix-platform:${VERSION}
# Image URL to use for work creator image in promise_controller.go
WC_IMG_VERSION ?= ${VERSION}
WC_IMG ?= docker.io/syntasso/kratix-platform-pipeline-adapter:${WC_IMG_VERSION}
WC_IMG_MIRROR ?= syntassodev/kratix-platform-pipeline-adapter:${VERSION}
# Version of the worker-resource-builder binary to build and release
WRB_VERSION ?= 0.0.0
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)
CRD_OPTIONS ?= "crd:ignoreUnexportedFields=true"
# Enable buildkit for docker
DOCKER_BUILDKIT ?= 1
export DOCKER_BUILDKIT

# Recreate Kind Clusters by default
RECREATE ?= true
export RECREATE

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

GINKGO = github.com/onsi/ginkgo/v2/ginkgo

# Setting SHELL to bash allows bash commands to be executed by recipes.
# This is a requirement for 'setup-envtest.sh' in the test target.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Kubebuilder

manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases

generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."
	go generate ./...

##@ Environment

teardown: ## Delete all Kratix resources from the Platform cluster
	./scripts/teardown

fast-quick-start: teardown ## Install Kratix without recreating the local clusters
	RECREATE=false make quick-start

quick-start: gitea-cli generate distribution ## Recreates the clusters and install Kratix
	VERSION=dev DOCKER_BUILDKIT=1 ./scripts/quick-start.sh --local --git-and-minio

prepare-platform-as-destination: ## Installs flux onto platform cluster and registers as a destination
	./scripts/register-destination --with-label environment=platform --context kind-platform --name platform-cluster

single-cluster: distribution ## Deploys Kratix on a single cluster
	VERSION=dev DOCKER_BUILDKIT=1 ./scripts/quick-start.sh --recreate --local --single-cluster

dev-env: quick-start prepare-platform-as-destination ## Quick-start + prepare-platform-as-destination

install-cert-manager: ## Install cert-manager on the platform cluster; used in the helm test
	kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.0/cert-manager.yaml
	kubectl wait --for condition=available -n cert-manager deployment/cert-manager --timeout 120s
	kubectl wait --for condition=available -n cert-manager deployment/cert-manager-cainjector --timeout 120s
	kubectl wait --for condition=available -n cert-manager deployment/cert-manager-webhook --timeout 120s

##@ Container Images

kind-load-image: docker-build ## Load locally built image into KinD
	kind load docker-image ${IMG_TAG} --name platform
	kind load docker-image ${IMG_MIRROR} --name platform

build-and-load-kratix: kind-load-image ## Build kratix container image and reloads
	kubectl rollout restart deployment -n kratix-platform-system -l control-plane=controller-manager

build-and-load-work-creator: ## Build work-creator container image and reloads
	WC_IMG_VERSION=${WC_IMG_VERSION} WC_IMG_MIRROR=${WC_IMG_MIRROR} make -C work-creator kind-load-image

##@ Build

# Generate manifests for distributed installation
build: generate fmt vet ## Build manager binary.
	CGO_ENABLED=0 go build -o bin/manager main.go

run: manifests generate fmt vet ## Run a controller from your host.
	go run ./main.go

debug-run: manifests generate fmt vet ## Run a controller in debug mode from your host
	dlv --listen=:2345 --headless=true --api-version=2 --accept-multiclient debug ./main.go

docker-build: ## Build docker image with the manager.
	docker build -t ${IMG_TAG} -t ${IMG_NAME}:latest .
	docker tag ${IMG_TAG} ${IMG_MIRROR}

docker-build-and-push: ## Push multi-arch docker image with the manager.
	if ! docker buildx ls | grep -q "kratix-image-builder"; then \
		docker buildx create --name kratix-image-builder; \
	fi;
	docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t ${IMG_TAG} -t ${IMG_NAME}:latest .
	docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t ${IMG_MIRROR} .

build-and-push-work-creator: ## Build and push the Work Creator image
	WC_IMG_VERSION=${WC_IMG_VERSION} WC_IMG_MIRROR=${WC_IMG_MIRROR} $(MAKE) -C work-creator docker-build-and-push

# If not installed, use: go install github.com/goreleaser/goreleaser@latest
build-worker-resource-builder-binary: ## Uses the goreleaser config to generate binaries
	WRB_VERSION=${WRB_VERSION} WRB_ON_BRANCH=${WRB_ON_BRANCH} ./scripts/release-worker-resource-builder

##@ Deployment

install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG_TAG} \
		&& sed 's#LATEST_WC_IMAGE#${WC_IMG}#g' patches/wc_image_value.yaml.template > patches/wc_image_value.yaml
	$(KUSTOMIZE) build config/default | kubectl apply -f -

distribution: manifests kustomize ## Create a deployment manifest in /distribution/kratix.yaml
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG_TAG} \
		&& sed 's#LATEST_WC_IMAGE#${WC_IMG}#g' patches/wc_image_value.yaml.template > patches/wc_image_value.yaml
	mkdir -p distribution
	$(KUSTOMIZE) build config/default --output distribution/kratix.yaml

release: distribution docker-build-and-push build-and-push-work-creator ## Create a release. Set VERSION env var to "vX.Y.Z-n".

undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete -f -

CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	$(eval CONTROLLER_GEN_VERSION := v0.16.5)
	$(eval CURRENT_CONTROLLER_GEN_VERSION := $(shell $(CONTROLLER_GEN) --version 2>/dev/null | grep -oE 'v[0-9]+.[0-9]+.[0-9]+' || echo "none"))
	@if [ "$(CURRENT_CONTROLLER_GEN_VERSION)" != "$(CONTROLLER_GEN_VERSION)" ]; then \
		echo "Warning: Controller-gen version $(CURRENT_CONTROLLER_GEN_VERSION) does not match desired version $(CONTROLLER_GEN_VERSION)"; \
		echo "Installing/updating to version $(CONTROLLER_GEN_VERSION)..."; \
		GOBIN=$(shell pwd)/bin go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION); \
	fi

KUSTOMIZE = $(shell pwd)/bin/kustomize
kustomize: ## Download kustomize locally if necessary.
	$(call go-get-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v4@v4.5.5)

# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-get-tool
@[ -f $(1) ] || { \
set -e ;\
TMP_DIR=$$(mktemp -d) ;\
cd $$TMP_DIR ;\
go mod init tmp ;\
echo "Downloading $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
rm -rf $$TMP_DIR ;\
}
endef

GITEA_PLATFORM=$(shell uname -s | tr '[:upper:]' '[:lower:]')
ifeq ($(GITEA_PLATFORM),darwin)
	GITEA_PLATFORM=darwin-10.12
endif
ARCH=$(shell uname -m)
ifeq ($(GITEA_PLATFORM),linux)
	ARCH=$(shell dpkg --print-architecture)
endif

define get-gitea-cli
@[ -f $(PROJECT_DIR)/bin/gitea ] || { \
curl --silent --output $(PROJECT_DIR)/bin/gitea https://dl.gitea.com/gitea/1.21.10/gitea-1.21.10-$(GITEA_PLATFORM)-$(ARCH); \
chmod +x $(PROJECT_DIR)/bin/gitea; \
}
endef
gitea-cli:
	mkdir -p bin
	$(call get-gitea-cli)

.PHONY: list
list:
	@$(MAKE) -pRrq -f $(lastword $(MAKEFILE_LIST)) : 2>/dev/null | awk -v RS= -F: '/^# File/,/^# Finished Make data base/ {if ($$1 !~ "^[#.]") {print $$1}}' | sort | egrep -v -e '^[^[:alnum:]]' -e '^$@$$'

##@ Tests
system-test: ## Recreate the clusters and run system tests
	make quick-start
	make -j4 run-system-test

fast-system-test: fast-quick-start ## Run the system tests without recreating the clusters
	make -j4 run-system-test

# kubebuilder-tools does not yet support darwin/arm64. The following is a workaround (see https://github.com/kubernetes-sigs/controller-runtime/issues/1657)
ARCH_FLAG =
ifeq ($(shell uname -sm),Darwin arm64)
	ARCH_FLAG = --arch=amd64
endif

.PHONY: test
test: manifests generate fmt vet ## Run unit tests.
	go run ${GINKGO} ${GINKGO_FLAGS} -r --coverprofile cover.out --skip-package=system

.PHONY: run-system-test
run-system-test: fmt vet build-and-load-bash
	PLATFORM_DESTINATION_IP=`docker inspect platform-control-plane | grep '"IPAddress": "172' | awk -F '"' '{print $$4}'` go run ${GINKGO} ${GINKGO_FLAGS} -p --output-interceptor-mode=none ./test/system/  --coverprofile cover.out

fmt: ## Run go fmt against code.
	go fmt ./...

vet: ## Run go vet against code.
	go vet ./...

build-and-load-bash: # Build and load all test pipeline images
	docker build --tag syntassodev/bash-promise:dev1 ./test/system/assets/bash-promise
	kind load docker-image syntassodev/bash-promise:dev1 --name platform

build-and-push-bash:
	docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 --tag syntassodev/bash-promise:dev1 ./test/system/assets/bash-promise

##@ Deprecated: will be deleted soon

# build-and-reload-kratix is deprecated in favor of build-and-load-kratix
build-and-reload-kratix: DEPRECATED ## Build and reload Kratix on local KinD cluster
	make kind-load-image
	kubectl rollout restart deployment -n kratix-platform-system kratix-platform-controller-manager

.SILENT: DEPRECATED
DEPRECATED:
	@echo
	@echo [WARN] Target has been deprecated. See Makefile for more information.
	@echo
	read -p 'Press any key to continue...'
	@echo
