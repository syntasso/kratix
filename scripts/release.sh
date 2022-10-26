#!/usr/bin/env bash

ROOT=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )

cd $ROOT

git checkout main
git merge --no-ff --no-edit dev

export VERSION="main"
export DOCKER_BUILDKIT=1

make distribution
make test
make int-test

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

docker push syntasso/knative-serving-pipeline
docker push syntasso/postgres-request-pipeline

git add -f config/
git add -f distribution/kratix.yaml
git commit --amend --no-edit
git push origin main