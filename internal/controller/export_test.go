package controller

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/delivery"
	"github.com/syntasso/kratix/lib/workflow"
	"github.com/syntasso/kratix/lib/writers"
	ctrl "sigs.k8s.io/controller-runtime"
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

func WriterForDelivery(store StateStore, base writers.StateStoreWriter, creds map[string][]byte) (delivery.Writer, *StateStoreError) {
	return writerForDelivery(store, base, creds)
}

func WorkPlacementStatusUpdatePredicate() predicate.Funcs {
	return workPlacementStatusUpdatePredicate()
}

func PromiseForRevision(ctx context.Context, obj client.Object) []reconcile.Request {
	return promiseForRevision(ctx, obj)
}

func PromiseRevisionAnnotationChangedPredicate() predicate.Predicate {
	return promiseRevisionAnnotationChangedPredicate()
}

func WithTrace(logger logr.Logger, reconcileFunc func() (ctrl.Result, error)) (ctrl.Result, error) {
	return withTrace(logger, reconcileFunc)
}

var DryRunWorksFailure = worksFailure

const DryRunWorksTimeout = dryRunWorksTimeout

func WorkPlacementsForStateStore(r *WorkPlacementReconciler, stateStoreKind string) func(context.Context, client.Object) []reconcile.Request {
	return r.workPlacementsForStateStore(stateStoreKind)
}

func StateStoreObservedGenerationChangedPredicate() predicate.Funcs {
	return stateStoreObservedGenerationChanged()
}
