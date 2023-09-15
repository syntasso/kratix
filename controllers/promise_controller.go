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
	"fmt"
	"time"

	controllerutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
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

// PromiseReconciler reconciles a Promise object
type PromiseReconciler struct {
	Client                    client.Client
	ApiextensionsClient       *clientset.Clientset
	Log                       logr.Logger
	Manager                   ctrl.Manager
	StartedDynamicControllers map[string]*dynamicResourceRequestController
	RestartManager            func()
}

const (
	kratixPrefix                                       = "kratix.io/"
	resourceRequestCleanupFinalizer                    = kratixPrefix + "resource-request-cleanup"
	dynamicControllerDependantResourcesCleaupFinalizer = kratixPrefix + "dynamic-controller-dependant-resources-cleanup"
	crdCleanupFinalizer                                = kratixPrefix + "api-crd-cleanup"
	dependenciesCleanupFinalizer                       = kratixPrefix + "dependencies-cleanup"
)

var (
	promiseFinalizers = []string{
		resourceRequestCleanupFinalizer,
		dynamicControllerDependantResourcesCleaupFinalizer,
		crdCleanupFinalizer,
		dependenciesCleanupFinalizer,
	}

	// fastRequeue can be used whenever we want to quickly requeue, and we don't expect
	// an error to occur. Example: we delete a resource, we then requeue
	// to check it's been deleted. Here we can use a fastRequeue instead of a defaultRequeue
	fastRequeue    = ctrl.Result{RequeueAfter: 1 * time.Second}
	defaultRequeue = ctrl.Result{RequeueAfter: 5 * time.Second}
	slowRequeue    = ctrl.Result{RequeueAfter: 15 * time.Second}
)

//+kubebuilder:rbac:groups=platform.kratix.io,resources=promises,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=platform.kratix.io,resources=promises/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=platform.kratix.io,resources=promises/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=create;update;list;watch;delete

//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=create;update;escalate;bind;list;get;delete;watch
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=create;update;list;get;delete;watch
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=create;update;escalate;bind;list;get;delete;watch
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=create;update;list;get;delete;watch
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;update;list;get;watch;delete

//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch;delete

