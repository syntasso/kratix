package controller

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

import (
	"context"
	"fmt"
	"sync"
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
	promiseLogKey                  = "promise"
	resourceRequestLogKey          = "resourceRequest"
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

	numJobsToKeepMu      sync.RWMutex
	numJobsToKeepGlobal  = 5

	reconciliationIntervalMu     sync.RWMutex
	reconciliationIntervalGlobal = DefaultReconciliationInterval
)

func SetNumberOfJobsToKeep(n int) {
	numJobsToKeepMu.Lock()
	defer numJobsToKeepMu.Unlock()
	numJobsToKeepGlobal = n
}

func getNumberOfJobsToKeep() int {
	numJobsToKeepMu.RLock()
	defer numJobsToKeepMu.RUnlock()
	return numJobsToKeepGlobal
}

func SetReconciliationInterval(d time.Duration) {
	reconciliationIntervalMu.Lock()
	defer reconciliationIntervalMu.Unlock()
	reconciliationIntervalGlobal = d
}

func getReconciliationInterval() time.Duration {
	reconciliationIntervalMu.RLock()
	defer reconciliationIntervalMu.RUnlock()
	return reconciliationIntervalGlobal
}

type opts struct {
	ctx    context.Context
	client client.Client
	logger logr.Logger
}

func withPromiseAndResourceRequest(logger logr.Logger, promiseName, resourceNamespace, resourceName string) logr.Logger {
	if promiseName != "" {
		logger = logger.WithValues(promiseLogKey, promiseName)
	}

	if promiseName == "" || resourceNamespace == "" || resourceName == "" {
		return logger
	}

	return logger.WithValues(resourceRequestLogKey, fmt.Sprintf("%s/%s/%s", promiseName, resourceNamespace, resourceName))
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

func ensureWorkflowRunsFromStart(ctx context.Context, c client.Client, obj client.Object) error {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[resourceutil.WorkflowRunFromStartLabel] = "true"
	delete(labels, v1alpha1.WorkflowSuspendedLabel)
	obj.SetLabels(labels)
	return c.Update(ctx, obj)
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

func withTrace(logger logr.Logger, reconcileFunc func() (ctrl.Result, error)) (result ctrl.Result, err error) {
	logging.Info(logger, "reconciliation started")

	defer logReconcileDuration(logger, time.Now(), result, err)()

	return reconcileFunc()
}
