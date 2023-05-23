# Version to use for building/releasing artifacts
VERSION ?= dev
# Image URL to use all building/pushing image targets
IMG ?= syntasso/kratix-platform:${VERSION}
IMG_MIRROR ?= syntassodev/kratix-platform:${VERSION}
# Image URL to use for work creator image in promise_controller.go
WC_IMG ?= syntasso/kratix-platform-pipeline-adapter:${VERSION}
WC_IMG_MIRROR ?= syntassodev/kratix-platform-pipeline-adapter:${VERSION}
# Version of the worker-resource-builder binary to build and release
WRB_VERSION ?= 0.0.0
# Produce CRDs that work back to Kubernetes 1.11 (no version conversion)
CRD_OPTIONS ?= "crd:ignoreUnexportedFields=true"
# Enable buildkit for docker
DOCKER_BUILDKIT ?= 1
export DOCKER_BUILDKIT

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

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

##@ Development

manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) $(CRD_OPTIONS) rbac:roleName=manager-role webhook paths="./..." output:crd:artifacts:config=config/crd/bases

generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

fmt: ## Run go fmt against code.
	go fmt ./...

vet: ## Run go vet against code.
	go vet ./...

build-and-load-bash:
	docker build --tag syntassodev/bash-promise-test-c0:dev ./test/system/assets/bash-promise --build-arg CONTAINER_INDEX=0
	docker build --tag syntassodev/bash-promise-test-c1:dev ./test/system/assets/bash-promise --build-arg CONTAINER_INDEX=1
	kind load docker-image syntassodev/bash-promise-test-c0:dev --name platform
	kind load docker-image syntassodev/bash-promise-test-c1:dev --name platform

build-and-load-kratix: kind-load-image

build-and-load-worker-creator:
	WC_IMG=${WC_IMG} WC_IMG_MIRROR=${WC_IMG_MIRROR} make -C work-creator kind-load-image

load-pipeline-images:
	docker pull docker.io/bitnami/kubectl:1.20.10
	kind load docker-image docker.io/bitnami/kubectl:1.20.10 --name platform
	docker pull syntasso/knative-serving-pipeline:latest
	kind load docker-image syntasso/knative-serving-pipeline:latest --name platform
	docker pull syntasso/postgres-request-pipeline:latest
	kind load docker-image syntasso/postgres-request-pipeline:latest --name platform
	docker pull syntasso/paved-path-demo-request-pipeline:latest
	kind load docker-image syntasso/paved-path-demo-request-pipeline:latest --name platform


prepare-platform-cluster-as-worker: ## Installs flux onto platform cluster and registers as a worker
	./scripts/prepare-platform-cluster-as-worker.sh

install-minio: ## Install test Minio server
	kubectl --context kind-platform apply -f hack/platform/minio-install.yaml

install-gitea: ## Install test gitea server
	kubectl --context kind-platform apply -f hack/platform/gitea-install.yaml

install-flux-to-platform:
	kubectl apply -f ./hack/worker/gitops-tk-install.yaml
	kubectl wait --namespace flux-system --for=condition=Available deployment source-controller --timeout=120s
	kubectl wait --namespace flux-system --for=condition=Available deployment kustomize-controller --timeout=120s

system-test: generate fmt vet ## Run integrations tests.
	make distribution
	make quick-start
	make build-and-load-bash
	make install-flux-to-platform
	make ginkgo-system-test

ginkgo-system-test:
	CK_GINKGO_DEPRECATIONS=1.16.4 go run github.com/onsi/ginkgo/ginkgo ./test/system/  -r  --coverprofile cover.out

kind-load-image: docker-build ## Load locally built image into KinD, use export IMG=syntasso/kratix-platform:${VERSION}
	kind load docker-image ${IMG} --name platform
	kind load docker-image ${IMG_MIRROR} --name platform

quick-start: distribution
	VERSION=dev DOCKER_BUILDKIT=1 ./scripts/quick-start.sh --recreate --local --git-and-minio

single-cluster: distribution
	VERSION=dev DOCKER_BUILDKIT=1 ./scripts/quick-start.sh --recreate --local --single-cluster

