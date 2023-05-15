package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// pass in nil resourceLabels to delete all resources of the GVK
func deleteAllResourcesWithKindMatchingLabel(ctx context.Context, kClient client.Client, gvk schema.GroupVersionKind, resourceLabels map[string]string, logger logr.Logger) (bool, error) {
	resourceList := &unstructured.UnstructuredList{}
	resourceList.SetGroupVersionKind(gvk)
	listOptions := client.ListOptions{LabelSelector: labels.SelectorFromSet(resourceLabels)}
	err := kClient.List(ctx, resourceList, &listOptions)
	if err != nil {
		return true, err
	}

	logger.Info("deleting resources", "kind", resourceList.GetKind(), "withLabels", resourceLabels, "resources", getResourceNames(resourceList.Items))

	for _, resource := range resourceList.Items {
		err = kClient.Delete(ctx, &resource)
		if err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "Error deleting resource, will try again in 5 seconds", "name", resource.GetName(), "kind", resource.GetKind())
			return true, err
		}
		logger.Info("successfully triggered deletion of resource", "name", resource.GetName(), "kind", resource.GetKind())
	}

	return len(resourceList.Items) != 0, nil
}

func getResourceNames(items []unstructured.Unstructured) []string {
	var names []string
	for _, item := range items {
		resource := item.GetName()
		//if the resurce is cluster scoped it has no namespace
		if item.GetNamespace() != "" {
			resource = fmt.Sprintf("%s/%s", item.GetNamespace(), item.GetName())
		}
		names = append(names, resource)
	}

	return names
}

// finalizers must be less than 64 characters
func addFinalizers(ctx context.Context, client client.Client, resource client.Object, finalizers []string, logger logr.Logger) (ctrl.Result, error) {
	logger.Info("Adding missing finalizers",
		"expectedFinalizers", finalizers,
		"existingFinalizers", resource.GetFinalizers(),
	)
	for _, finalizer := range finalizers {
		controllerutil.AddFinalizer(resource, finalizer)
	}
	if err := client.Update(ctx, resource); err != nil {
		return defaultRequeue, err
	}
	return fastRequeue, nil
}

func finalizersAreMissing(resource client.Object, finalizers []string) bool {
	for _, finalizer := range finalizers {
		if !controllerutil.ContainsFinalizer(resource, finalizer) {
			return true
		}
	}
	return false
}

func finalizersAreDeleted(resource client.Object, finalizers []string) bool {
	for _, finalizer := range finalizers {
		if controllerutil.ContainsFinalizer(resource, finalizer) {
			return false
		}
	}
	return true
}

func newStateStore(ctx context.Context, kubeClient client.Client, stateStoreRef types.NamespacedName, logger logr.Logger) (*platformv1alpha1.StateStore, error) {
	stateStore := &platformv1alpha1.StateStore{}
	if err := kubeClient.Get(ctx, stateStoreRef, stateStore); err != nil {
		logger.Error(err, "not found", "stateStoreRef", stateStoreRef)
		return nil, err
	}

	secret := &v1.Secret{}
	secretRef := types.NamespacedName{
		Name:      stateStore.Spec.SecretRef.Name,
		Namespace: or(stateStore.Spec.SecretRef.Namespace, stateStore.Namespace),
	}
	if err := kubeClient.Get(ctx, secretRef, secret); err != nil {
		logger.Error(err, "not found", "secretRef", secretRef)
		return nil, err
	}

	stateStore.SetCredentials(secret)
	return stateStore, nil
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
