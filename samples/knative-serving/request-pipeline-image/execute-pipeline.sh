#!/bin/sh
export CLUSTER_SELECTOR="$(yq eval '.spec.type' /input/object.yaml)"

if [ "${CLUSTER_SELECTOR}" != "null" ]; then
  echo "environment: ${CLUSTER_SELECTOR}" > /metadata/cluster-selectors.yaml
fi
cp /tmp/transfer/* /output/