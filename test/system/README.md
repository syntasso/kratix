# System tests

The suite runs with `ginkgo -p`. In CI it is split into three shards via
`--label-filter`, each on its own runner with its own kind clusters:

| Shard | Filter | Contains |
|---|---|---|
| `config` | `config-mutating` | tests that change platform-wide config |
| `destinations` | `destination \|\| git-recovery \|\| compound-promise` | tests that mutate shared state-store infrastructure |
| `rest` | negation of the above | everything else |

The `rest` filter is a pure negation, so a Describe with no label always runs
there — a forgotten label can never drop a test from CI.

## Adding a new test

1. **Default: no label, no `Serial`.** Give every resource a unique name
   (promise, CRD kind, requests). Your Describe runs in parallel in the
   `rest` shard.
2. **Your test replaces or patches the `kratix` ConfigMap, restarts the
   controller-manager, or depends on non-default platform config?**
   Mark it `Label("config-mutating"), Serial`.
3. **Your test mutates shared state-store infrastructure** (Destination
   credentials or secrets, the Gitea repo out-of-band, MinIO buckets,
   Destinations that other promises could be scheduled to)?
   Mark it `Label("destination"), Serial`.
4. **`Serial` needed for another reason** (fixed names, globally-scoped
   assertions)? Prefer unique names and scoped assertions so you can drop
   `Serial`; if you must keep it, `Serial` without a label is fine — it
   serialises within the `rest` shard.
5. Put labels on the **top-level Describe** so a feature's specs stay in one
   shard.

`Serial` serialises specs within a shard, and each shard runs on its own
cluster, so a wrong label costs shard balance, not correctness. If a shard
grows past ~15 minutes of test time, rebalance by moving whole Describes
(swap labels — never split a Describe across shards).

## Running a shard locally

```sh
GINKGO_FLAGS="--label-filter='destination || git-recovery || compound-promise'" \
  make -j4 run-system-test
```

The inner single quotes are required: the Makefile expands `GINKGO_FLAGS`
unquoted, and an unquoted `&&` in the filter is split by the shell, making
ginkgo fail with "Found no test suites".
