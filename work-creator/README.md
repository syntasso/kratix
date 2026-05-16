* Start KinD
* `make deploy` in root of kratix
* `cd work-creator`
* Start work creator (note `--input-directory` needs to be an absolute path)
** `go run cmd/main.go work-creator --input-directory=${PWD}/test/integration/samples --promise-name=yourPromise --pipeline-name=yourPipeline`
* Edit samples to your liking
* Re-run work creator to update objects
** `go run cmd/main.go work-creator --input-directory=${PWD}/test/integration/samples --promise-name=yourPromise --pipeline-name=yourPipeline`

## Subcommands

The `pipeline-adapter` binary ships four subcommands:

* `reader` — runs before the user pipeline; fetches the requesting object and promise into `/kratix/input`.
* `work-creator` — builds a `Work` resource from pipeline output. Used by the in-cluster pipeline Job and available for manual debugging.
* `update-status` — patches the requesting object's status from `/work-creator-files/metadata/status.yaml`. Available for manual debugging.
* `run` — runs `work-creator` followed by `update-status` in a single process. **This is what the Kratix operator schedules in production**; `work-creator` and `update-status` are kept as separate subcommands purely for direct invocation when debugging a live pod.

Example (manual invocation matching what the operator schedules):

```
go run cmd/main.go run \
  --input-directory=${PWD}/test/integration/samples \
  --promise-name=yourPromise \
  --pipeline-name=yourPipeline \
  --workflow-type=resource
```
