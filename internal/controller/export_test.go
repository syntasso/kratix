package controller

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/workflow"
	"github.com/syntasso/kratix/lib/writers"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

func PromiseForRevision(ctx context.Context, obj client.Object) []reconcile.Request {
	return promiseForRevision(ctx, obj)
}

func PromiseRevisionAnnotationChangedPredicate() predicate.Predicate {
	return promiseRevisionAnnotationChangedPredicate()
}
