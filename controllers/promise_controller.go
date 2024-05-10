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
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/pipeline"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1cs "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kmanager "sigs.k8s.io/controller-runtime/pkg/manager"
)

var reconcileConfigure = workflow.ReconcileConfigure
var reconcileDelete = workflow.ReconcileDelete

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 . Manager
type Manager interface {
	kmanager.Manager
}

// PromiseReconciler reconciles a Promise object
type PromiseReconciler struct {
	Scheme                    *runtime.Scheme
	Client                    client.Client
	ApiextensionsClient       apiextensionsv1cs.CustomResourceDefinitionsGetter
	Log                       logr.Logger
	Manager                   ctrl.Manager
	StartedDynamicControllers map[string]*DynamicResourceRequestController
	RestartManager            func()
}

const (
	resourceRequestCleanupFinalizer = v1alpha1.KratixPrefix + "resource-request-cleanup"
	// TODO fix the name of this finalizer: dependant -> dependent (breaking change)
	dynamicControllerDependantResourcesCleanupFinalizer = v1alpha1.KratixPrefix + "dynamic-controller-dependant-resources-cleanup"
	crdCleanupFinalizer                                 = v1alpha1.KratixPrefix + "api-crd-cleanup"
	dependenciesCleanupFinalizer                        = v1alpha1.KratixPrefix + "dependencies-cleanup"

	requirementStateInstalled                      = "Requirement installed"
	requirementStateNotInstalled                   = "Requirement not installed"
	requirementStateNotInstalledAtSpecifiedVersion = "Requirement not installed at the specified version"
	requirementUnknownInstallationState            = "Requirement state unknown"
)

