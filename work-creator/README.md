* Start KinD
* `make deploy` in root of kratix
* `cd work-creator`
* Start work creator (note `--input-directory` needs to be an absolute path)
** `go run cmd/main.go work-creator --input-directory=${PWD}/test/integration/samples --promise-name=yourPromise --pipeline-name=yourPipeline`
* Edit samples to your liking
* Re-run work creator to update objects
** `go run cmd/main.go work-creator --input-directory=${PWD}/test/integration/samples --promise-name=yourPromise --pipeline-name=yourPipeline`