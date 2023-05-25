# Kratix
This chart is for installing [Kratix](https://kratix.io/) on your Platform cluster.

## Installation
The Helm Chart can be installed without providing any values, this will install
the Kratix controllers and CRDs only.
```bash
helm --kube-context ${PLATFORM} install kratix charts/kratix/
```

### Optional Configuration
Kratix will often be configured with one or more [Clusters](https://kratix.io/docs/main/reference/clusters/intro)
and [StateStores](https://kratix.io/docs/main/reference/statestore/intro). If you
know at installation time the values for these resources you can provide
them as values. Alternatively you can manually install later on. For example to
configure a worker cluster and a [BucketStateStore](https://kratix.io/docs/main/reference/statestore/bucketstatestore)
at installation time you could provide the following `values.yaml` file:

```yaml
stateStores:
- kind: BucketStateStore
  name: default
  namespace: default
  secretRef:
    name: minio-credentials
    # Optional, omit `values` field when the secret creation is managed externally
    values:
      accesskey: bWluaW9hZG1pbg==
      secretkey: bWluaW9hZG1pbg==
  insecure: true
  endpoint: minio.kratix-platform-system.svc.cluster.local
  bucket: kratix

clusters:
- name: worker-cluster-1
  namespace: default
  labels:
    env: dev
  path: ""
  stateStoreRef:
    name: default
    kind: BucketStateStore
```

See [the values file for more example configuration](./values.yaml). To pass the values file
in during the helm install run as follows:

```bash
helm --kube-context ${PLATFORM} install kratix charts/kratix/ -f values.yaml
```
