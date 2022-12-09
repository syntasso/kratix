#!/usr/bin/env bash

ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )

set -eu

cd $ROOT

source "$ROOT/scripts/utils.sh"
export VERSION="$(commit_sha)"
export DOCKER_BUILDKIT=1


docker run -it --rm --privileged tonistiigi/binfmt --install all

# Kratix Platform image
make docker-build-and-push

# Work Creator image
make work-creator-docker-build-and-push

# Build workshop images
make build-and-push-jenkins-pipeline-image

docker build --platform linux/amd64 --tag syntasso/knative-serving-pipeline \
    --file samples/knative-serving/request-pipeline-image/Dockerfile \
    samples/knative-serving/request-pipeline-image

docker build --platform linux/amd64 --tag syntasso/postgres-request-pipeline \
    --file samples/postgres/request-pipeline-image/Dockerfile \
    samples/postgres/request-pipeline-image

docker build --platform linux/amd64 --tag syntasso/paved-path-demo-request-pipeline \
    --file samples/paved-path-demo/request-pipeline-image/Dockerfile \
    samples/paved-path-demo/request-pipeline-image

docker push syntasso/knative-serving-pipeline
docker push syntasso/postgres-request-pipeline
docker push syntasso/paved-path-demo-request-pipeline
