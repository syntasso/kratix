package controller

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
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
	promiseReleaseNameLabel                          = v1alpha1.KratixPrefix + "promise-release-name"
	removeAllWorkflowJobsFinalizer                   = v1alpha1.KratixPrefix + "workflows-cleanup"
	runDeleteWorkflowsFinalizer                      = v1alpha1.KratixPrefix + "delete-workflows"
	DefaultReconciliationInterval                    = time.Hour * 10
	secretRefFieldName                               = "secretRef"
	StatusNotReady                                   = "NotReady"
	StatusReady                                      = "Ready"
	StateStoreReadyConditionType                     = "Ready"
	StateStoreReadyConditionReason                   = "StateStoreReady"
	StateStoreReadyConditionMessage                  = "State store is ready"
	StateStoreNotReadyErrorInitialisingWriterReason  = "ErrorInitialisingWriter"
	StateStoreNotReadyErrorInitialisingWriterMessage = "Error initialising writer"
	StateStoreNotReadyErrorWritingTestFileReason     = "ErrorWritingTestFile"
	StateStoreNotReadyErrorWritingTestFileMessage    = "Error writing test file"
)

var (
	newS3Writer func(logger logr.Logger, stateStoreSpec v1alpha1.BucketStateStoreSpec, destinationPath string,
		creds map[string][]byte) (writers.StateStoreWriter, error) = writers.NewS3Writer

	newGitWriter func(logger logr.Logger, stateStoreSpec v1alpha1.GitStateStoreSpec, destinationPath string,
		creds map[string][]byte) (writers.StateStoreWriter, error) = writers.NewGitWriter
)

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
		o.logger.Info("no resources found for deletion", "gvk", resourceList.GroupVersionKind(), "withLabels", resourceLabels)
		return false, nil
	}

	o.logger.Info("deleting resources", "gvk", resourceList.GroupVersionKind(), "withLabels", resourceLabels, "resources", resourceutil.GetResourceNames(resourceList.Items))

	for _, resource := range resourceList.Items {
		err = o.client.Delete(o.ctx, &resource, client.PropagationPolicy(metav1.DeletePropagationBackground))
		o.logger.Info("deleting resource", "res", resource.GetName(), "gvk", resource.GroupVersionKind().String())
		if err != nil && !errors.IsNotFound(err) {
			o.logger.Error(err, "Error deleting resource, will try again in 5 seconds", "name", resource.GetName(), "kind", resource.GetKind())
			return true, err
		}
		o.logger.Info("successfully triggered deletion of resource", "name", resource.GetName(), "kind", resource.GetKind())
	}

	return len(resourceList.Items) != 0, nil
}

// finalizers must be less than 64 characters
func addFinalizers(o opts, resource client.Object, finalizers []string) (ctrl.Result, error) {
	o.logger.Info("Adding missing finalizers",
		"expectedFinalizers", finalizers,
		"existingFinalizers", resource.GetFinalizers(),
	)
	for _, finalizer := range finalizers {
		controllerutil.AddFinalizer(resource, finalizer)
	}
	if err := o.client.Update(o.ctx, resource); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
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
		o.logger.Error(err, "unable to create StateStoreWriter")
		return nil, err
	}
	return writer, nil
}

func shortID(id string) string {
	return id[0:5]
}
