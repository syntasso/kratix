# Kratix
This chart is for installing Kratix. To install running from the repo root directory:

```bash
helm --kube-context ${PLATFORM} install kratix charts/kratix/
```

## Optional values
As well as installing Kratix you can install Kratix `Clustes`, `StateStores` and
any additional resources through the values file. See [the values file for examples](./values.yaml)

If you have configured a set of values for Kratix you can provide them at install time
by passing the `-f path/to/values.yaml` flag, e.g.:

```bash
helm --kube-context ${PLATFORM} install kratix charts/kratix/ -f values.yaml
```