func (r *PromiseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.StartedDynamicControllers == nil {
		r.StartedDynamicControllers = make(map[string]*dynamicResourceRequestController)
	}
	promise := &v1alpha1.Promise{}
	err := r.Client.Get(ctx, req.NamespacedName, promise)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		r.Log.Error(err, "Failed getting Promise", "namespacedName", req.NamespacedName)
		return defaultRequeue, nil
	}

	logger := r.Log.WithValues("identifier", promise.GetName())

	if !promise.DeletionTimestamp.IsZero() {
		return r.deletePromise(ctx, promise, logger)
	}

	var rrCRD *apiextensionsv1.CustomResourceDefinition
	var rrGVK schema.GroupVersionKind

	if promise.ContainsAPI() {
		rrCRD, rrGVK, err = generateCRDAndGVK(promise, logger)
		if err != nil {
			return ctrl.Result{}, err
		}

		err := r.ensureCRDExists(ctx, rrCRD, rrGVK, logger)
		if err != nil {
			return ctrl.Result{}, err
		}

		if doesNotContainFinalizer(promise, crdCleanupFinalizer) {
			return addFinalizers(ctx, r.Client, promise, []string{crdCleanupFinalizer}, logger)
		}

		if err := r.createResourcesForDynamicControllerIfTheyDontExist(ctx, promise, rrCRD, rrGVK, logger); err != nil {
			// TODO add support for updates
			return ctrl.Result{}, err
		}

		if doesNotContainFinalizer(promise, dynamicControllerDependantResourcesCleaupFinalizer) {
			return addFinalizers(ctx, r.Client, promise, []string{dynamicControllerDependantResourcesCleaupFinalizer}, logger)
		}
	}

	if err := r.applyWorkResourceForDependencies(ctx, promise, logger); err != nil {
		logger.Error(err, "Error creating Works")
		return ctrl.Result{}, err
	}

	if doesNotContainFinalizer(promise, dependenciesCleanupFinalizer) {
		return addFinalizers(ctx, r.Client, promise, []string{dependenciesCleanupFinalizer}, logger)
	}

	if promise.DoesNotContainAPI() {
		logger.Info("Promise only contains dependencies, skipping creation of API and dynamic controller")
		return ctrl.Result{}, nil
	}

	configurePipelines, deletePipelines, err := r.generatePipelines(promise, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.ensureDynamicControllerIsStarted(promise, rrCRD, rrGVK, configurePipelines, deletePipelines, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	if doesNotContainFinalizer(promise, resourceRequestCleanupFinalizer) {
		return addFinalizers(ctx, r.Client, promise, []string{resourceRequestCleanupFinalizer}, logger)
	}

	if promise.GetGeneration() != promise.Status.ObservedGeneration {
		if err := r.reconcileAllRRs(rrGVK); err != nil {
			return ctrl.Result{}, err
		}
		promise.Status.ObservedGeneration = promise.GetGeneration()
		return ctrl.Result{}, r.Client.Status().Update(ctx, promise)
	}

	return ctrl.Result{}, nil
}

func (r *PromiseReconciler) reconcileAllRRs(rrGVK schema.GroupVersionKind) error {
	//label all rr with manual reocnciliation
	rrs := &unstructured.UnstructuredList{}
	rrListGVK := rrGVK
	rrListGVK.Kind = rrListGVK.Kind + "List"
	rrs.SetGroupVersionKind(rrListGVK)
	err := r.Client.List(context.Background(), rrs)
	if err != nil {
		return err
	}
	for _, rr := range rrs.Items {
		newLabels := rr.GetLabels()
		if newLabels == nil {
			newLabels = make(map[string]string)
		}
		newLabels[resourceutil.ManualReconciliationLabel] = "true"
		rr.SetLabels(newLabels)
		if err := r.Client.Update(context.TODO(), &rr); err != nil {
			return err
		}
	}
	return nil
}

func (r *PromiseReconciler) ensureDynamicControllerIsStarted(promise *v1alpha1.Promise, rrCRD *apiextensionsv1.CustomResourceDefinition, rrGVK schema.GroupVersionKind, configurePipelines, deletePipelines []v1alpha1.Pipeline, logger logr.Logger) error {
	// The Dynamic Controller needs to be started once and only once.
	if r.dynamicControllerHasAlreadyStarted(promise) {
		logger.Info("dynamic controller already started")

		dynamicController := r.StartedDynamicControllers[string(promise.GetUID())]
		dynamicController.deletePipelines = deletePipelines
		dynamicController.configurePipelines = configurePipelines
		dynamicController.gvk = &rrGVK
		dynamicController.crd = rrCRD

		return nil
	}
	logger.Info("starting dynamic controller")

	//temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
	//once resolved, delete dynamic controller rather than disable
	enabled := true
	dynamicResourceRequestController := &dynamicResourceRequestController{
		Client:                      r.Manager.GetClient(),
		scheme:                      r.Manager.GetScheme(),
		gvk:                         &rrGVK,
		crd:                         rrCRD,
		promiseIdentifier:           promise.GetName(),
		configurePipelines:          configurePipelines,
		deletePipelines:             deletePipelines,
		promiseDestinationSelectors: promise.Spec.DestinationSelectors,
		log:                         r.Log.WithName(promise.GetName()),
		uid:                         string(promise.GetUID())[0:5],
		enabled:                     &enabled,
	}
	r.StartedDynamicControllers[string(promise.GetUID())] = dynamicResourceRequestController

	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(rrGVK)

	return ctrl.NewControllerManagedBy(r.Manager).
		For(unstructuredCRD).
		Complete(dynamicResourceRequestController)
}

func (r *PromiseReconciler) dynamicControllerHasAlreadyStarted(promise *v1alpha1.Promise) bool {
	_, ok := r.StartedDynamicControllers[string(promise.GetUID())]
	return ok
}

func (r *PromiseReconciler) createResourcesForDynamicControllerIfTheyDontExist(ctx context.Context, promise *v1alpha1.Promise,
	rrCRD *apiextensionsv1.CustomResourceDefinition, rrGVK schema.GroupVersionKind, logger logr.Logger) error {
	cr := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   promise.GetControllerResourceName(),
			Labels: promise.GenerateSharedLabels(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{rrGVK.Group},
				Resources: []string{rrCRD.Spec.Names.Plural},
				Verbs:     []string{rbacv1.VerbAll},
			},
			{
				APIGroups: []string{rrGVK.Group},
				Resources: []string{rrCRD.Spec.Names.Plural + "/finalizers"},
				Verbs:     []string{"update"},
			},
			{
				APIGroups: []string{rrGVK.Group},
				Resources: []string{rrCRD.Spec.Names.Plural + "/status"},
				Verbs:     []string{"get", "update", "patch"},
			},
		},
	}
	logger.Info("creating cluster role if it doesn't exist", "clusterRoleName", cr.GetName())
	err := r.Client.Create(ctx, &cr)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			// TODO: Handle updates of all Promise resources gracefully.
			logger.Info("Cannot execute update on pre-existing ClusterRole")
		} else {
			logger.Error(err, "Error creating ClusterRole")
			return err
		}
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
				Namespace: v1alpha1.KratixSystemNamespace,
				Name:      "kratix-platform-controller-manager",
			},
		},
	}

	logger.Info("creating cluster role binding if it doesn't exist", "clusterRoleBinding", crb.GetName())
	err = r.Client.Create(ctx, &crb)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			// TODO: Handle updates of all Promise resources gracefully.
			logger.Info("Cannot execute update on pre-existing ClusterRoleBinding")
		} else {
			logger.Error(err, "Error creating ClusterRoleBinding")
			return err
		}
	}

	logger.Info("finished creating resources for dynamic controller")
	return nil
}

