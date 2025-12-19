package controller

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/internal/telemetry"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/writers"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	promiseReleaseNameLabel        = v1alpha1.KratixPrefix + "promise-release-name"
	removeAllWorkflowJobsFinalizer = v1alpha1.KratixPrefix + "workflows-cleanup"
	runDeleteWorkflowsFinalizer    = v1alpha1.KratixPrefix + "delete-workflows"
	// DefaultReconciliationInterval is the interval on which the workflows will be re-run.
	DefaultReconciliationInterval                       = time.Hour * 10
	secretRefFieldName                                  = "secretRef"
	StatusNotReady                                      = "NotReady"
	StatusReady                                         = "Ready"
	StateStoreReadyConditionType                        = "Ready"
	StateStoreReadyConditionReason                      = "StateStoreReady"
	StateStoreReadyConditionMessage                     = "State store is ready"
	StateStoreNotReadyErrorInitialisingWriterReason     = "ErrorInitialisingWriter"
	StateStoreNotReadyErrorInitialisingWriterMessage    = "Error initialising writer"
	StateStoreNotReadyErrorValidatingPermissionsReason  = "ErrorValidatingPermissions"
	StateStoreNotReadyErrorValidatingPermissionsMessage = "Error validating state store permissions"
)

var (
	newS3Writer func(logger logr.Logger, stateStoreSpec v1alpha1.BucketStateStoreSpec, destinationPath string,
		creds map[string][]byte) (writers.StateStoreWriter, error) = writers.NewS3Writer

	newGitWriter func(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string,
		creds map[string][]byte) (writers.StateStoreWriter, error) = writers.NewGitWriter
)

// SetStateStoreWriterFactories overrides the state store writer constructors for tests/examples.
// Pass nil to leave the existing constructor unchanged.
func SetStateStoreWriterFactories(
	s3 func(logr.Logger, v1alpha1.BucketStateStoreSpec, string, map[string][]byte) (writers.StateStoreWriter, error),
	git func(logr.Logger, v1alpha1.GitStateStoreSpec, string, map[string][]byte) (writers.StateStoreWriter, error),
) {
	if s3 != nil {
		newS3Writer = s3
	}
	if git != nil {
		newGitWriter = git
	}
}

type opts struct {
	ctx    context.Context
	client client.Client
	logger logr.Logger
}

// pass in nil resourceLabels to delete all resources of the GVK
func deleteAllResourcesWithKindMatchingLabel(o opts, gvk *schema.GroupVersionKind, resourceLabels map[string]string) (bool, error) {
	resourceList := &unstructured.UnstructuredList{}
	resourceList.SetGroupVersionKind(*gvk)
	listOptions := client.ListOptions{LabelSelector: labels.SelectorFromSet(resourceLabels)}
	err := o.client.List(o.ctx, resourceList, &listOptions)
	if err != nil {
		return true, err
	}

	if len(resourceList.Items) == 0 {
		logging.Debug(o.logger, "no resources found for deletion", "gvk", resourceList.GroupVersionKind(), "withLabels", resourceLabels)
		return false, nil
	}

	logging.Debug(o.logger, "deleting resources", "gvk", resourceList.GroupVersionKind(), "withLabels", resourceLabels, "resources", resourceutil.GetResourceNames(resourceList.Items))

	for _, resource := range resourceList.Items {
		deleteErr := o.client.Delete(o.ctx, &resource, client.PropagationPolicy(metav1.DeletePropagationBackground))
		logging.Trace(o.logger, "deleting resource", "resource", resource.GetName(), "gvk", resource.GroupVersionKind().String())
		if deleteErr != nil && !errors.IsNotFound(deleteErr) {
			logging.Warn(o.logger, "resource deletion will be retried", "name", resource.GetName(), "kind", resource.GetKind(), "error", deleteErr)
			return true, deleteErr
		}
		logging.Trace(o.logger, "deletion triggered", "name", resource.GetName(), "kind", resource.GetKind())
	}

	logging.Info(o.logger, "requested deletion for resources", "gvk", resourceList.GroupVersionKind(), "withLabels", resourceLabels, "count", len(resourceList.Items))

	return len(resourceList.Items) != 0, nil
}

func ensureTraceAnnotations(ctx context.Context, c client.Client, obj client.Object, parentAnnotations map[string]string) error {
	if parentAnnotations == nil {
		return nil
	}
	parentTrace := parentAnnotations[telemetry.TraceParentAnnotation]
	if parentTrace == "" {
		return nil
	}
	annotations := obj.GetAnnotations()
	if annotations != nil && annotations[telemetry.TraceParentAnnotation] != "" {
		return nil
	}

	original := obj.DeepCopyObject()
	obj.SetAnnotations(telemetry.CopyTraceAnnotations(annotations, parentAnnotations))
	if err := c.Patch(ctx, obj, client.MergeFrom(original.(client.Object))); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// finalizers must be less than 64 characters
func addFinalizers(o opts, resource client.Object, finalizers []string) error {
	logging.Info(o.logger, "adding missing finalizers",
		"expectedFinalizers", finalizers,
		"existingFinalizers", resource.GetFinalizers(),
	)
	for _, finalizer := range finalizers {
		controllerutil.AddFinalizer(resource, finalizer)
	}
	return o.client.Update(o.ctx, resource)
}

func newWriter(o opts, stateStoreName, stateStoreKind, destinationPath string) (writers.StateStoreWriter, error) {
	stateStoreRef := client.ObjectKey{
		Name: stateStoreName,
	}

	var writer writers.StateStoreWriter
	var err error
	switch stateStoreKind {
	case "BucketStateStore":
		stateStore := &v1alpha1.BucketStateStore{}
		secret, fetchErr := fetchObjectAndSecret(o, stateStoreRef, stateStore)
		if fetchErr != nil {
			return nil, fetchErr
		}
		var data map[string][]byte = nil
		if secret != nil {
			data = secret.Data
		}

		writer, err = newS3Writer(o.logger.WithName("writers").WithName("BucketStateStoreWriter"), stateStore.Spec, destinationPath, data)
	case "GitStateStore":
		stateStore := &v1alpha1.GitStateStore{}
		secret, fetchErr := fetchObjectAndSecret(o, stateStoreRef, stateStore)
		if fetchErr != nil {
			return nil, fetchErr
		}

		writer, err = newGitWriter(o.logger.WithName("writers").WithName("GitStateStoreWriter"), stateStore.Spec, destinationPath, secret.Data)
	default:
		return nil, fmt.Errorf("unsupported kind %s", stateStoreKind)
	}

	if err != nil {
		logging.Error(o.logger, err, "unable to create StateStoreWriter")
		return nil, err
	}
	return writer, nil
}

func shortID(id string) string {
	return id[0:5]
}

func logReconcileDuration(logger logr.Logger, start time.Time, result ctrl.Result, retErr error) func() {
	return func() {
		duration := time.Since(start)
		fields := []any{"duration", duration}
		if result.Requeue {
			fields = append(fields, "requeue", true)
		}
		if result.RequeueAfter > 0 {
			fields = append(fields, "requeueAfter", result.RequeueAfter)
		}
		if retErr != nil {
			logging.Error(logger, retErr, "reconciliation failed", fields...)
			return
		}
		logging.Info(logger, "reconciliation finished", fields...)
	}
}
