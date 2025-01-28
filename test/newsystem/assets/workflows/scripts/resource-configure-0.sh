#!/usr/bin/env sh

set -xe

configMapName="$(yq ".spec.configMapName" /kratix/input/object.yaml)"

mkdir -p /kratix/output/dest1 /kratix/output/dest2

kubectl create configmap --dry-run=client --output=yaml --namespace testbundle-ns "${configMapName}" \
  --from-literal=timestamp="$(date +%s)" > /kratix/output/dest1/configmap.yaml

kubectl create configmap --dry-run=client --output=yaml --namespace testbundle-ns "${configMapName}" \
  --from-literal=timestamp="$(date +%s)" > /kratix/output/dest2/configmap.yaml

cat <<EOF > /kratix/metadata/destination-selectors.yaml
- directory: dest1
  matchLabels: {target: worker1}
- matchLabels: {target: worker2}
EOF

cat <<EOF > /kratix/metadata/status.yaml
stage: one
EOF

