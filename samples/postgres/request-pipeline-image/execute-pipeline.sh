#!/bin/sh 

cp -r /tmp/transfer/* /input/

cat /input/minimal-postgres-manifest.yaml |  \
  NAME=$(yq eval '.metadata.name' /input/object.yaml) \
  NAMESPACE=$(yq eval '.metadata.namespace' /input/object.yaml) \
  PREPARED_DBS=$(yq eval '.spec.preparedDatabases' /input/object.yaml) \
  yq eval '.metadata.namespace = env(NAMESPACE) | .metadata.name = env(NAME) | .spec.preparedDatabases = env(PREPARED_DBS)' - \
  > /output/output.yaml