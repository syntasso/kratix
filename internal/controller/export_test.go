package controller

import (
	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/workflow"
	"github.com/syntasso/kratix/lib/writers"
)

func SetReconcileConfigureWorkflow(f func(workflow.Opts) (bool, error)) {
	reconcileConfigure = f
}

func SetReconcileDeleteWorkflow(f func(workflow.Opts) (bool, error)) {
	reconcileDelete = f
}

func SetNewS3Writer(f func(logger logr.Logger, stateStoreSpec v1alpha1.BucketStateStoreSpec, destination v1alpha1.Destination,
	creds map[string][]byte) (writers.StateStoreWriter, error)) {
	newS3Writer = f
}

func SetNewGitWriter(f func(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, destination v1alpha1.Destination,
	creds map[string][]byte) (writers.StateStoreWriter, error)) {
	newGitWriter = f
}