dev-env: distribution quick-start prepare-platform-cluster-as-worker ## Tears down existing resources and sets up a local development environment

# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.23
# kubebuilder-tools does not yet support darwin/arm64. The following is a workaround (see https://github.com/kubernetes-sigs/controller-runtime/issues/1657)
ARCH_FLAG =
ifeq ($(shell uname -sm),Darwin arm64)
	ARCH_FLAG = --arch=amd64
endif
.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) $(ARCH_FLAG) use $(ENVTEST_K8S_VERSION) -p path)" WC_IMG=${WC_IMG} TEST_PROMISE_CONTROLLER_POD_IDENTIFIER_UUID=12345 ACK_GINKGO_DEPRECATIONS=1.16.4 go run github.com/onsi/ginkgo/ginkgo -r  --coverprofile cover.out --skipPackage=system

ENVTEST = $(shell pwd)/bin/setup-envtest
.PHONY: envtest
envtest: ## Download envtest-setup locally if necessary.
	$(call go-get-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest@latest)

##@ Build

# Generate manifests for distributed installation
build: generate fmt vet ## Build manager binary.
	CGO_ENABLED=1 go build -o bin/manager main.go

run: manifests generate fmt vet ## Run a controller from your host.
	go run ./main.go

debug-run: manifests generate fmt vet ## Run a controller in debug mode from your host
	dlv --listen=:2345 --headless=true --api-version=2 --accept-multiclient debug ./main.go

docker-build: ## Build docker image with the manager.
	docker build -t ${IMG} .
	docker build -t ${IMG_MIRROR} .

docker-build-and-push: ## Push multi-arch docker image with the manager.
	if ! docker buildx ls | grep -q "kratix-image-builder"; then \
		docker buildx create --name kratix-image-builder; \
	fi;
	docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t ${IMG} .
	docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t ${IMG_MIRROR} .

##@ Deployment

install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl delete -f -

deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	WC_IMG=${WC_IMG} $(KUSTOMIZE) build config/default | kubectl apply -f -

distribution: manifests kustomize ## Create a deployment manifest in /distribution/kratix.yaml
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	WC_IMG=${WC_IMG} $(KUSTOMIZE) build config/default --output distribution/kratix.yaml

release: distribution docker-build-and-push work-creator-docker-build-and-push ## Create a release. Set VERSION env var to "vX.Y.Z-n".

work-creator-docker-build-and-push:
	WC_IMG=${WC_IMG} WC_IMG_MIRROR=${WC_IMG_MIRROR} $(MAKE) -C work-creator docker-build-and-push

undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/default | kubectl delete -f -

# If not installed, use: go install github.com/goreleaser/goreleaser@latest
build-worker-resource-builder-binary: ## Uses the goreleaser config to generate binaries
	goreleaser release --rm-dist --snapshot

CONTROLLER_GEN = $(shell pwd)/bin/controller-gen
controller-gen: ## Download controller-gen locally if necessary.
	$(call go-get-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen@v0.12.0)

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

.PHONY: list
list:
	@$(MAKE) -pRrq -f $(lastword $(MAKEFILE_LIST)) : 2>/dev/null | awk -v RS= -F: '/^# File/,/^# Finished Make data base/ {if ($$1 !~ "^[#.]") {print $$1}}' | sort | egrep -v -e '^[^[:alnum:]]' -e '^$@$$'

JENKINS_PIPELINE_IMAGE ?= syntasso/jenkins-request-pipeline
build-and-push-jenkins-pipeline-image:
	if ! docker buildx ls | grep -q "jenkins-pipeline-builder"; then \
		docker buildx create --name jenkins-pipeline-builder; \
	fi;
	docker buildx build \
		--builder jenkins-pipeline-builder \
		--platform linux/arm64,linux/amd64 \
		--tag ${JENKINS_PIPELINE_IMAGE} \
		--push \
		--file samples/jenkins/request-pipeline-image/Dockerfile \
		samples/jenkins/request-pipeline-image
