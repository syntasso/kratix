# System tests

These specs run against a real Kratix on kind clusters.

```sh
make system-test                         # recreate the clusters, then run the suite
make run-system-test                     # run against clusters that are already up
GINKGO_PROCS=--procs=4 make system-test  # cap parallelism
```

The suite runs in parallel, one ginkgo process per core. On a machine with many
cores that may overload the kind apiserver. `GINKGO_PROCS` caps this.

## Adding a spec

Default to parallel and unlabelled. Give every resource a unique name (promise,
CRD kind, requests) and scope assertions to your own objects — an unscoped
`kubectl get workplacement` matches whatever other specs have running.

A test Destination needs `strictMatchLabels: true` and a label unique to the
spec. Without it, works from a promise with no `destinationSelectors` are
scheduled to every matching Destination, so another spec's files land in yours.

`Serial` is for specs that genuinely cannot coexist with others. Use the
following guidance for adding new specs:

| If the spec…                                                                             | Mark the top-level Describe        |
| ---------------------------------------------------------------------------------------- | ---------------------------------- |
| replaces the `kratix` ConfigMap, or restarts the controller                              | `Label("config-mutating"), Serial` |
| mutates shared state store infrastructure — Destination secrets, the Gitea repo, buckets | `Label("destination"), Serial`     |
| is Serial for some other reason (fixed names, global assertions)                         | `Serial`, no label                 |

Labels group specs so CI can split the suite across runners, so put them on the
top-level Describe and keep a feature's specs together.

The controller reads the `kratix` ConfigMap only at boot, so a spec that changes
it must also `restartController()`. Nothing puts the default back afterwards: a
Serial spec runs with whatever config the previous one left, and only Serial
specs are exposed, since the parallel pool always runs first. If your spec
depends on the default config, apply `assets/kratix-config.yaml` yourself.
