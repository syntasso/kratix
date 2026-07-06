# -*- mode: Python -*-
# Kratix local dev environment.
# Spec: docs/superpowers/specs/2026-07-06-kratix-tilt-dev-env-design.md
# Usage: `make dev` (default argo-git) or `make dev GITOPS=flux-bucket`.

load('ext://restart_process', 'docker_build_with_restart')

allow_k8s_contexts('kind-platform')

IMAGE = 'docker.io/syntasso/kratix-platform:dev'

# Cross-compile for the cluster's OS/arch. kind nodes are linux and match the
# host arch; building for the host's native darwin/amd64 would produce a binary
# that cannot execute in the linux pod.
GOARCH = str(local('go env GOARCH', quiet=True)).strip()

# ---- Build layer: host go build + live_update ----
local_resource(
    'go-build',
    cmd='CGO_ENABLED=0 GOOS=linux GOARCH=%s go build -o bin/manager cmd/main.go && ' % GOARCH +
        'CGO_ENABLED=0 GOOS=linux GOARCH=%s go build -o bin/pipeline-adapter work-creator/*.go' % GOARCH,
    deps=['cmd', 'work-creator', 'api', 'lib', 'internal', 'go.mod', 'go.sum'],
    labels=['build'],
)

docker_build_with_restart(
    IMAGE,
    '.',
    dockerfile='hack/dev/Dockerfile.dev',
    entrypoint=['/manager'],
    only=['bin/manager', 'bin/pipeline-adapter'],
    # Sync BOTH binaries: go-build recompiles both on any change, so a changed
    # bin/pipeline-adapter without a matching sync rule would force a full image
    # rebuild on every edit. Syncing both keeps the common controller-edit loop
    # in-place (~1s). NOTE: a live_update does not re-tag the registry image, so
    # a genuine work-creator/pipeline-adapter change that pipeline pods must pull
    # needs a full rebuild — trigger it from the Tilt UI (or `tilt trigger`).
    live_update=[
        sync('bin/manager', '/manager'),
        sync('bin/pipeline-adapter', '/bin/pipeline-adapter'),
    ],
    # Rewrite the image ref wherever it appears as a literal container env value
    # (PIPELINE_ADAPTER_IMG) so pipeline pods pull from the local registry too.
    match_in_env_vars=True,
)

# ---- cert-manager (webhooks dependency) ----
local_resource(
    'cert-manager',
    cmd='kubectl --context kind-platform apply -f ' +
        'https://github.com/cert-manager/cert-manager/releases/download/v1.15.0/cert-manager.yaml && ' +
        'kubectl --context kind-platform wait --for=condition=available --timeout=180s ' +
        '-n cert-manager deployment/cert-manager deployment/cert-manager-cainjector deployment/cert-manager-webhook',
    labels=['platform'],
)

# ---- kratix controller ----
# Convert PIPELINE_ADAPTER_IMG from configMapKeyRef to a literal env value so
# Tilt's match_in_env_vars rewrites it to the local-registry ref.
objects = read_yaml_stream('distribution/kratix.yaml')
for o in objects:
    if o.get('kind') == 'Deployment' and \
       o['metadata']['name'] == 'kratix-platform-controller-manager':
        for c in o['spec']['template']['spec']['containers']:
            if c['name'] == 'manager':
                newenv = []
                for e in c.get('env', []):
                    if e['name'] == 'PIPELINE_ADAPTER_IMG':
                        newenv.append({'name': 'PIPELINE_ADAPTER_IMG', 'value': IMAGE})
                    else:
                        newenv.append(e)
                c['env'] = newenv

k8s_yaml(encode_yaml_stream(objects))

k8s_resource(
    'kratix-platform-controller-manager',
    resource_deps=['go-build', 'cert-manager'],
    labels=['platform'],
)
