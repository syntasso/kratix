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
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
	StateStoreNotReadySecretErrorReason              = "ErrorFetchingSecret"
	StateStoreNotReadySecretErrorMessage             = "Could not fetch Secret"
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

type StateStore interface {
	client.Object
	GetSecretRef() *v1.SecretReference
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

func fetchObjectAndSecret(o opts, stateStoreRef client.ObjectKey, stateStore StateStore) (*v1.Secret, error) {
	if err := o.client.Get(o.ctx, stateStoreRef, stateStore); err != nil {
		o.logger.Error(err, "unable to fetch resource", "resourceKind", stateStore.GetObjectKind(), "stateStoreRef", stateStoreRef)
		return nil, err
	}

	if stateStore.GetSecretRef() == nil {
		return nil, nil
	}

	namespace := stateStore.GetSecretRef().Namespace
	if namespace == "" {
		namespace = "default"
	}

	secret := &v1.Secret{}
	secretRef := types.NamespacedName{
		Name:      stateStore.GetSecretRef().Name,
		Namespace: namespace,
	}

	if err := o.client.Get(o.ctx, secretRef, secret); err != nil {
		o.logger.Error(err, "unable to fetch resource", "resourceKind", stateStore.GetObjectKind(), "secretRef", secretRef)
		return nil, err
	}

	return secret, nil
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

func writeStateStoreTestFile(writer writers.StateStoreWriter) error {
	content := fmt.Sprintf("This file tests that Kratix can write to this state store. Last write time: %s", time.Now().String())
	_, err := writer.UpdateFiles("", "kratix-write-probe", []v1alpha1.Workload{
		{
			Filepath: "kratix-write-probe.txt",
			Content:  content,
		},
	}, nil)

	return err
}

func shortID(id string) string {
	return id[0:5]
}

func secretRefIndexKey(secretName, secretNamespace string) string {
	return fmt.Sprintf("%s.%s", secretNamespace, secretName)
}

// reconcileStateStoreCommon contains the common logic for state store reconciliation.
func reconcileStateStoreCommon[T metav1.Object](
	o opts,
	stateStore T,
	secretRef *corev1.SecretReference,
	resourceType string,
	updateStatus func(stateStore T, failureReason string, failureMessage string, err error) error,
) (ctrl.Result, error) {
	if secretRef == nil {
		err := fmt.Errorf("secretRef is empty")
		if err := updateStatus(stateStore, StateStoreNotReadySecretErrorReason, StateStoreNotReadySecretErrorMessage, err); err != nil {
			o.logger.Error(err, "error updating state store status")
		}
		return ctrl.Result{}, err
	}

	namespace := secretRef.Namespace
	if namespace == "" {
		namespace = "default"
	}

	objectKey := types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: namespace,
	}

	secret := &corev1.Secret{}
	if err := o.client.Get(o.ctx, objectKey, secret); err != nil {
		if errors.IsNotFound(err) {
			err = fmt.Errorf("secret %q not found in namespace %q", secretRef.Name, namespace)
		}
		if err := updateStatus(stateStore, StateStoreNotReadySecretErrorReason, StateStoreNotReadySecretErrorMessage, err); err != nil {
			o.logger.Error(err, "error updating state store status")
		}
		return ctrl.Result{}, err
	}

	writer, err := newWriter(o, stateStore.GetName(), resourceType, "")
	if err != nil {
		if err := updateStatus(stateStore, StateStoreNotReadyErrorInitialisingWriterReason, StateStoreNotReadyErrorInitialisingWriterMessage, err); err != nil {
			o.logger.Error(err, "error updating state store status")
		}
		return ctrl.Result{}, err
	}

	if err := writeStateStoreTestFile(writer); err != nil {
		if err := updateStatus(stateStore, StateStoreNotReadyErrorWritingTestFileReason, StateStoreNotReadyErrorWritingTestFileMessage, err); err != nil {
			o.logger.Error(err, "error updating state store status")
		}
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, updateStatus(stateStore, "", "", nil)
}
