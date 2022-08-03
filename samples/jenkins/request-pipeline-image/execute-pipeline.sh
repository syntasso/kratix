#!/bin/sh

set -x

# Read current values from the provided resource request
export NAME=$(yq eval '.metadata.name' /input/object.yaml)

# Replace defaults with user provided values
cat /tmp/transfer/jenkins_instance.yaml |  \
  yq eval '.metadata.name = env(NAME)' - \
  > /output/jenkins_instance.yaml

sed "s/NAME/${NAME}/g" /tmp/transfer/service_account.yaml > /output/service_account.yaml
