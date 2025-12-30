IMG_TAG ?= docker.io/syntasso/kratix-quick-start-installer:latest

ifeq ($(CI),true)
ifeq ($(GITHUB_ACTIONS),true)
DOCKER_BUILD := docker buildx build --load --cache-from=type=local,src=$${BUILDKIT_CACHE_DIR:-cache-mount} --cache-to=type=local,dest=$${BUILDKIT_CACHE_DIR:-cache-mount},mode=max
else
DOCKER_BUILD := docker build
endif
else
DOCKER_BUILD := docker build
endif

build-and-load:
	$(DOCKER_BUILD) -t $(IMG_TAG) .
	kind load docker-image $(IMG_TAG) --name platform

create-platform:
	kind create cluster --name platform --config ../platform/kind-platform-config.yaml

delete-platform:
	kind delete cluster --name platform

run-system-test:
	go run github.com/onsi/ginkgo/v2/ginkgo -r test/

system-test: create-platform build-and-load run-system-test delete-platform

docker-build-and-push: ## Push multi-arch docker image with the manager.
	if ! docker buildx ls | grep -q "kratix-image-builder"; then \
		docker buildx create --name kratix-image-builder; \
	fi;
	docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t ${IMG_TAG} .

quick-start: create-platform build-and-load
	kubectl apply -f manifests/kratix-quick-start-installer.yaml
