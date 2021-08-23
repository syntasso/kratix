* Start KinD
* `make deploy` in root of kratix
* `cd work-creator/pipeline/cmd`
* Start work creator (note `-input-directory` needs to be an absolute path)
** `go run main.go -identifier=workName -input-directory=${PWD}/work-creator/test/integration/samples`
* Edit samples to your liking
* Re-run work creator to update objects
** `go run main.go -identifier=workName -input-directory=${PWD}/work-creator/test/integration/samples`