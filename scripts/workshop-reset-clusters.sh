#!/usr/bin/env bash

if [ -z "$PLATFORM_CONTEXT" ] || [ -z "$WORKER_CONTEXT" ]; then
    echo "Please set the context for the platform and worker clusters:"
    echo -e "  export PLATFORM_CONTEXT=\"<your platform context>\""
    echo -e "  export WORKER_CONTEXT=\"<your worker context>\""
    echo
    echo "If you are running your clusters with KinD:"
    echo "  export PLATFORM_CONTEXT=\"kind-platform\""
    echo "  export WORKER_CONTEXT=\"kind-worker\""
    exit 1
fi

# Delete minio deployment to wipe out the buckets
kubectl delete --context $PLATFORM_CONTEXT deployment minio --namespace kratix-platform-system

# Delete Kratix deployment to stop dynamic controllers
kubectl delete --context $PLATFORM_CONTEXT deployment -n kratix-platform-system kratix-platform-controller-manager

# Remove all kratix-related crds to remove all promises
kubectl delete --context $PLATFORM_CONTEXT crds \
    $(kubectl --context $PLATFORM_CONTEXT get crds --no-headers -o custom-columns=":metadata.name" | grep -e "kratix\|promise")

# Scale down postgres statefulset to zero -- tell k8s to remove the pods
kubectl scale --context $WORKER_CONTEXT statefulset \
    $(kubectl get --context $WORKER_CONTEXT statefulsets --selector application=spilo --no-headers -o custom-columns=":metadata.name") --replicas=0

# Remove buckets from the worker
kubectl delete --all --context $WORKER_CONTEXT --namespace flux-system buckets.source.toolkit.fluxcd.io
kubectl delete --all --context $WORKER_CONTEXT --namespace flux-system kustomizations.kustomize.toolkit.fluxcd.io

# Remove all pipeline pods
kubectl delete --context $PLATFORM_CONTEXT pod \
    $(kubectl get --context $PLATFORM_CONTEXT pods --no-headers -o custom-columns=":metadata.name" | grep request-pipeline)

# Reinstall Kratix and re-register the worker cluster
kubectl apply --context $PLATFORM_CONTEXT -f https://raw.githubusercontent.com/syntasso/kratix/main/distribution/kratix.yaml
kubectl apply --context $PLATFORM_CONTEXT -f https://raw.githubusercontent.com/syntasso/kratix/main/hack/platform/minio-install.yaml
kubectl apply --context $PLATFORM_CONTEXT -f https://raw.githubusercontent.com/syntasso/kratix/main/config/samples/platform_v1alpha1_worker_cluster.yaml
kubectl apply --context $WORKER_CONTEXT -f https://raw.githubusercontent.com/syntasso/kratix/main/hack/worker/gitops-tk-install.yaml
kubectl apply --context $WORKER_CONTEXT -f https://raw.githubusercontent.com/syntasso/kratix/main/hack/worker/gitops-tk-resources.yaml

# Remove leftover postgres resources
kubectl delete --context $WORKER_CONTEXT statefulset \
    $(kubectl get --context $WORKER_CONTEXT statefulsets --selector application=spilo --no-headers -o custom-columns=":metadata.name")
kubectl delete --context $WORKER_CONTEXT services \
    $(kubectl get --context $WORKER_CONTEXT services --selector application=spilo --no-headers -o custom-columns=":metadata.name")
kubectl delete --context $WORKER_CONTEXT poddisruptionbudgets.policy \
    $(kubectl get --context $WORKER_CONTEXT poddisruptionbudgets.policy --selector application=spilo --no-headers -o custom-columns=":metadata.name")