func (r *PromiseReconciler) ensureCRDExists(ctx context.Context, rrCRD *apiextensionsv1.CustomResourceDefinition,
	rrGVK schema.GroupVersionKind, logger logr.Logger) error {

	_, err := r.ApiextensionsClient.ApiextensionsV1().
		CustomResourceDefinitions().
		Create(ctx, rrCRD, metav1.CreateOptions{})
	if err != nil {
		if errors.IsAlreadyExists(err) {
			//todo test for existence and handle gracefully.
			logger.Info("CRD already exists", "crdName", rrCRD.Name)
			existingCRD, err := r.ApiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, rrCRD.GetName(), metav1.GetOptions{})
			if err != nil {
				return err
			}

			existingCRD.Spec.Versions = rrCRD.Spec.Versions
			existingCRD.Spec.Conversion = rrCRD.Spec.Conversion
			existingCRD.Spec.PreserveUnknownFields = rrCRD.Spec.PreserveUnknownFields
			_, err = r.ApiextensionsClient.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, existingCRD, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
		} else {
			logger.Error(err, "Error creating crd")
		}
	}

	_, err = r.Manager.GetRESTMapper().RESTMapping(rrGVK.GroupKind(), rrGVK.Version)
	return err
}

func (r *PromiseReconciler) deletePromise(ctx context.Context, promise *v1alpha1.Promise, logger logr.Logger) (ctrl.Result, error) {
	if finalizersAreDeleted(promise, promiseFinalizers) {
		return ctrl.Result{}, nil
	}

	if controllerutil.ContainsFinalizer(promise, resourceRequestCleanupFinalizer) {
		logger.Info("deleting resources associated with finalizer", "finalizer", resourceRequestCleanupFinalizer)
		err := r.deleteResourceRequests(ctx, promise, logger)
		if err != nil {
			return defaultRequeue, err
		}
		return fastRequeue, nil
	}

	//temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
	//once resolved, delete dynamic controller rather than disable
	if d, exists := r.StartedDynamicControllers[string(promise.GetUID())]; exists {
		r.RestartManager()
		enabled := false
		d.enabled = &enabled
	}

	if controllerutil.ContainsFinalizer(promise, dynamicControllerDependantResourcesCleaupFinalizer) {
		logger.Info("deleting resources associated with finalizer", "finalizer", dynamicControllerDependantResourcesCleaupFinalizer)
		err := r.deleteDynamicControllerResources(ctx, promise, logger)
		if err != nil {
			return defaultRequeue, err
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(promise, dependenciesCleanupFinalizer) {
		logger.Info("deleting Work associated with finalizer", "finalizer", dependenciesCleanupFinalizer)
		err := r.deleteWork(ctx, promise, logger)
		if err != nil {
			return defaultRequeue, err
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(promise, crdCleanupFinalizer) {
		logger.Info("deleting CRDs associated with finalizer", "finalizer", crdCleanupFinalizer)
		err := r.deleteCRDs(ctx, promise, logger)
		if err != nil {
			return defaultRequeue, err
		}
		return fastRequeue, nil
	}

	return fastRequeue, nil
}

func (r *PromiseReconciler) deleteDynamicControllerResources(ctx context.Context, promise *v1alpha1.Promise, logger logr.Logger) error {
	resourcesToDelete := map[schema.GroupVersion][]string{
		rbacv1.SchemeGroupVersion: {"ClusterRoleBinding", "ClusterRole", "RoleBinding", "Role"},
		v1.SchemeGroupVersion:     {"ServiceAccount", "ConfigMap"},
	}

	for gv, toDelete := range resourcesToDelete {
		for _, resource := range toDelete {
			gvk := schema.GroupVersionKind{
				Group:   gv.Group,
				Version: gv.Version,
				Kind:    resource,
			}
			resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(ctx, r.Client, gvk, promise.GenerateSharedLabels(), logger)
			if err != nil {
				return err
			}

			if resourcesRemaining {
				return nil
			}
		}
	}

	controllerutil.RemoveFinalizer(promise, dynamicControllerDependantResourcesCleaupFinalizer)
	return r.Client.Update(ctx, promise)
}

func (r *PromiseReconciler) deleteResourceRequests(ctx context.Context, promise *v1alpha1.Promise, logger logr.Logger) error {
	_, rrGVK, err := generateCRDAndGVK(promise, logger)
	if err != nil {
		return err
	}

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
		controllerutil.RemoveFinalizer(promise, dependenciesCleanupFinalizer)
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

func generateCRDAndGVK(promise *v1alpha1.Promise, logger logr.Logger) (*apiextensionsv1.CustomResourceDefinition, schema.GroupVersionKind, error) {
	rrCRD := &apiextensionsv1.CustomResourceDefinition{}
	rrGVK := schema.GroupVersionKind{}

	rrCRD, err := promise.GetAPIAsCRD()
	if err != nil {
		logger.Error(err, "Failed unmarshalling CRD")
		return rrCRD, rrGVK, err
	}
	rrCRD.Labels = labels.Merge(rrCRD.Labels, promise.GenerateSharedLabels())

	setStatusFieldsOnCRD(rrCRD)

	storedVersion := rrCRD.Spec.Versions[0]
	for _, version := range rrCRD.Spec.Versions {
		if version.Storage {
			storedVersion = version
			break
		}
	}

	rrGVK = schema.GroupVersionKind{
		Group:   rrCRD.Spec.Group,
		Version: storedVersion.Name,
		Kind:    rrCRD.Spec.Names.Kind,
	}

	return rrCRD, rrGVK, nil
}

func setStatusFieldsOnCRD(rrCRD *apiextensionsv1.CustomResourceDefinition) {
	for i := range rrCRD.Spec.Versions {
		rrCRD.Spec.Versions[i].Subresources = &apiextensionsv1.CustomResourceSubresources{
			Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
		}

		rrCRD.Spec.Versions[i].AdditionalPrinterColumns = []apiextensionsv1.CustomResourceColumnDefinition{
			{
				Name:     "status",
				Type:     "string",
				JSONPath: ".status.message",
			},
		}

		rrCRD.Spec.Versions[i].Schema.OpenAPIV3Schema.Properties["status"] = apiextensionsv1.JSONSchemaProps{
			Type:                   "object",
			XPreserveUnknownFields: &[]bool{true}[0], // pointer to bool
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				"message": {
					Type: "string",
				},
				"conditions": {
					Type: "array",
					Items: &apiextensionsv1.JSONSchemaPropsOrArray{
						Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"lastTransitionTime": {
									Type:   "string",
									Format: "datetime", //RFC3339
								},
								"message": {
									Type: "string",
								},
								"reason": {
									Type: "string",
								},
								"status": {
									Type: "string",
								},
								"type": {
									Type: "string",
								},
							},
						},
					},
				},
			},
		}
	}
}

func (r *PromiseReconciler) applyWorkResourceForDependencies(ctx context.Context, promise *v1alpha1.Promise, logger logr.Logger) error {
	work, err := v1alpha1.NewPromiseDependenciesWork(promise)
	if err != nil {
		return err
	}

	workCopy := work.DeepCopy()

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, work, func() error {
		work.ObjectMeta.Labels = workCopy.ObjectMeta.Labels
		work.Spec = workCopy.Spec
		return nil
	})

	if err != nil {
		return err
	}

	logger.Info("resource reconciled", "operation", op, "namespace", work.GetNamespace(), "name", work.GetName(), "gvk", work.GroupVersionKind())
	return nil
}

func (r *PromiseReconciler) generatePipelines(promise *v1alpha1.Promise, logger logr.Logger) ([]v1alpha1.Pipeline, []v1alpha1.Pipeline, error) {
	var configurePipelines []v1alpha1.Pipeline

	for _, pipeline := range promise.Spec.Workflows.Resource.Configure {
		p, err := generatePipeline(pipeline, logger)
		if err != nil {
			return nil, nil, err
		}
		configurePipelines = append(configurePipelines, p)
	}

	var deletePipelines []v1alpha1.Pipeline
	for _, pipeline := range promise.Spec.Workflows.Resource.Delete {
		p, err := generatePipeline(pipeline, logger)
		if err != nil {
			return nil, nil, err
		}
		deletePipelines = append(deletePipelines, p)
	}

	return configurePipelines, deletePipelines, nil
}

func generatePipeline(pipeline unstructured.Unstructured, logger logr.Logger) (v1alpha1.Pipeline, error) {
	pipelineLogger := logger.WithValues(
		"pipelineKind", pipeline.GetKind(),
		"pipelineVersion", pipeline.GetAPIVersion(),
		"pipelineName", pipeline.GetName())

	if pipeline.GetKind() == "Pipeline" && pipeline.GetAPIVersion() == "platform.kratix.io/v1alpha1" {
		jsonPipeline, err := pipeline.MarshalJSON()
		if err != nil {
			// TODO test
			pipelineLogger.Error(err, "Failed marshalling pipeline to json")
			return v1alpha1.Pipeline{}, err
		}

		p := v1alpha1.Pipeline{}
		err = json.Unmarshal(jsonPipeline, &p)
		if err != nil {
			// TODO test
			pipelineLogger.Error(err, "Failed unmarshalling pipeline")
			return v1alpha1.Pipeline{}, err
		}

		return p, nil
	}

	return v1alpha1.Pipeline{}, fmt.Errorf("unsupported pipeline %q (%s.%s)",
		pipeline.GetName(), pipeline.GetKind(), pipeline.GetAPIVersion())
}
