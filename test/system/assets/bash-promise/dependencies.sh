#!/usr/bin/env sh
set -euo pipefail
set +x


if [ "${KRATIX_WORKFLOW_ACTION}" = "configure" ]; then
  kubectl create namespace bash-workflow-namespace-${VERSION} --dry-run=client -oyaml > /kratix/output/namespace.yaml
  yq -i '.metadata.labels.modifydepsinpipeline = "yup"' /kratix/output/static/dependencies.yaml

  mkdir -p /kratix/output/platform/
  kubectl create namespace promise-workflow-namespace --dry-run=client -oyaml > /kratix/output/platform/namespace.yaml
  cat <<EOF > /kratix/metadata/destination-selectors.yaml
  - directory: platform
    matchLabels:
      environment: platform
  - matchLabels:
      extra: label
EOF

  kubectl get namespace $(yq '.metadata.name' /kratix/input/object.yaml)-workflow-imperative-namespace || kubectl create namespace $(yq '.metadata.name' /kratix/input/object.yaml)-workflow-imperative-namespace
  exit 0
fi

cat /kratix/input/object.yaml
kubectl delete namespace $(yq '.metadata.name' /kratix/input/object.yaml)-workflow-imperative-namespace

