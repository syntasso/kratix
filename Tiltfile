# -*- mode: Python -*-
# Kratix local dev environment.
# Spec: docs/superpowers/specs/2026-07-06-kratix-tilt-dev-env-design.md
# Usage: `make dev` (default argo-git) or `make dev GITOPS=flux-bucket`.

load('ext://restart_process', 'docker_build_with_restart')

allow_k8s_contexts('kind-platform')

IMAGE = 'docker.io/syntasso/kratix-platform:dev'

# ---- GitOps matrix ----
config.define_string('gitops')
_cfg = config.parse()
_gitops = _cfg.get('gitops', 'argo-git')
_matrix = {
    'argo-git':    ('argo', 'git'),
    'flux-bucket': ('flux', 'bucket'),
    'flux-git':    ('flux', 'git'),
}
if _gitops not in _matrix:
    fail('unknown gitops=%s (argo-git|flux-bucket|flux-git); argo-bucket is unsupported upstream' % _gitops)
PROVIDER, STORE = _matrix[_gitops]

# Cross-compile for the cluster's OS/arch. kind nodes are linux and match the
# host arch; building for the host's native darwin/amd64 would produce a binary
# that cannot execute in the linux pod.
GOARCH = str(local('go env GOARCH', quiet=True)).strip()

# ---- Build layer: host go build + live_update ----
# Split builds so editing controller code rebuilds only the manager binary (and
# live-syncs it), while a work-creator change rebuilds the adapter and triggers a
# full image rebuild — which is what pipeline pods need (they pull the image).
local_resource(
    'go-build-manager',
    cmd='CGO_ENABLED=0 GOOS=linux GOARCH=%s go build -o bin/manager cmd/main.go' % GOARCH,
    deps=['cmd', 'api', 'lib', 'internal', 'go.mod', 'go.sum'],
    labels=['build'],
)
local_resource(
    'go-build-adapter',
    cmd='CGO_ENABLED=0 GOOS=linux GOARCH=%s go build -o bin/pipeline-adapter work-creator/*.go' % GOARCH,
    deps=['work-creator', 'api', 'lib', 'go.mod', 'go.sum'],
    labels=['build'],
)

docker_build_with_restart(
    IMAGE,
    '.',
    dockerfile='hack/dev/Dockerfile.dev',
    entrypoint=['/home/appuser/manager'],
    only=['bin/manager', 'bin/pipeline-adapter'],
    # Only the manager is live-synced, into $HOME so the non-root user can replace
    # it in place (replacing a file needs write on its parent dir; / is root-owned).
    # A work-creator change rebuilds bin/pipeline-adapter, which is outside this
    # sync rule and so triggers a full image rebuild — correct, since pipeline pods
    # pull the freshly-tagged image.
    live_update=[sync('bin/manager', '/home/appuser/manager')],
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
    resource_deps=['go-build-manager', 'go-build-adapter', 'cert-manager'],
    labels=['platform'],
)

# ---- state-store backend (Gitea or MinIO) ----
local_resource(
    'statestore-backend',
    cmd='./hack/dev/setup-statestore.sh %s' % STORE,
    labels=['gitops'],
)

# ---- StateStore CR (patched for kind networking on git) ----
if STORE == 'git':
    # Reuse the repo's existing platform_destination_ip helper (scripts/utils.sh)
    # instead of reimplementing the docker-inspect+yq lookup, and fail fast if it
    # returns empty/null rather than sed-ing "https://null:31333" and timing out.
    _store_cmd = ("bash -c 'source scripts/utils.sh; " +
                  "ip=$(platform_destination_ip platform); " +
                  "if [ -z \"$ip\" ] || [ \"$ip\" = \"null\" ]; then echo \"could not resolve platform cluster IP\" >&2; exit 1; fi; " +
                  "sed \"s/172.18.0.2/$ip/g\" config/samples/platform_v1alpha1_gitstatestore.yaml " +
                  "| kubectl --context kind-platform apply -f - && " +
                  "kubectl --context kind-platform wait gitstatestore default --for=condition=Ready --timeout=180s'")
else:
    _store_cmd = ("kubectl --context kind-platform apply -f config/samples/platform_v1alpha1_bucketstatestore.yaml && " +
                  "kubectl --context kind-platform wait bucketstatestore default --for=condition=Ready --timeout=180s")

local_resource(
    'statestore-cr',
    cmd=_store_cmd,
    resource_deps=['kratix-platform-controller-manager', 'statestore-backend'],
    labels=['gitops'],
)

# ---- register the platform as a destination (installs the reconciler) ----
# register-destination -> install-gitops honours GITOPS_PROVIDER; --git selects git store.
_reg_flags = '--git' if STORE == 'git' else ''
local_resource(
    'register-destination',
    cmd='GITOPS_PROVIDER=%s ./scripts/register-destination ' % PROVIDER +
        '--name platform-cluster --context kind-platform ' +
        '--with-label environment=dev ' +
        '--platform-context kind-platform %s && ' % _reg_flags +
        'kubectl --context kind-platform wait destination platform-cluster --for=condition=Ready --timeout=300s',
    resource_deps=['statestore-cr'],
    labels=['gitops'],
)

# ---- expose the ArgoCD UI on a host-mapped NodePort (argo cells only) ----
if PROVIDER == 'argo':
    local_resource(
        'argocd-ui',
        cmd='./hack/dev/expose-argocd.sh',
        resource_deps=['register-destination'],
        labels=['gitops'],
    )