var (
	promiseFinalizers = []string{
		resourceRequestCleanupFinalizer,
		dynamicControllerDependantResourcesCleanupFinalizer,
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
		r.StartedDynamicControllers = make(map[string]*DynamicResourceRequestController)
	}
	promise := &v1alpha1.Promise{}
	err := r.Client.Get(ctx, req.NamespacedName, promise)

	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if client.IgnoreNotFound(err) != nil {
		r.Log.Error(err, "Failed getting Promise", "namespacedName", req.NamespacedName)
		return defaultRequeue, nil
	}

	originalStatus := promise.Status.Status

	logger := r.Log.WithValues("identifier", promise.GetName())

	opts := opts{
		client: r.Client,
		ctx:    ctx,
		logger: logger,
	}

	pipelines, err := promise.GeneratePipelines(logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	if !promise.DeletionTimestamp.IsZero() {
		return r.deletePromise(opts, promise, pipelines.DeletePromise)
	}

	if value, found := promise.Labels[v1alpha1.PromiseVersionLabel]; found {
		if promise.Status.Version != value {
			promise.Status.Version = value
			return ctrl.Result{}, r.Client.Status().Update(ctx, promise)
		}
	}

	//Set status to unavailable, at the end of this function we set it to
	//available. If at anytime we return early, it persisted as unavailable
	promise.Status.Status = v1alpha1.PromiseStatusUnavailable
	updated, err := r.ensureRequiredPromiseStatusIsUpToDate(ctx, promise)
	if err != nil || updated {
		return ctrl.Result{}, err
	}

	//TODO handle removing finalizer
	requeue, err := ensurePromiseDeleteWorkflowFinalizer(opts, promise, pipelines.DeletePromise)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue != nil {
		return *requeue, nil
	}

	//TODO add workflowFinalizzer if deletes exist (currently we only add it if we have a configure pipeline)

	var rrCRD *apiextensionsv1.CustomResourceDefinition
	var rrGVK schema.GroupVersionKind

	if promise.ContainsAPI() {
		rrCRD, rrGVK, err = generateCRDAndGVK(promise, logger)
		if err != nil {
			return ctrl.Result{}, err
		}

		requeue, err := r.ensureCRDExists(ctx, promise, rrCRD, rrGVK, logger)
		if err != nil {
			return ctrl.Result{}, err
		}

		if requeue != nil {
			return *requeue, nil
		}

		if resourceutil.DoesNotContainFinalizer(promise, crdCleanupFinalizer) {
			return addFinalizers(opts, promise, []string{crdCleanupFinalizer})
		}

		if err := r.createResourcesForDynamicControllerIfTheyDontExist(ctx, promise, rrCRD, rrGVK, logger); err != nil {
			// TODO add support for updates
			return ctrl.Result{}, err
		}

		if resourceutil.DoesNotContainFinalizer(promise, dynamicControllerDependantResourcesCleanupFinalizer) {
			return addFinalizers(opts, promise, []string{dynamicControllerDependantResourcesCleanupFinalizer})
		}
	}

	if resourceutil.DoesNotContainFinalizer(promise, dependenciesCleanupFinalizer) {
		return addFinalizers(opts, promise, []string{dependenciesCleanupFinalizer})
	}

	requeue, err = r.reconcileDependencies(opts, promise, pipelines.ConfigurePromise)
	if err != nil {
		return ctrl.Result{}, err
	}

	if requeue != nil {
		return *requeue, nil
	}

	if promise.ContainsAPI() {
		dynamicControllerCanCreateResources := true
		for _, req := range promise.Status.RequiredPromises {
			if req.State != requirementStateInstalled {
				logger.Info("requirement not installed, disabling dynamic controller", "requirement", req)
				dynamicControllerCanCreateResources = false
			}
		}

		err = r.ensureDynamicControllerIsStarted(promise, rrCRD, rrGVK, pipelines.ConfigureResource, pipelines.DeleteResource, &dynamicControllerCanCreateResources, logger)
		if err != nil {
			return ctrl.Result{}, err
		}

		if resourceutil.DoesNotContainFinalizer(promise, resourceRequestCleanupFinalizer) {
			return addFinalizers(opts, promise, []string{resourceRequestCleanupFinalizer})
		}

		if !dynamicControllerCanCreateResources {
			logger.Info("requirements not fulfilled, disabled dynamic controller and requeuing", "requirementsStatus", promise.Status.RequiredPromises)
			return slowRequeue, nil
		}

		logger.Info("requirements are fulfilled", "requirementsStatus", promise.Status.RequiredPromises)

		if promise.GetGeneration() != promise.Status.ObservedGeneration {
			if err := r.reconcileAllRRs(rrGVK); err != nil {
				return ctrl.Result{}, err
			}
			promise.Status.ObservedGeneration = promise.GetGeneration()
			return ctrl.Result{}, r.Client.Status().Update(ctx, promise)
		}
	} else {
		logger.Info("Promise only contains dependencies, skipping creation of API and dynamic controller")
	}

	if originalStatus == v1alpha1.PromiseStatusAvailable {
		return ctrl.Result{}, nil
	}
	logger.Info("Promise status being set to Available")
	promise.Status.Status = v1alpha1.PromiseStatusAvailable
	return ctrl.Result{}, r.Client.Status().Update(ctx, promise)
}

func (r *PromiseReconciler) ensureRequiredPromiseStatusIsUpToDate(ctx context.Context, promise *v1alpha1.Promise) (bool, error) {
	latestCondition, latestRequirements := r.generateStatusAndMarkRequirements(ctx, promise)

	requirementsFieldChanged := updateRequirementsStatusOnPromise(promise, promise.Status.RequiredPromises, latestRequirements)
	conditionsFieldChanged := updateConditionOnPromise(promise, latestCondition)

	if conditionsFieldChanged || requirementsFieldChanged {
		return true, r.Client.Status().Update(ctx, promise)
	}

	return false, nil
}

func updateConditionOnPromise(promise *v1alpha1.Promise, latestCondition metav1.Condition) bool {
	for i, condition := range promise.Status.Conditions {
		if condition.Type == latestCondition.Type {
			if condition.Status != latestCondition.Status {
				promise.Status.Conditions[i] = latestCondition
				return true
			}
			return false
		}
	}
	promise.Status.Conditions = append(promise.Status.Conditions, latestCondition)
	return true
}

func updateRequirementsStatusOnPromise(promise *v1alpha1.Promise, oldReqs, newReqs []v1alpha1.RequiredPromiseStatus) bool {
	compareValue := slices.CompareFunc(oldReqs, newReqs, func(a, b v1alpha1.RequiredPromiseStatus) int {
		if a.Name == b.Name && a.Version == b.Version && a.State == b.State {
			return 0
		}
		return -1
	})
	if compareValue != 0 {
		promise.Status.RequiredPromises = newReqs
		return true
	}
	return false
}

func (r *PromiseReconciler) generateStatusAndMarkRequirements(ctx context.Context, promise *v1alpha1.Promise) (metav1.Condition, []v1alpha1.RequiredPromiseStatus) {
	promiseCondition := metav1.Condition{
		Type:               "RequirementsFulfilled",
		LastTransitionTime: metav1.NewTime(time.Now()),
		Status:             metav1.ConditionTrue,
		Message:            "Requirements fulfilled",
		Reason:             "RequirementsInstalled",
	}

	requirements := []v1alpha1.RequiredPromiseStatus{}

	for _, requirement := range promise.Spec.RequiredPromises {
		requirementState := requirementStateInstalled
		requiredPromise := &v1alpha1.Promise{}
		err := r.Client.Get(ctx, types.NamespacedName{Name: requirement.Name}, requiredPromise)
		if err != nil {
			promiseCondition.Reason = "RequirementsNotInstalled"
			if errors.IsNotFound(err) && promiseCondition.Status != metav1.ConditionUnknown {
				requirementState = requirementStateNotInstalled
				promiseCondition.Status = metav1.ConditionFalse
				promiseCondition.Message = "Requirements not fulfilled"
			} else {
				requirementState = requirementUnknownInstallationState
				promiseCondition.Status = metav1.ConditionUnknown
				promiseCondition.Message = "Unable to determine if requirements are fulfilled"
			}
		} else {
			if requiredPromise.Status.Version != requirement.Version || requiredPromise.Status.Status != v1alpha1.PromiseStatusAvailable {
				promiseCondition.Reason = "RequirementsNotInstalled"
				requirementState = requirementStateNotInstalledAtSpecifiedVersion

				if promiseCondition.Status != metav1.ConditionUnknown {
					promiseCondition.Status = metav1.ConditionFalse
					promiseCondition.Message = "Requirements not fulfilled"
				}
			}

			r.markRequiredPromiseAsRequired(ctx, requirement.Version, promise, requiredPromise)
		}

		requirements = append(requirements, v1alpha1.RequiredPromiseStatus{
			Name:    requirement.Name,
			Version: requirement.Version,
			State:   requirementState,
		})
	}

	return promiseCondition, requirements
}

func (r *PromiseReconciler) reconcileDependencies(o opts, promise *v1alpha1.Promise, configurePipeline []v1alpha1.Pipeline) (*ctrl.Result, error) {
	o.logger.Info("Applying static dependencies for Promise", "promise", promise.GetName())
	if len(promise.Spec.Dependencies) > 0 {
		if err := r.applyWorkForDependencies(o, promise); err != nil {
			o.logger.Error(err, "Error creating Works")
			return nil, err
		}
	}
	if len(configurePipeline) == 0 {
		return nil, nil
	}

	//TODO remove finalizer if we don't have any configure (or delete?)
	if resourceutil.DoesNotContainFinalizer(promise, removeAllWorkflowJobsFinalizer) {
		result, err := addFinalizers(o, promise, []string{removeAllWorkflowJobsFinalizer})
		return &result, err
	}

	o.logger.Info("Promise contains workflows.promise.configure, reconciling workflows")
	unstructuredPromise, err := promise.ToUnstructured()
	if err != nil {
		return nil, err
	}

	var pipelines []workflow.Pipeline
	for i, p := range configurePipeline {
		isLast := i == len(configurePipeline)-1
		pipelineResources, err := pipeline.NewConfigurePromise(
			unstructuredPromise,
			p,
			promise.GetName(),
			promise.Spec.DestinationSelectors,
			o.logger,
		)
		if err != nil {
			return nil, err
		}
		job := pipelineResources[4].(*batchv1.Job)
		job.Spec.Template.Spec.Containers[0].Env = append(job.Spec.Template.Spec.Containers[0].Env, v1.EnvVar{
			Name:  "IS_LAST_PIPELINE",
			Value: strconv.FormatBool(isLast),
		})
		pipelines = append(pipelines, workflow.Pipeline{
			Job:                  job,
			JobRequiredResources: pipelineResources[0:4],
			Name:                 p.Name,
		})
	}

	jobOpts := workflow.NewOpts(o.ctx, o.client, o.logger, unstructuredPromise, pipelines, "promise")

	requeue, err := reconcileConfigure(jobOpts)
	if err != nil {
		return nil, err
	}

	if requeue {
		return &defaultRequeue, nil
	}
	return nil, nil
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

func (r *PromiseReconciler) ensureDynamicControllerIsStarted(promise *v1alpha1.Promise, rrCRD *apiextensionsv1.CustomResourceDefinition, rrGVK schema.GroupVersionKind, configurePipelines, deletePipelines []v1alpha1.Pipeline, canCreateResources *bool, logger logr.Logger) error {

	// The Dynamic Controller needs to be started once and only once.
	if r.dynamicControllerHasAlreadyStarted(promise) {
		logger.Info("dynamic controller already started, ensuring it is up to date")

		dynamicController := r.StartedDynamicControllers[string(promise.GetUID())]
		dynamicController.DeletePipelines = deletePipelines
		dynamicController.ConfigurePipelines = configurePipelines
		dynamicController.GVK = &rrGVK
		dynamicController.CRD = rrCRD

		dynamicController.CanCreateResources = canCreateResources

		dynamicController.PromiseDestinationSelectors = promise.Spec.DestinationSelectors

		return nil
	}
	logger.Info("starting dynamic controller")

	//temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
	//once resolved, delete dynamic controller rather than disable
	enabled := true
	dynamicResourceRequestController := &DynamicResourceRequestController{
		Client:                      r.Client,
		Scheme:                      r.Scheme,
		GVK:                         &rrGVK,
		CRD:                         rrCRD,
		PromiseIdentifier:           promise.GetName(),
		ConfigurePipelines:          configurePipelines,
		DeletePipelines:             deletePipelines,
		PromiseDestinationSelectors: promise.Spec.DestinationSelectors,
		Log:                         r.Log.WithName(promise.GetName()),
		UID:                         string(promise.GetUID())[0:5],
		Enabled:                     &enabled,
		CanCreateResources:          canCreateResources,
	}
	r.StartedDynamicControllers[string(promise.GetUID())] = dynamicResourceRequestController

	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(rrGVK)

	return ctrl.NewControllerManagedBy(r.Manager).
		For(unstructuredCRD).
		Owns(&batchv1.Job{}).
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
				Namespace: v1alpha1.SystemNamespace,
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

func (r *PromiseReconciler) ensureCRDExists(ctx context.Context, promise *v1alpha1.Promise, rrCRD *apiextensionsv1.CustomResourceDefinition,
	rrGVK schema.GroupVersionKind, logger logr.Logger) (*ctrl.Result, error) {

	_, err := r.ApiextensionsClient.
		CustomResourceDefinitions().
		Create(ctx, rrCRD, metav1.CreateOptions{})

	if err == nil {
		return &fastRequeue, nil
	}

	if !errors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("Error creating crd: %w", err)
	}

	logger.Info("CRD already exists", "crdName", rrCRD.Name)
	existingCRD, err := r.ApiextensionsClient.CustomResourceDefinitions().Get(ctx, rrCRD.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	existingCRD.Spec.Versions = rrCRD.Spec.Versions
	existingCRD.Spec.Conversion = rrCRD.Spec.Conversion
	existingCRD.Spec.PreserveUnknownFields = rrCRD.Spec.PreserveUnknownFields
	_, err = r.ApiextensionsClient.CustomResourceDefinitions().Update(ctx, existingCRD, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}

	version := ""
	for _, v := range rrCRD.Spec.Versions {
		if v.Storage {
			version = v.Name
			break
		}
	}

	statusUpdated, err := r.updateStatus(promise, rrCRD.Spec.Names.Kind, rrCRD.Spec.Group, version)
	if err != nil {
		return nil, err
	}

	if statusUpdated {
		return &fastRequeue, nil
	}

	updatedCRD, err := r.ApiextensionsClient.CustomResourceDefinitions().Get(ctx, rrCRD.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	for _, cond := range updatedCRD.Status.Conditions {
		if string(cond.Type) == string(apiextensions.Established) && cond.Status == apiextensionsv1.ConditionTrue {
			logger.Info("CRD established", "crdName", rrCRD.Name)
			return nil, nil
		}
	}

	logger.Info("CRD not yet established", "crdName", rrCRD.Name, "statusConditions", updatedCRD.Status.Conditions)

	return &fastRequeue, nil
}

func (r *PromiseReconciler) updateStatus(promise *v1alpha1.Promise, kind, group, version string) (bool, error) {
	apiVersion := strings.ToLower(group + "/" + version)
	if promise.Status.Kind == kind && promise.Status.APIVersion == apiVersion {
		return false, nil
	}

	promise.Status.Kind = kind
	promise.Status.APIVersion = apiVersion
	return true, r.Client.Status().Update(context.TODO(), promise)
}

func (r *PromiseReconciler) deletePromise(o opts, promise *v1alpha1.Promise, deletePipelines []v1alpha1.Pipeline) (ctrl.Result, error) {
	o.logger.Info("finalizers existing", "finalizers", promise.GetFinalizers())
	if resourceutil.FinalizersAreDeleted(promise, promiseFinalizers) {
		return ctrl.Result{}, nil
	}

	if controllerutil.ContainsFinalizer(promise, runDeleteWorkflowsFinalizer) {
		unstructuredPromise, err := promise.ToUnstructured()
		if err != nil {
			return ctrl.Result{}, err
		}
		var pipelines []workflow.Pipeline
		for _, p := range deletePipelines {
			pipelineResources := pipeline.NewDeletePromise(
				unstructuredPromise, p,
			)

			pipelines = append(pipelines, workflow.Pipeline{
				Job:                  pipelineResources[3].(*batchv1.Job),
				JobRequiredResources: pipelineResources[0:3],
				Name:                 p.Name,
			})
		}

		jobOpts := workflow.NewOpts(o.ctx, o.client, o.logger, unstructuredPromise, pipelines, "promise")

		requeue, err := reconcileDelete(jobOpts)
		if err != nil {
			return ctrl.Result{}, err
		}

		if requeue {
			return defaultRequeue, nil
		}

		controllerutil.RemoveFinalizer(promise, runDeleteWorkflowsFinalizer)
		if err := r.Client.Update(o.ctx, promise); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.ContainsFinalizer(promise, removeAllWorkflowJobsFinalizer) {
		err := r.deletePromiseWorkflowJobs(o, promise, removeAllWorkflowJobsFinalizer)
		if err != nil {
			return ctrl.Result{}, err
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(promise, resourceRequestCleanupFinalizer) {
		o.logger.Info("deleting resources associated with finalizer", "finalizer", resourceRequestCleanupFinalizer)
		err := r.deleteResourceRequests(o, promise)
		if err != nil {
			return ctrl.Result{}, err
		}
		return fastRequeue, nil
	}

	//temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
	//once resolved, delete dynamic controller rather than disable
	if d, exists := r.StartedDynamicControllers[string(promise.GetUID())]; exists {
		r.RestartManager()
		enabled := false
		d.Enabled = &enabled
	}

	if controllerutil.ContainsFinalizer(promise, dynamicControllerDependantResourcesCleanupFinalizer) {
		o.logger.Info("deleting resources associated with finalizer", "finalizer", dynamicControllerDependantResourcesCleanupFinalizer)
		err := r.deleteDynamicControllerAndWorkflowResources(o, promise)
		if err != nil {
			return defaultRequeue, nil
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(promise, dependenciesCleanupFinalizer) {
		o.logger.Info("deleting Work associated with finalizer", "finalizer", dependenciesCleanupFinalizer)
		err := r.deleteWork(o, promise)
		if err != nil {
			return ctrl.Result{}, err
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(promise, crdCleanupFinalizer) {
		o.logger.Info("deleting CRDs associated with finalizer", "finalizer", crdCleanupFinalizer)
		err := r.deleteCRDs(o, promise)
		if err != nil {
			return ctrl.Result{}, err
		}
		return fastRequeue, nil
	}

	return fastRequeue, nil
}

func (r *PromiseReconciler) deletePromiseWorkflowJobs(o opts, promise *v1alpha1.Promise, finalizer string) error {
	jobGVK := schema.GroupVersionKind{
		Group:   batchv1.SchemeGroupVersion.Group,
		Version: batchv1.SchemeGroupVersion.Version,
		Kind:    "Job",
	}

	jobLabels := pipeline.LabelsForAllPromiseWorkflows(promise.GetName())

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, jobGVK, jobLabels)
	if err != nil {
		return err
	}

	if !resourcesRemaining {
		controllerutil.RemoveFinalizer(promise, finalizer)
		if err := r.Client.Update(o.ctx, promise); err != nil {
			return err
		}
	}

	return nil
}

func (r *PromiseReconciler) deleteDynamicControllerAndWorkflowResources(o opts, promise *v1alpha1.Promise) error {
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
			resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, gvk, promise.GenerateSharedLabels())
			if err != nil {
				return err
			}

			if resourcesRemaining {
				return nil
			}
		}
	}

	controllerutil.RemoveFinalizer(promise, dynamicControllerDependantResourcesCleanupFinalizer)
	return r.Client.Update(o.ctx, promise)
}

func (r *PromiseReconciler) deleteResourceRequests(o opts, promise *v1alpha1.Promise) error {
	rrCRD, rrGVK, err := generateCRDAndGVK(promise, o.logger)
	if err != nil {
		return err
	}

	// No need to pass labels since all resource requests are of Kind
	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, rrGVK, nil)
	if err != nil {
		return err
	}

	pipelines, err := promise.GeneratePipelines(o.logger)
	if err != nil {
		return err
	}

	var canCreateResources bool
	err = r.ensureDynamicControllerIsStarted(promise, rrCRD, rrGVK, pipelines.ConfigureResource, pipelines.DeleteResource, &canCreateResources, o.logger)
	if err != nil {
		return err
	}

	if !resourcesRemaining {
		controllerutil.RemoveFinalizer(promise, resourceRequestCleanupFinalizer)
		if err := r.Client.Update(o.ctx, promise); err != nil {
			return err
		}
	}

	return nil
}

func (r *PromiseReconciler) deleteCRDs(o opts, promise *v1alpha1.Promise) error {
	rrCRD, err := promise.GetAPIAsCRD()
	if err != nil {
		o.logger.Error(err, "Failed unmarshalling CRD, skipping deletion")
		controllerutil.RemoveFinalizer(promise, crdCleanupFinalizer)
		if err := r.Client.Update(o.ctx, promise); err != nil {
			return err
		}
		return nil
	}

	_, err = r.ApiextensionsClient.CustomResourceDefinitions().Get(o.ctx, rrCRD.GetName(), metav1.GetOptions{})

	if errors.IsNotFound(err) {
		controllerutil.RemoveFinalizer(promise, crdCleanupFinalizer)
		return r.Client.Update(o.ctx, promise)
	}

	return r.ApiextensionsClient.
		CustomResourceDefinitions().
		Delete(o.ctx, rrCRD.GetName(), metav1.DeleteOptions{})
}

func (r *PromiseReconciler) deleteWork(o opts, promise *v1alpha1.Promise) error {
	workGVK := schema.GroupVersionKind{
		Group:   v1alpha1.GroupVersion.Group,
		Version: v1alpha1.GroupVersion.Version,
		Kind:    "Work",
	}

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, workGVK, promise.GenerateSharedLabels())
	if err != nil {
		return err
	}

	if !resourcesRemaining {
		controllerutil.RemoveFinalizer(promise, dependenciesCleanupFinalizer)
		if err := r.Client.Update(o.ctx, promise); err != nil {
			return err
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PromiseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Promise{}).
		Owns(&batchv1.Job{}).
		Watches(
			&v1alpha1.Promise{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				promise := obj.(*v1alpha1.Promise)
				var resources []reconcile.Request
				for _, req := range promise.Status.RequiredBy {
					resources = append(resources, reconcile.Request{NamespacedName: types.NamespacedName{Name: req.Promise.Name}})
				}
				return resources
			}),
		).
		Complete(r)
}

func ensurePromiseDeleteWorkflowFinalizer(o opts, promise *v1alpha1.Promise, deletePipelines []v1alpha1.Pipeline) (*ctrl.Result, error) {
	promiseDeletePipelineExists := deletePipelines != nil
	promiseContainsDeleteWorkflowsFinalizer := controllerutil.ContainsFinalizer(promise, runDeleteWorkflowsFinalizer)
	promiseContainsRemoveAllWorkflowJobsFinalizer := controllerutil.ContainsFinalizer(promise, removeAllWorkflowJobsFinalizer)

	if promiseDeletePipelineExists &&
		(!promiseContainsDeleteWorkflowsFinalizer || !promiseContainsRemoveAllWorkflowJobsFinalizer) {
		result, err := addFinalizers(o, promise, []string{runDeleteWorkflowsFinalizer, removeAllWorkflowJobsFinalizer})
		return &result, err
	}

	if !promiseDeletePipelineExists && promiseContainsDeleteWorkflowsFinalizer {
		controllerutil.RemoveFinalizer(promise, runDeleteWorkflowsFinalizer)
		return &ctrl.Result{}, o.client.Update(o.ctx, promise)
	}

	return nil, nil
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
				"observedGeneration": {
					Type:   "integer",
					Format: "int64",
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

func (r *PromiseReconciler) applyWorkForDependencies(o opts, promise *v1alpha1.Promise) error {
	name := resourceutil.GenerateObjectName(promise.GetName() + "-static-deps")
	work, err := v1alpha1.NewPromiseDependenciesWork(promise, name)
	if err != nil {
		return err
	}
	resourceutil.SetStaticDependencyWorkLabels(work.Labels, promise.GetName())

	existingWork, err := resourceutil.GetWorkForStaticDependencies(r.Client, v1alpha1.SystemNamespace, promise.GetName())
	if err != nil {
		return err
	}

	var op string
	if existingWork == nil {
		op = "created"
		err = r.Client.Create(o.ctx, work)
	} else {
		op = "updated"
		existingWork.Spec = work.Spec
		err = r.Client.Update(o.ctx, existingWork)
	}

	if err != nil {
		return err
	}

	o.logger.Info("resource reconciled", "operation", op, "namespace", work.GetNamespace(), "name", work.GetName(), "gvk", work.GroupVersionKind())
	return nil
}

func (r *PromiseReconciler) markRequiredPromiseAsRequired(ctx context.Context, version string, promise, requiredPromise *v1alpha1.Promise) {
	requiredBy := v1alpha1.RequiredBy{
		Promise: v1alpha1.PromiseSummary{
			Name:    promise.Name,
			Version: promise.Status.Version,
		},
		RequiredVersion: version,
	}

	var found bool
	for i, required := range requiredPromise.Status.RequiredBy {
		if required.Promise.Name == promise.GetName() {
			requiredPromise.Status.RequiredBy[i] = requiredBy
			found = true
		}
	}

	if !found {
		requiredPromise.Status.RequiredBy = append(requiredPromise.Status.RequiredBy, requiredBy)
	}

	err := r.Client.Status().Update(ctx, requiredPromise)
	if err != nil {
		r.Log.Error(err, "error updating promise required by promise", "promise", promise.GetName(), "required promise", requiredPromise.GetName())
	}
}
