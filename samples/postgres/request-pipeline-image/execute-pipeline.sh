#!/bin/sh 

set -x

# Store all input files in a known location
cp -r /tmp/transfer/* /input/

# Read current values from the provided resource request
export NAME=$(yq eval '.metadata.name' /input/object.yaml)
export NAMESPACE=$(yq eval '.metadata.namespace' /input/object.yaml)
export PREPARED_DBS=$(yq eval '.spec.preparedDatabases' /input/object.yaml)

# Replace defaults with user provided values
cat /input/minimal-postgres-manifest.yaml |  \
  yq eval '.metadata.name = env(NAME) |
          .metadata.namespace = env(NAMESPACE) |
          .spec.preparedDatabases = env(PREPARED_DBS)' - \
  > /output/output.yaml