# Version to use for building/releasing artifacts
VERSION ?= dev
# Image URL to use all building/pushing image targets
IMG_NAME ?= docker.io/syntasso/kratix-platform
QUICKSTART_TAG ?= docker.io/syntasso/kratix-platform-quickstart:latest
IMG_VERSION ?= ${VERSION}
IMG_TAG ?= ${IMG_NAME}:${IMG_VERSION}
IMG_MIRROR ?= syntassodev/kratix-platform:${VERSION}
# Image URL to use for work creator image in promise_controller.go
PIPELINE_ADAPTER_IMG_VERSION ?= ${VERSION}
PIPELINE_ADAPTER_IMG ?= docker.io/syntasso/kratix-platform-pipeline-adapter:${PIPELINE_ADAPTER_IMG_VERSION}
PIPELINE_ADAPTER_IMG_MIRROR ?= syntassodev/kratix-platform-pipeline-adapter:${VERSION}
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

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))

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

generate-fakes:
	go generate ./...

##@ Environment

teardown: ## Delete all Kratix resources from the Platform cluster
	./scripts/teardown

fast-quick-start: teardown ## Install Kratix without recreating the local clusters
	RECREATE=false make quick-start

quick-start: generate distribution ## Recreates the clusters and install Kratix
	VERSION=dev DOCKER_BUILDKIT=1 ./scripts/quick-start.sh --recreate --local --git-and-minio

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
	PIPELINE_ADAPTER_IMG_VERSION=${PIPELINE_ADAPTER_IMG_VERSION} PIPELINE_ADAPTER_IMG_MIRROR=${PIPELINE_ADAPTER_IMG_MIRROR} make -C work-creator kind-load-image

##@ Build

# Generate manifests for distributed installation
build: generate fmt vet ## Build manager binary.
	CGO_ENABLED=0 go build -o bin/manager cmd/main.go

run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

debug-run: manifests generate fmt vet ## Run a controller in debug mode from your host
	dlv --listen=:2345 --headless=true --api-version=2 --accept-multiclient debug ./main.go

docker-build: ## Build docker image with the manager.
	docker build -t ${QUICKSTART_TAG} -t ${IMG_MIRROR} -t ${IMG_TAG} -t ${IMG_NAME}:latest .

docker-build-and-push: ## Push multi-arch docker image with the manager.
	if ! docker buildx ls | grep -q "kratix-image-builder"; then \
		docker buildx create --name kratix-image-builder; \
	fi;
	docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t ${QUICKSTART_TAG} -t ${IMG_TAG} -t ${IMG_NAME}:latest .
	docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t ${IMG_MIRROR} .

build-and-push-work-creator: ## Build and push the Work Creator image
	PIPELINE_ADAPTER_IMG_VERSION=${PIPELINE_ADAPTER_IMG_VERSION} PIPELINE_ADAPTER_IMG_MIRROR=${PIPELINE_ADAPTER_IMG_MIRROR} $(MAKE) -C work-creator docker-build-and-push

# If not installed, use: go install github.com/goreleaser/goreleaser@latest
build-worker-resource-builder-binary: ## Uses the goreleaser config to generate binaries
	WRB_VERSION=${WRB_VERSION} WRB_ON_BRANCH=${WRB_ON_BRANCH} ./scripts/release-worker-resource-builder

##@ Deployment

install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG_TAG}
	echo "PIPELINE_ADAPTER_IMG=${PIPELINE_ADAPTER_IMG}" > config/manager/pipeline-adapter-config.properties
	$(KUSTOMIZE) build config/default | kubectl apply -f -

distribution: manifests kustomize ## Create a deployment manifest in /distribution/kratix.yaml
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG_TAG}
	mkdir -p distribution
	echo "PIPELINE_ADAPTER_IMG=${PIPELINE_ADAPTER_IMG}" > config/manager/pipeline-adapter-config.properties
	$(KUSTOMIZE) build config/default --output distribution/kratix.yaml

release: distribution docker-build-and-push build-and-push-work-creator ## Create a release. Set VERSION env var to "vX.Y.Z-n".

undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete -f -

# go-get-tool will 'go get' any package $2 and install it to $1.
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

PLATFORM=$(shell uname -s | tr '[:upper:]' '[:lower:]')
GITEA_PLATFORM=$(PLATFORM)
ifeq ($(GITEA_PLATFORM),darwin)
	GITEA_PLATFORM=darwin-10.12
endif
ARCH=$(shell uname -m)
ifeq ($(PLATFORM),linux)
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

define get-mc-cli
@[ -f $(PROJECT_DIR)/bin/mc ] || { \
curl --silent --output $(PROJECT_DIR)/bin/mc https://dl.min.io/aistor/mc/release/$(PLATFORM)-$(ARCH)/mc; \
chmod +x $(PROJECT_DIR)/bin/mc; \
}
endef
minio-cli:
	mkdir -p bin
	$(call get-mc-cli)

.PHONY: list
list:
	@$(MAKE) -pRrq -f $(lastword $(MAKEFILE_LIST)) : 2>/dev/null | awk -v RS= -F: '/^# File/,/^# Finished Make data base/ {if ($$1 !~ "^[#.]") {print $$1}}' | sort | egrep -v -e '^[^[:alnum:]]' -e '^$@$$'

##@ Tests
system-test: generate distribution build-and-load-work-creator build-and-load-kratix ## Recreate the clusters and run system tests
	#make quick-start
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
	go run ${GINKGO} ${GINKGO_FLAGS} -r --coverprofile cover.out --skip-package=system,core

PLATFORM_CLUSTER_NAME ?= platform
core-test: quick-start run-core-test

run-core-test:
	cd test/core/assets/workflows/ && docker build -t syntasso/test-bundle-image:v0.1.0 .
	kind load docker-image syntasso/test-bundle-image:v0.1.0 --name ${PLATFORM_CLUSTER_NAME}
	go run ${GINKGO} ${GINKGO_FLAGS} test/core/

build-and-push-core-test-image: # for non-kind environment where images cannot be loaded
	cd test/core/assets/workflows/ && docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t syntasso/test-bundle-image:v0.1.0 -t syntasso/test-bundle-image:v0.1.0 .

.PHONY: run-system-test
run-system-test: fmt vet
	PATH="$(PROJECT_DIR)/bin:${PATH}" go run ${GINKGO} ${GINKGO_FLAGS} -p --output-interceptor-mode=none ./test/system/  --coverprofile cover.out

fmt: ## Run go fmt against code.
	go fmt ./...

vet: ## Run go vet against code.
	go vet ./...

lint: golangci-lint # Lint with required config
	$(GOLANGCI_LINT) run --config=.golangci-required.yml

lint-all: # Lint with full config
	golangci-lint run --config=.golangci.yml

##@ act targets to run GH Action jobs locally

ACT_JOB = act -j '$(1)' --rm -s GITHUB_TOKEN="$(shell gh auth token)"
act-test-and-lint:
	$(call ACT_JOB,unit-tests-and-lint)

act-integration-test:
	$(call ACT_JOB,integration-test)

act-system-test:
	$(call ACT_JOB,system-test)

act-run-all: act-system-test act-integration-test

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


##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
GINKGO = github.com/onsi/ginkgo/v2/ginkgo
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.6.0
CONTROLLER_TOOLS_VERSION ?= v0.16.5
GOLANGCI_LINT_VERSION ?= v1.63.4

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))


.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) || true ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $(1)-$(3) $(1)
endef
