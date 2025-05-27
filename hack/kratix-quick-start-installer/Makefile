IMG_TAG ?= docker.io/syntasso/kratix-quick-start-installer:latest

build-and-load:
	docker build  -t $(IMG_TAG) .
	kind load docker-image $(IMG_TAG) --name platform

create-platform:
	kind create cluster --name platform --config ../platform/kind-platform-config.yaml

delete-platform:
	kind delete cluster --name platform

run-system-test:
	ginkgo -r test/

system-test: create-platform build-and-load run-system-test delete-platform

docker-build-and-push: ## Push multi-arch docker image with the manager.
	if ! docker buildx ls | grep -q "kratix-image-builder"; then \
		docker buildx create --name kratix-image-builder; \
	fi;
	docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t ${IMG_TAG} .

quick-start: create-platform build-and-load
	kubectl apply -f manifests/kratix-quick-start-installer.yaml

