#!/usr/bin/env bash
set -e

test_promise() {
  kubectl get crd redisfailovers.databases.spotahome.com
  kubectl wait --for=condition=Available --timeout=5s deployment/redisoperator
}

test_resource_request() {
  kubectl wait --for=condition=Available --timeout=5s deployment/rfs-example
}

if [ "$1" = "promise" ]; then
  test_promise
else
  test_resource_request
fi
