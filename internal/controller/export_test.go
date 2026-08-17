package controller

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/workflow"
	"github.com/syntasso/kratix/lib/writers"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func SetReconcileConfigureWorkflow(f func(workflow.Opts) (bool, error)) {
	reconcileConfigure = f
}

func SetReconcileDeleteWorkflow(f func(workflow.Opts) (bool, error)) {
	reconcileDelete = f
}

func SetNewS3Writer(f func(logger logr.Logger, stateStoreSpec v1alpha1.BucketStateStoreSpec, destinationPath string,
	creds map[string][]byte) (writers.StateStoreWriter, error)) {
	newS3Writer = f
}

func ResourceNameLabelValue(resourceName string) string {
	return resourceNameLabelValue(resourceName)
}

func SetNewGitWriter(f func(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string,
	creds map[string][]byte) (writers.StateStoreWriter, error)) {
	newGitWriter = func(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string,
		creds map[string][]byte, _ ...writers.GitWriterOption) (writers.StateStoreWriter, error) {
		return f(logger, stateStoreSpec, destinationPath, creds)
	}
}

var ErrNoLatestPromiseRevisionYet = errNoLatestPromiseRevisionYet

func LatestRevision(ctx context.Context, c client.Client, promise *v1alpha1.Promise) (*v1alpha1.PromiseRevision, error) {
	return latestRevision(ctx, c, promise)
}
