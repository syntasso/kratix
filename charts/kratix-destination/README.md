# Kratix Worker
This chart is for installing [Flux](https://github.com/fluxcd/flux2) (optional) and configuring
it with the necessary Flux resources to pull down the resources Kratix writes to
the configured [StateStore](https://kratix.io/docs/main/reference/statestore/intro).

## Installation
The Helm Chart needs to be configured with a set of values. A `Git` or `Bucket` config
must be provided. 

### Git
Example Git config:
```yaml
config:
  path: "default/worker-1" # Path in StateStore. See https://kratix.io/docs/main/reference/destinations/intro
  namespace: "default" # Namespace to create config
  secretRef:
    name: "gitea-credentials" # Name of secret
    values:
      username: Z2l0ZWFfYWRtaW4=
      password: cjhzQThDUEhEOSFidDZk
  git:
    url: https://172.18.0.2:31333/gitea_admin/kratix
    branch: main
```

### Bucket
Example Bucket config:
```yaml
config:
  path: "default/worker-1"
  namespace: "default"
  secretRef:
    name: "minio-credentials"
    values:
      accesskey: bWluaW9hZG1pbg==
      secretkey: bWluaW9hZG1pbg==
  bucket:
    insecure: true
    endpoint: 172.18.0.2:31337
    bucket: kratix
```

Flux is installed by default in the chart. If you already have Flux installed or
wish to use your own installation method set the following value in your values file:
```yaml
installFlux: false
```

Once you have your values file configured you can install the chart by running
the following:
```bash
export WORKER=kind-worker # or the context you are installing this on
helm --kube-context ${WORKER} install kratix-destination charts/kratix-destination/ -f values.yaml
```
