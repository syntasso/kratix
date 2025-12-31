IMG_TAG ?= docker.io/syntasso/kratix-quick-start-installer:latest
IMG_TAG_NO_REG := $(patsubst docker.io/%,%,$(IMG_TAG))

build-image:
	@if [ "$${CI:-false}" = "true" ] && [ "$${FORCE_QUICKSTART_BUILD:-false}" != "true" ] && \
		docker image inspect $(IMG_TAG_NO_REG) >/dev/null 2>&1; then \
		echo "CI: quick-start installer image already loaded in the runner, skipping build" 1>&2; \
	else \
		docker build -t $(IMG_TAG) -t $(IMG_TAG_NO_REG) . ; \
	fi

export-image: build-image
	@QUICKSTART_EXPORT_PATH="$${QUICKSTART_EXPORT_PATH:-$$(mktemp -t kratix-quick-start-installer-XXXXXX.tar)}"; \
	docker save $(IMG_TAG) $(IMG_TAG_NO_REG) -o "$${QUICKSTART_EXPORT_PATH}"; \
	echo "$${QUICKSTART_EXPORT_PATH}"

build-and-load: build-image
	@kind load docker-image $(IMG_TAG) --name platform

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
