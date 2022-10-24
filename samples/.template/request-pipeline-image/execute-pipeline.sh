#!/bin/sh

set -x

# example of reading values from the provided resource request
# note: Kratix will always load the resource request to /input/object.yaml
export NAME=$(yq eval '.metadata.name' /input/object.yaml)

# example of injecting values into the example asset
# note: all kubernetes resources in /output will be applied to the worker cluster
cat /tmp/transfer/example-asset.yaml | \
  yq eval '.metadata.name = env(NAME) ' - > /output/example-asset.yaml
