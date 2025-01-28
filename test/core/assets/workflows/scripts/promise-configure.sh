#!/usr/bin/env sh

set -xe

kubectl create namespace --dry-run=client --output=yaml testbundle-ns > /kratix/output/namespace.yaml
kubectl create configmap --dry-run=client --output=yaml --namespace testbundle-ns testbundle-cm \
  --from-literal=timestamp="$(date +%s)" \
  --from-literal=promiseEnv="${VALUE:-"undefined"}" > /kratix/output/configmap.yaml