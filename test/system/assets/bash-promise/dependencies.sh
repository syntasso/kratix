#!/usr/bin/env sh
set -eux

unique_id=$(yq '.metadata.name' /kratix/input/object.yaml)

imperative_platform_namespace=${unique_id}-platform-imperative
declarative_worker_namespace=${unique_id}-worker-declarative
declarative_platform_namespace=${unique_id}-platform-declarative

if [ "${KRATIX_WORKFLOW_ACTION}" = "configure" ]; then
	kubectl create namespace ${declarative_worker_namespace} --dry-run=client -oyaml > /kratix/output/namespace.yaml
	yq -i '.metadata.labels.modifydepsinpipeline = "yup"' /kratix/output/static/dependencies.yaml

	mkdir -p /kratix/output/platform/
	kubectl create namespace ${declarative_platform_namespace} --dry-run=client -oyaml > /kratix/output/platform/namespace.yaml
	cat <<EOF > /kratix/metadata/destination-selectors.yaml
  - directory: platform
    matchLabels:
      environment: platform
  - matchLabels:
      extra: label
EOF

	kubectl get namespace ${imperative_platform_namespace} || kubectl create namespace ${imperative_platform_namespace}
	exit 0
fi

kubectl delete namespace ${imperative_platform_namespace}


