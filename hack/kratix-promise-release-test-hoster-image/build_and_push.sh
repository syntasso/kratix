#!/usr/bin/env sh

export IMG="syntassodev/kratix-promise-release-test-hoster:v0.0.2"

if ! docker buildx ls | grep -q "kratix-image-builder"; then \
	docker buildx create --name kratix-image-builder; \
fi;

docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t ${IMG} .

