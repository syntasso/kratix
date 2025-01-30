#!/usr/bin/env sh

set -xe

stage="$(yq ".status.stage" /kratix/input/object.yaml)"

if [ "$stage" != "one" ]; then
  echo "unexpected status: $stage; expected one"
  exit 1
fi

namespaceName="$(yq ".spec.namespaceName" /kratix/input/object.yaml)"
kubectl create namespace --dry-run=client --output=yaml "${namespaceName}" > /kratix/output/namespace.yaml

cat <<EOF > /kratix/metadata/destination-selectors.yaml
- matchLabels: {target: worker-1}
EOF

cat <<EOF > /kratix/metadata/status.yaml
stage: two
completed: true
EOF

