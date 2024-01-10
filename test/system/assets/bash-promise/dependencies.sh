#!/usr/bin/env sh
set -eux

uniqueId=$1

imperative_platform_namespace=$(yq '.metadata.name' /kratix/input/object.yaml)-platform-imperative
declarative_worker_namespace=$(yq '.metadata.name' /kratix/input/object.yaml)-worker-declarative
declarative_platform_namespace=$(yq '.metadata.name' /kratix/input/object.yaml)-platform-declarative

if [ "${KRATIX_WORKFLOW_ACTION}" = "configure" ]; then
	kubectl create namespace ${declarative_worker_namespace} --dry-run=client -oyaml > /kratix/output/namespace.yaml

	yq -i '.metadata.labels.modifydepsinpipeline = "yup"' /kratix/output/static/dependencies.yaml

	mkdir /kratix/output/platform/

	cat <<EOF > /kratix/output/platform/cr-and-crb.yaml
	---
	apiVersion: rbac.authorization.k8s.io/v1
	kind: ClusterRole
	metadata:
		name: bash-${uniqueId}-default-resource-pipeline-credentials
	rules:
	- apiGroups:
		- ""
		resources:
		- namespaces
		verbs:
		- "*"
	---
	apiVersion: rbac.authorization.k8s.io/v1
	kind: ClusterRoleBinding
	metadata:
		name: bash-${uniqueId}-default-resource-pipeline-credentials
	roleRef:
		apiGroup: rbac.authorization.k8s.io
		kind: ClusterRole
		name: bash-${uniqueId}-default-resource-pipeline-credentials
	subjects:
	- kind: ServiceAccount
		name: bash-${uniqueId}-resource-pipeline
		namespace: default
EOF

	kubectl create namespace ${declarative_platform_namespace} --dry-run=client -oyaml > /kratix/output/platform/namespace.yaml
	cat <<EOF > /kratix/metadata/destination-selectors.yaml
	- directory: platform
		matchLabels:
			environment: platform
	- matchLabels:
			extra: label
EOF

	kubectl get namespace ${imperative_platform_namespace} || kubectl create namespace $(yq '.metadata.name' /kratix/input/object.yaml)-workflow-imperative-namespace
	exit 0
fi

kubectl delete namespace ${imperative_platform_namespace}


