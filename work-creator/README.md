* Start KinD
* `make deploy` in root of kratix
* `cd work-creator`
* Start work creator (all directories must be absolute paths)
** `go run main.go work-creator --workloads-directory=${PWD}/samples/complete/input --metadata-directory=${PWD}/samples/complete/metadata --promise-scheduling-directory=${PWD}/samples/complete/kratix-system --workflow-type=resource --promise-name=yourPromise --pipeline-name=yourPipeline`
* Edit samples to your liking
* Re-run work creator to update objects

The work creator reads three directories:

* `--workloads-directory`: the documents to schedule. Required.
* `--metadata-directory`: `destination-selectors.yaml` and `workflow-control.yaml`. The health definitions count file is written here. Optional.
* `--promise-scheduling-directory`: the `promise-scheduling` file. Optional.

`--workflow-type` is required with these flags.

`--input-directory` is deprecated. It points at a directory with `input/`, `metadata/` and `kratix-system/` folders inside. It cannot be used with the flags above.
