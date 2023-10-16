#!/usr/bin/env sh

export IMG="ghcr.io/syntasso/kratix-pipeline-debugger:v0.0.1"

if ! docker buildx ls | grep -q "kratix-image-builder"; then \
	docker buildx create --name kratix-image-builder; \
fi;

docker buildx build --builder kratix-image-builder --push --platform linux/arm64,linux/amd64 -t ${IMG} .

