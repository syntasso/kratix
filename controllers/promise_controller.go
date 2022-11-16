/*
Copyright 2021 Syntasso.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"encoding/json"
	"time"

	controllerutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultRequeue = 5 * time.Second
	// fastRequeue can be used whenever we want to quickly requeue, and we don't expect
	// an error to occur. Example: we delete a resource, we then requeue
	// to check it's been deleted. Here we can use a fastRequeue instead of a defaultRequeue
	fastRequeue = 1 * time.Second
)

// PromiseReconciler reconciles a Promise object
type PromiseReconciler struct {
	client.Client
	ApiextensionsClient *clientset.Clientset
	Log                 logr.Logger
	Manager             ctrl.Manager
	DynamicControllers  map[string]*bool
}

const (
	finalizerPrefix                                    = "kratix.io/"
	clusterSelectorsConfigMapCleanupFinalizer          = finalizerPrefix + "cluster-selectors-config-map-cleanup"
	resourceRequestCleanupFinalizer                    = finalizerPrefix + "resource-request-cleanup"
	dynamicControllerDependantResourcesCleaupFinalizer = finalizerPrefix + "dynamic-controller-dependant-resources-cleanup"
	crdCleanupFinalizer                                = finalizerPrefix + "crd-cleanup"
	workerClusterResourcesCleanupFinalizer             = finalizerPrefix + "worker-cluster-resources-cleanup"
)

var promiseFinalizers = []string{
	clusterSelectorsConfigMapCleanupFinalizer,
	resourceRequestCleanupFinalizer,
	dynamicControllerDependantResourcesCleaupFinalizer,
	crdCleanupFinalizer,
	workerClusterResourcesCleanupFinalizer,
}

//+kubebuilder:rbac:groups=platform.kratix.io,resources=promises,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=promises/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=promises/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=create;list;watch;delete

//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=create;escalate;bind;list;get;delete;watch
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=create;list;get;delete;watch
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;list;get;watch;delete

//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch;delete

func (r *PromiseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.Log.WithValues("promise", req.NamespacedName)

	promise := &v1alpha1.Promise{}
	err := r.Client.Get(ctx, req.NamespacedName, promise)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed getting Promise")
		return ctrl.Result{}, nil
	}

	configMapName := "cluster-selectors-" + promise.GetIdentifier()
	configMapNamespace := "default"

	//Instance-Level Reconciliation
	crdToCreate := &apiextensionsv1.CustomResourceDefinition{}
	err = json.Unmarshal(promise.Spec.XaasCrd.Raw, crdToCreate)
	if err != nil {
		logger.Error(err, "Failed unmarshalling CRD")
		return ctrl.Result{}, nil
	}
	crdToCreate.Labels = labels.Merge(crdToCreate.Labels, promise.GenerateSharedLabels())

	crdToCreateGvk := schema.GroupVersionKind{
		Group:   crdToCreate.Spec.Group,
		Version: crdToCreate.Spec.Versions[0].Name,
		Kind:    crdToCreate.Spec.Names.Kind,
	}

	if !promise.DeletionTimestamp.IsZero() {
		return r.deletePromise(ctx, promise, crdToCreateGvk, logger)
	}

	if finalizersAreMissing(promise, promiseFinalizers) {
		logger.Info("Adding missing finalizers",
			"expectedFinalizers", promiseFinalizers,
			"existingFinalizers", promise.GetFinalizers(),
		)
		return addFinalizers(ctx, r.Client, promise, promiseFinalizers, logger)
	}

	//this needs to be after RR deletion
	_, err = r.ApiextensionsClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Create(ctx, crdToCreate, metav1.CreateOptions{})
	if err != nil {
		if errors.IsAlreadyExists(err) {
			//todo test for existence and handle gracefully.
			logger.Info("CRD " + req.Name + " already exists")
		} else {
			logger.Error(err, "Error creating crd")
		}
	}

	// We should only proceed once the new gvk has been created in the API server
	if r.gvkDoesNotExist(crdToCreateGvk) {
		logger.Info("Requeue:" + crdToCreate.Name + " is not ready on the API server yet.")
		return ctrl.Result{RequeueAfter: defaultRequeue}, nil
	}

	workToCreate := &v1alpha1.Work{}
	workToCreate.Spec.Replicas = v1alpha1.WorkerResourceReplicas
	workToCreate.Name = promise.GetIdentifier()
	workToCreate.Namespace = "default"
	workToCreate.Labels = promise.GenerateSharedLabels()
	workToCreate.Spec.ClusterSelector = promise.Spec.ClusterSelector
	for _, u := range promise.Spec.WorkerClusterResources {
		workToCreate.Spec.Workload.Manifests = append(workToCreate.Spec.Workload.Manifests, v1alpha1.Manifest{Unstructured: u.Unstructured})
	}

	logger.Info("Creating Work resource for promise: " + promise.GetIdentifier())
	err = r.Client.Create(ctx, workToCreate)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			//todo test for existence and handle gracefully.
			logger.Info("Works " + promise.GetIdentifier() + " already exists")
		} else {
			logger.Error(err, "Error creating Works "+promise.GetIdentifier())
		}
		return ctrl.Result{}, err
	}

	// CONTROLLER RBAC
	cr := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   promise.GetControllerResourceName(),
			Labels: promise.GenerateSharedLabels(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{crdToCreateGvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural},
				Verbs:     []string{"get", "list", "update", "create", "patch", "delete", "watch"},
			},
			{
				APIGroups: []string{crdToCreateGvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural + "/finalizers"},
				Verbs:     []string{"update"},
			},
			{
				APIGroups: []string{crdToCreateGvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural + "/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
		},
	}
	err = r.Client.Create(ctx, &cr)
	if err != nil {
		logger.Error(err, "Error creating ClusterRole")
	}

	crb := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   promise.GetControllerResourceName(),
			Labels: promise.GenerateSharedLabels(),
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     cr.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: "kratix-platform-system",
				Name:      "kratix-platform-controller-manager",
			},
		},
	}
	err = r.Client.Create(ctx, &crb)
	if err != nil {
		logger.Error(err, "Error creating ClusterRoleBinding")
	}
	// END CONTROLLER RBAC

	// PIPELINE RBAC
	cr = rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   promise.GetPipelineResourceName(),
			Labels: promise.GenerateSharedLabels(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{crdToCreateGvk.Group},
				Resources: []string{crdToCreate.Spec.Names.Plural},
				Verbs:     []string{"get", "list", "update", "create", "patch"},
			},
			{
				APIGroups: []string{"platform.kratix.io"},
				Resources: []string{"works"},
				Verbs:     []string{"get", "update", "create", "patch"},
			},
		},
	}
	err = r.Client.Create(ctx, &cr)
	if err != nil {
		logger.Error(err, "Error creating ClusterRole")
	}

	logger.Info("Creating Service Account for " + promise.GetIdentifier())
	sa := v1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      promise.GetPipelineResourceName(),
			Namespace: "default",
			Labels:    promise.GenerateSharedLabels(),
		},
	}

	crb = rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   promise.GetPipelineResourceName(),
			Labels: promise.GenerateSharedLabels(),
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     cr.GetName(),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: "default",
				Name:      sa.GetName(),
			},
		},
	}
	err = r.Client.Create(ctx, &crb)
	if err != nil {
		logger.Error(err, "Error creating ClusterRoleBinding")
	}

	err = r.Client.Create(ctx, &sa)
	if err != nil {
		logger.Error(err, "Error creating ServiceAccount for Promise "+promise.GetIdentifier())
	} else {
		logger.Info("Created ServiceAccount for Promise " + promise.GetIdentifier())
	}

	configMap := v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: configMapNamespace,
			Labels:    promise.GenerateSharedLabels(),
		},
		Data: map[string]string{
			"selectors": labels.FormatLabels(promise.Spec.ClusterSelector),
		},
	}

	err = r.Client.Create(ctx, &configMap)
	if err != nil {
		logger.Error(err, "Error creating config map",
			"promiseIdentifier", promise.GetIdentifier(),
			"configMap", configMap.Name,
		)
	}

	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(crdToCreateGvk)

	//temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
	//once resolved, delete dynamic controller rather than disable
	enabled := true
	r.DynamicControllers[string(promise.GetUID())] = &enabled
	dynamicResourceRequestController := &dynamicResourceRequestController{
		client:                 r.Manager.GetClient(),
		scheme:                 r.Manager.GetScheme(),
		gvk:                    &crdToCreateGvk,
		promiseIdentifier:      promise.GetIdentifier(),
		promiseClusterSelector: promise.Spec.ClusterSelector,
		xaasRequestPipeline:    promise.Spec.XaasRequestPipeline,
		log:                    r.Log,
		uid:                    getShortUuid(),
		enabled:                &enabled,
	}

	ctrl.NewControllerManagedBy(r.Manager).
		For(unstructuredCRD).
		Complete(dynamicResourceRequestController)

	return ctrl.Result{}, nil
}

func (r *PromiseReconciler) gvkDoesNotExist(gvk schema.GroupVersionKind) bool {
	_, err := r.Manager.GetRESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version)
	return err != nil
}

func (r *PromiseReconciler) deletePromise(ctx context.Context, promise *v1alpha1.Promise, rrGVK schema.GroupVersionKind, logger logr.Logger) (ctrl.Result, error) {
	if finalizersAreDeleted(promise, promiseFinalizers) {
		return ctrl.Result{}, nil
	}

	if controllerutil.ContainsFinalizer(promise, resourceRequestCleanupFinalizer) {
		logger.Info("deleting resources associated with finalizer", "finalizer", resourceRequestCleanupFinalizer)
		err := r.deleteResourceRequests(ctx, promise, rrGVK, logger)
		if err != nil {
			return ctrl.Result{RequeueAfter: defaultRequeue}, err
		}
		return ctrl.Result{RequeueAfter: fastRequeue}, err
	}

	//temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
	//once resolved, delete dynamic controller rather than disable
	if enabled, exists := r.DynamicControllers[string(promise.GetUID())]; exists {
		*enabled = false
	}

	if controllerutil.ContainsFinalizer(promise, clusterSelectorsConfigMapCleanupFinalizer) {
		logger.Info("deleting resources associated with finalizer", "finalizer", clusterSelectorsConfigMapCleanupFinalizer)
		err := r.deleteConfigMap(ctx, promise, logger)
		if err != nil {
			return ctrl.Result{RequeueAfter: defaultRequeue}, err
		}
		return ctrl.Result{RequeueAfter: fastRequeue}, err
	}

	if controllerutil.ContainsFinalizer(promise, dynamicControllerDependantResourcesCleaupFinalizer) {
		logger.Info("deleting resources associated with finalizer", "finalizer", dynamicControllerDependantResourcesCleaupFinalizer)
		err := r.deleteDynamicControllerResources(ctx, promise, logger)
		if err != nil {
			return ctrl.Result{RequeueAfter: defaultRequeue}, err
		}
		return ctrl.Result{RequeueAfter: fastRequeue}, err
	}

	if controllerutil.ContainsFinalizer(promise, crdCleanupFinalizer) {
		logger.Info("deleting CRDs associated with finalizer", "finalizer", crdCleanupFinalizer)
		err := r.deleteCRDs(ctx, promise, logger)
		if err != nil {
			return ctrl.Result{RequeueAfter: defaultRequeue}, err
		}
		return ctrl.Result{RequeueAfter: fastRequeue}, err
	}

	if controllerutil.ContainsFinalizer(promise, workerClusterResourcesCleanupFinalizer) {
		logger.Info("deleting Work associated with finalizer", "finalizer", workerClusterResourcesCleanupFinalizer)
		err := r.deleteWork(ctx, promise, logger)
		if err != nil {
			return ctrl.Result{RequeueAfter: defaultRequeue}, err
		}
		return ctrl.Result{RequeueAfter: fastRequeue}, err
	}

	return ctrl.Result{RequeueAfter: fastRequeue}, nil
}

// crb + cr + sa things we create for the pipeline
// crb + cr attach to our existing service account
func (r *PromiseReconciler) deleteDynamicControllerResources(ctx context.Context, promise *v1alpha1.Promise, logger logr.Logger) error {
	var resourcesToDelete []schema.GroupVersionKind
	resourcesToDelete = append(resourcesToDelete, schema.GroupVersionKind{
		Group:   rbacv1.SchemeGroupVersion.Group,
		Version: rbacv1.SchemeGroupVersion.Version,
		Kind:    "ClusterRoleBinding",
	})

	resourcesToDelete = append(resourcesToDelete, schema.GroupVersionKind{
		Group:   rbacv1.SchemeGroupVersion.Group,
		Version: rbacv1.SchemeGroupVersion.Version,
		Kind:    "ClusterRole",
	})

	resourcesToDelete = append(resourcesToDelete, schema.GroupVersionKind{
		Group:   v1.SchemeGroupVersion.Group,
		Version: v1.SchemeGroupVersion.Version,
		Kind:    "ServiceAccount",
	})

	for _, gvk := range resourcesToDelete {
		resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(ctx, r.Client, gvk, promise.GenerateSharedLabels(), logger)
		if err != nil {
			return err
		}

		if resourcesRemaining {
			return nil
		}
	}

	controllerutil.RemoveFinalizer(promise, dynamicControllerDependantResourcesCleaupFinalizer)
	return r.Client.Update(ctx, promise)
}

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
		names = append(names, item.GetName())
	}

	return names
}

func (r *PromiseReconciler) deleteResourceRequests(ctx context.Context, promise *v1alpha1.Promise, rrGVK schema.GroupVersionKind, logger logr.Logger) error {
	// No need to pass labels since all resource requests are of Kind
	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(ctx, r.Client, rrGVK, nil, logger)
	if err != nil {
		return err
	}

	if !resourcesRemaining {
		controllerutil.RemoveFinalizer(promise, resourceRequestCleanupFinalizer)
		if err := r.Client.Update(ctx, promise); err != nil {
			return err
		}
	}

	return nil
}

func (r *PromiseReconciler) deleteConfigMap(ctx context.Context, promise *v1alpha1.Promise, logger logr.Logger) error {
	gvk := schema.GroupVersionKind{
		Group:   v1.SchemeGroupVersion.Group,
		Version: v1.SchemeGroupVersion.Version,
		Kind:    "ConfigMap",
	}

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(ctx, r.Client, gvk, promise.GenerateSharedLabels(), logger)
	if err != nil {
		return err
	}

	if !resourcesRemaining {
		controllerutil.RemoveFinalizer(promise, clusterSelectorsConfigMapCleanupFinalizer)
		return r.Client.Update(ctx, promise)
	}

	return nil
}

func (r *PromiseReconciler) deleteCRDs(ctx context.Context, promise *v1alpha1.Promise, logger logr.Logger) error {
	crdGVK := schema.GroupVersionKind{
		Group:   apiextensionsv1.SchemeGroupVersion.Group,
		Version: apiextensionsv1.SchemeGroupVersion.Version,
		Kind:    "CustomResourceDefinition",
	}

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(ctx, r.Client, crdGVK, promise.GenerateSharedLabels(), logger)
	if err != nil {
		return err
	}

	if !resourcesRemaining {
		controllerutil.RemoveFinalizer(promise, crdCleanupFinalizer)
		if err := r.Client.Update(ctx, promise); err != nil {
			return err
		}
	}

	return nil
}

func (r *PromiseReconciler) deleteWork(ctx context.Context, promise *v1alpha1.Promise, logger logr.Logger) error {
	workGVK := schema.GroupVersionKind{
		Group:   v1alpha1.GroupVersion.Group,
		Version: v1alpha1.GroupVersion.Version,
		Kind:    "Work",
	}

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(ctx, r.Client, workGVK, promise.GenerateSharedLabels(), logger)
	if err != nil {
		return err
	}

	if !resourcesRemaining {
		controllerutil.RemoveFinalizer(promise, workerClusterResourcesCleanupFinalizer)
		if err := r.Client.Update(ctx, promise); err != nil {
			return err
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromiseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Promise{}).
		Complete(r)
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
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: defaultRequeue}, nil
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
