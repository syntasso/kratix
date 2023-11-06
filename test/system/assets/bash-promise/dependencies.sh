#!/usr/bin/env sh
set -eux

kubectl create namespace bash-workflow-namespace-${VERSION} --dry-run=client -oyaml > /kratix/output/namespace.yaml
yq -i '.metadata.labels.modifydepsinpipeline = "yup"' /kratix/output/static/dependencies.yaml

mkdir /kratix/output/platform/
kubectl create namespace promise-workflow-namespace --dry-run=client -oyaml > /kratix/output/platform/namespace.yaml
cat <<EOF > /kratix/metadata/destination-selectors.yaml
- directory: platform
  matchLabels:
    environment: platform
- matchLabels:
    extra: label
EOF