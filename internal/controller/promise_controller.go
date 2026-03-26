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

// Package controller contains the controllers for all Kratix-managed CRDs.
package controller

import (
	"context"
	stderrors "errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/syntasso/kratix/lib/objectutil"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"
	"go.opentelemetry.io/otel/attribute"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1cs "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kmanager "sigs.k8s.io/controller-runtime/pkg/manager"
)

var reconcileConfigure = workflow.ReconcileConfigure
var reconcileDelete = workflow.ReconcileDelete

//counterfeiter:generate . Manager
type Manager interface {
	kmanager.Manager
}

// PromiseReconciler reconciles a Promise object.
type PromiseReconciler struct {
	Scheme                    *runtime.Scheme
	Client                    client.Client
	ApiextensionsClient       apiextensionsv1cs.CustomResourceDefinitionsGetter
	Log                       logr.Logger
	Manager                   ctrl.Manager
	StartedDynamicControllers map[string]*DynamicResourceRequestController
	NumberOfJobsToKeep        int
	ReconciliationInterval    time.Duration
	EventRecorder             record.EventRecorder
	PromiseUpgrade            bool
}

const (
	resourceRequestCleanupFinalizer = v1alpha1.KratixPrefix + "resource-request-cleanup"
	// TODO fix the name of this finalizer: dependant -> dependent (breaking change)
	dynamicControllerDependantResourcesCleanupFinalizer = v1alpha1.KratixPrefix + "dynamic-controller-dependant-resources-cleanup"
	crdCleanupFinalizer                                 = v1alpha1.KratixPrefix + "api-crd-cleanup"
	dependenciesCleanupFinalizer                        = v1alpha1.KratixPrefix + "dependencies-cleanup"
	revisionCleanupFinalizer                            = v1alpha1.KratixPrefix + "revision-cleanup"
	lastUpdatedAtAnnotation                             = v1alpha1.KratixPrefix + "last-updated-at"

	requirementStateInstalled                      = "Requirement installed"
	requirementStateNotInstalled                   = "Requirement not installed"
	requirementStateNotInstalledAtSpecifiedVersion = "Requirement not installed at the specified version"
	requirementStateNotAvailable                   = "Requirement not available"
	requirementUnknownInstallationState            = "Requirement state unknown"
	pauseReconciliationLabel                       = v1alpha1.KratixPrefix + "paused"
	pausedReconciliationReason                     = "PausedReconciliation"
	workflowSuspendedReason                        = "WorkflowSuspended"
)

var (
	promiseFinalizers = []string{
		resourceRequestCleanupFinalizer,
		dynamicControllerDependantResourcesCleanupFinalizer,
		crdCleanupFinalizer,
		dependenciesCleanupFinalizer,
		removeAllWorkflowJobsFinalizer,
		runDeleteWorkflowsFinalizer,
		revisionCleanupFinalizer,
	}

	// fastRequeue can be used whenever we want to quickly requeue, and we don't expect
	// an error to occur. Example: we delete a resource, we then requeue
	// to check it's been deleted. Here we can use a fastRequeue instead of a defaultRequeue
	fastRequeue    = ctrl.Result{RequeueAfter: 5 * time.Second}
	defaultRequeue = ctrl.Result{RequeueAfter: 15 * time.Second}
	slowRequeue    = ctrl.Result{RequeueAfter: 60 * time.Second}
)

// +kubebuilder:rbac:groups=platform.kratix.io,resources=promises,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.kratix.io,resources=promises/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.kratix.io,resources=promises/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=create;update;list;watch;delete

// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=create;update;escalate;bind;list;get;delete;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=create;update;list;get;delete;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles,verbs=create;update;escalate;bind;list;get;delete;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=create;update;list;get;delete;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;update;list;get;watch;delete

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create;update;patch;delete

func (r *PromiseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	logger := r.Log.WithValues(
		"controller", "promise",
		"name", req.Name,
	)
	if r.StartedDynamicControllers == nil {
		r.StartedDynamicControllers = make(map[string]*DynamicResourceRequestController)
	}
	promise := &v1alpha1.Promise{}
	if err := r.Client.Get(ctx, req.NamespacedName, promise); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logging.Warn(logger, "failed to get Promise; requeueing")
		return defaultRequeue, nil
	}

	baseLogger := logger.WithValues(
		"generation", promise.GetGeneration(),
	)
	spanName := fmt.Sprintf("%s/PromiseReconcile", promise.GetName())
	ctx, logger, traceCtx := setupReconcileTrace(ctx, "promise-controller", spanName, promise, baseLogger)
	defer finishReconcileTrace(traceCtx, &retErr)()

	addPromiseSpanAttributes(traceCtx, promise)

	logging.Info(logger, "reconciliation started")
	defer logReconcileDuration(logger, time.Now(), result, retErr)()

	if err := persistReconcileTrace(traceCtx, r.Client, logger); err != nil {
		logging.Error(logger, err, "failed to persist trace annotations")
		return ctrl.Result{}, err
	}

	if v, ok := promise.Labels[pauseReconciliationLabel]; ok && v == "true" {
		msg := fmt.Sprintf("'%s' label set to 'true' for promise; pausing reconciliation", pauseReconciliationLabel)
		logging.Info(r.Log, msg)
		r.EventRecorder.Event(promise, v1.EventTypeWarning, pausedReconciliationReason, msg)
		return ctrl.Result{}, r.setPausedReconciliationStatusConditions(ctx, promise)
	}

	opts := opts{client: r.Client, ctx: ctx, logger: logger}

	if !promise.DeletionTimestamp.IsZero() {
		return r.deletePromise(opts, promise)
	}
	if changed := r.syncPromiseFinalizers(promise); changed {
		return ctrl.Result{}, r.Client.Update(opts.ctx, promise)
	}

	result, err := r.handlePromiseVersion(ctx, promise)
	if err != nil || !result.IsZero() {
		return result, err
	}

	// Gather Promise original information
	originalStatus := promise.Status.Kratix.Status
	originalAvailableCondition := promise.GetCondition(v1alpha1.PromiseAvailableConditionType)

	// Set status to unavailable, at the end of this function we set it to
	// available. If at any time we return early, it persisted as unavailable
	promise.Status.Status = v1alpha1.PromiseStatusUnavailable
	promise.Status.Kratix.Status = v1alpha1.PromiseStatusUnavailable

	// Mark the Promise.Status.Conditions to Unavailable
	updateConditionOnPromise(promise, promiseUnavailableStatusCondition())

	requirementsChanged := r.hasPromiseRequirementsChanged(ctx, promise)
	if requirementsChanged {
		if meta.IsStatusConditionFalse(promise.Status.Conditions, "RequirementsFulfilled") {
			// Mark the Promise.Status.Conditions to `Message: "Pending"`
			updateConditionOnPromise(promise, promiseReconciledPendingCondition("RequirementsNotFulfilled"))
		}

		if result, statusUpdateErr := r.updatePromiseStatus(ctx, promise); statusUpdateErr != nil || !result.IsZero() {
			return result, statusUpdateErr
		}

		// If the originalStatus was available, now we have transitioned to an Unavailable or Pending state
		// therefore we need to produce an event to inform of this transition
		if originalStatus == v1alpha1.PromiseStatusAvailable {
			msg := "Promise no longer available: Requirements have changed"
			r.EventRecorder.Eventf(
				promise, "Warning", "Unavailable", msg)
			logging.Info(r.Log, msg)
		}

		logging.Warn(logger, "promise requirements changed; requeueing")
		return ctrl.Result{}, nil
	}

	var rrCRD *apiextensionsv1.CustomResourceDefinition
	var rrGVK *schema.GroupVersionKind

	if promise.ContainsAPI() {
		rrCRD, rrGVK, err = generateCRDAndGVK(promise, logger)
		if err != nil {
			return ctrl.Result{}, err
		}

		requeue, err := r.ensureCRDExists(ctx, promise, rrCRD, logger)
		if err != nil {
			return ctrl.Result{}, err
		}

		if requeue != nil {
			return *requeue, nil
		}

		if err = r.createResourcesForDynamicControllerIfTheyDontExist(ctx, promise, rrCRD, rrGVK, logger); err != nil {
			// TODO add support for updates
			return ctrl.Result{}, err
		}
	}

	usPromise, err := promise.ToUnstructured()
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("error converting Promise to Unstructured: %w", err)
	}

	passiveRequeue, err := r.reconcileDependenciesAndPromiseWorkflows(opts, promise, usPromise)
	if err != nil {
		return ctrl.Result{}, err
	}

	if passiveRequeue {
		logging.Debug(logger, "reconciliation paused awaiting Promise configure workflow updates")
		return ctrl.Result{}, nil
	}

	if promise.ContainsAPI() {
		dynamicControllerCanCreateResources := true
		for _, req := range promise.Status.RequiredPromises {
			if req.State != requirementStateInstalled {
				logging.Warn(logger, "requirement not installed; disabling dynamic controller", "requirement", req)
				dynamicControllerCanCreateResources = false
			}
		}

		if err = r.ensureDynamicControllerIsStarted(promise, rrCRD, rrGVK, &dynamicControllerCanCreateResources, logger); err != nil {
			return ctrl.Result{}, err
		}

		if !dynamicControllerCanCreateResources {
			logging.Warn(logger, "requirements not fulfilled; dynamic controller disabled", "requirementsStatus", promise.Status.RequiredPromises)
			return slowRequeue, nil
		}

		logging.Info(logger, "requirements fulfilled", "requirementsStatus", promise.Status.RequiredPromises)
		if shouldReconcileResources(promise) {
			return r.reconcileResources(ctx, logger, promise, rrGVK, originalStatus)
		}
	} else {
		logging.Debug(logger, "Promise only contains dependencies; skipping API and dynamic controller creation")
	}

	// If the originalStatus was not equal to `Available` then set the PromiseStatus to Available and exit reconciliation loop
	if originalStatus != v1alpha1.PromiseStatusAvailable {
		return r.setPromiseStatusToAvailable(ctx, promise, logger)
	}

	promise.Status.Status = originalStatus
	promise.Status.Kratix.Status = originalStatus
	timeStamp := metav1.Time{Time: time.Now()}

	if originalAvailableCondition != nil {
		timeStamp = originalAvailableCondition.LastTransitionTime
	}
	updateConditionOnPromise(promise, promiseAvailableStatusCondition(timeStamp))

	statusUpdate, err := r.generateConditions(ctx, promise, r.getWorkflowsCount(promise))
	if err != nil {
		return ctrl.Result{}, err
	}

	if statusUpdate {
		return ctrl.Result{}, r.Client.Status().Update(ctx, promise)
	}

	completedCond := promise.GetCondition(string(resourceutil.ConfigureWorkflowCompletedCondition))
	if !promise.HasPipeline(v1alpha1.WorkflowTypePromise, v1alpha1.WorkflowActionConfigure) ||
		(completedCond != nil && completedCond.Status == metav1.ConditionTrue) {
		if completedCond != nil {
			r.EventRecorder.Eventf(promise, v1.EventTypeNormal, "ConfigureWorkflowCompleted", "All workflows completed")
		}
		return r.nextReconciliation(logger), nil
	}

	return ctrl.Result{}, nil
}

func (r *PromiseReconciler) syncPromiseFinalizers(promise *v1alpha1.Promise) bool {
	finalizersChanged := addMissingFinalizersToResource(promise, r.promiseFinalizers(promise))

	if shouldRemoveDeleteWorkflowsFinalizer(promise) {
		controllerutil.RemoveFinalizer(promise, runDeleteWorkflowsFinalizer)
		finalizersChanged = true
	}

	return finalizersChanged
}

func addMissingFinalizersToResource(resource client.Object, finalizers []string) bool {
	finalizersChanged := false

	for _, finalizer := range finalizers {
		if controllerutil.ContainsFinalizer(resource, finalizer) {
			continue
		}

		controllerutil.AddFinalizer(resource, finalizer)
		finalizersChanged = true
	}

	return finalizersChanged
}

func shouldRemoveDeleteWorkflowsFinalizer(promise *v1alpha1.Promise) bool {
	return !promise.HasPipeline(v1alpha1.WorkflowTypePromise, v1alpha1.WorkflowActionDelete) &&
		controllerutil.ContainsFinalizer(promise, runDeleteWorkflowsFinalizer)
}

func (r *PromiseReconciler) handlePromiseVersion(ctx context.Context, promise *v1alpha1.Promise) (ctrl.Result, error) {
	var promiseVersion string
	var found bool
	if promiseVersion, found = promise.Labels[v1alpha1.PromiseVersionLabel]; found {
		if promise.Status.Version != promiseVersion && promise.Status.Kratix.Version != promiseVersion {
			promise.Status.Version = promiseVersion
			promise.Status.Kratix.Version = promiseVersion
			return r.updatePromiseStatus(ctx, promise)
		}
	}

	if !r.PromiseUpgrade {
		return ctrl.Result{}, nil
	}

	if promiseVersion == "" {
		promiseVersion = "not-set"
	}

	revision := v1alpha1.NewPromiseRevision(promise, promiseVersion)

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, revision, func() error {
		revision.Spec.PromiseRef = v1alpha1.PromiseRef{Name: promise.Name}
		revision.Spec.PromiseSpec = promise.Spec
		revision.Spec.Version = promiseVersion
		l := revision.GetLabels()
		revision.SetLabels(labels.Merge(l, labels.Merge(promise.GenerateSharedLabels(), map[string]string{
			"kratix.io/latest-revision": "true",
		})))

		return controllerutil.SetControllerReference(promise, revision, scheme.Scheme)
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	if op == controllerutil.OperationResultCreated {
		r.EventRecorder.Eventf(promise, v1.EventTypeNormal, "RevisionCreated", fmt.Sprintf("Revision %s created", revision.GetName()))
	}
	return ctrl.Result{}, nil
}

func (r *PromiseReconciler) setPausedReconciliationStatusConditions(ctx context.Context, promise *v1alpha1.Promise) error {
	return r.setPromiseUnavailableStatusConditions(
		ctx,
		promise,
		promiseAvailablePausedStatusCondition(),
		promiseReconciledPausedCondition(),
		"Paused",
	)
}

func (r *PromiseReconciler) setWorkflowSuspendedStatusCondition(ctx context.Context, promise *v1alpha1.Promise) error {
	return r.setPromiseUnavailableStatusConditions(
		ctx,
		promise,
		promiseAvailableSuspendedStatusCondition(),
		promiseReconciledSuspendedCondition(),
		"Suspended",
	)
}

func (r *PromiseReconciler) setPromiseUnavailableStatusConditions(
	ctx context.Context,
	promise *v1alpha1.Promise,
	availableCondition metav1.Condition,
	reconciledCondition metav1.Condition,
	expectedReconciledMessage string,
) error {
	var updated bool
	available := promise.GetCondition(v1alpha1.PromiseStatusAvailable)
	if available == nil ||
		available.Status != availableCondition.Status ||
		available.Reason != availableCondition.Reason ||
		available.Message != availableCondition.Message {
		updateConditionOnPromise(promise, availableCondition)
		promise.Status.Status = v1alpha1.PromiseStatusUnavailable
		promise.Status.Kratix.Status = v1alpha1.PromiseStatusUnavailable
		updated = true
	}

	reconciled := promise.GetCondition(string(resourceutil.ReconciledCondition))
	if reconciled == nil || reconciled.Status != metav1.ConditionUnknown || reconciled.Message != expectedReconciledMessage {
		updateConditionOnPromise(promise, reconciledCondition)
		updated = true
	}

	if !updated {
		return nil
	}

	return r.Client.Status().Update(ctx, promise)
}

func resetPromiseWorkflowPipelinesToPending(promise *v1alpha1.Promise) {
	promise.Status.Kratix.Workflows.SuspendedGeneration = 0
	for i := range promise.Status.Kratix.Workflows.Pipelines {
		promise.Status.Kratix.Workflows.Pipelines[i].Phase = v1alpha1.WorkflowPhasePending
		promise.Status.Kratix.Workflows.Pipelines[i].Message = ""
		promise.Status.Kratix.Workflows.Pipelines[i].LastTransitionTime = metav1.Now()
	}
}

func (r *PromiseReconciler) generateConditions(ctx context.Context, promise *v1alpha1.Promise, numberOfPipelines int64) (bool, error) {
	failed, misplaced, pending, ready, err := r.getWorksStatus(ctx, promise)
	if err != nil {
		return false, err
	}
	worksSucceededUpdate := r.updateWorksSucceededCondition(promise, failed, pending, ready, misplaced)
	reconciledUpdate := r.updateReconciledCondition(promise)
	workflowsCounterStatusUpdate := r.generateWorkflowsCounterStatus(promise, numberOfPipelines)

	return worksSucceededUpdate || reconciledUpdate || workflowsCounterStatusUpdate, nil
}

func (r *PromiseReconciler) generateWorkflowsCounterStatus(promise *v1alpha1.Promise, numOfPipelines int64) bool {
	desiredWorkflows := numOfPipelines
	var desiredWorkflowsSucceeded int64

	completedCond := promise.GetCondition(string(resourceutil.ConfigureWorkflowCompletedCondition))
	if completedCond != nil && completedCond.Status == metav1.ConditionTrue {
		desiredWorkflowsSucceeded = numOfPipelines
	}

	// TODO: remove deprecated promise.Status.Workflows, promise.Status.WorkflowsSucceeded, promise.Status.WorkflowsFailed
	if promise.Status.Workflows != desiredWorkflows || promise.Status.WorkflowsSucceeded != desiredWorkflowsSucceeded {
		promise.Status.Workflows = desiredWorkflows
		promise.Status.WorkflowsSucceeded = desiredWorkflowsSucceeded
		promise.Status.WorkflowsFailed = 0
		return true
	}
	return false
}

func (r *PromiseReconciler) updateReconciledCondition(promise *v1alpha1.Promise) bool {
	worksSucceeded := promise.GetCondition(string(resourceutil.WorksSucceededCondition))
	workflowCompleted := promise.GetCondition(string(resourceutil.ConfigureWorkflowCompletedCondition))
	reconciled := promise.GetCondition(string(resourceutil.ReconciledCondition))

	var updated bool
	if workflowCompleted != nil &&
		workflowCompleted.Status == "False" && workflowCompleted.Reason == "PipelinesInProgress" {
		if reconciled == nil || reconciled.Status != "Unknown" {
			updateConditionOnPromise(promise, promiseReconciledPendingCondition("WorkflowPending"))
			updated = true
		}
	} else if workflowCompleted != nil && workflowCompleted.Status == metav1.ConditionFalse {
		if reconciled == nil || reconciled.Status != metav1.ConditionFalse ||
			reconciled.Reason != resourceutil.ConfigureWorkflowCompletedFailedReason {
			updateConditionOnPromise(promise, promiseReconciledFailingCondition(resourceutil.ConfigureWorkflowCompletedFailedReason))
			updated = true
		}
	} else if worksSucceeded != nil && worksSucceeded.Status == "Unknown" {
		if reconciled == nil || reconciled.Status != "Unknown" {
			updateConditionOnPromise(promise, promiseReconciledPendingCondition("WorksPending"))
			updated = true
		}
	} else if worksSucceeded != nil && worksSucceeded.Status == metav1.ConditionFalse {
		if reconciled == nil || reconciled.Status != metav1.ConditionFalse {
			updateConditionOnPromise(promise, promiseReconciledFailingCondition("WorksFailing"))
			updated = true
		}
	} else if workflowCompleted != nil && worksSucceeded != nil &&
		workflowCompleted.Status == "True" && worksSucceeded.Status == "True" {

		if reconciled == nil || reconciled.Status != "True" {
			updateConditionOnPromise(promise, promiseReconciledCondition())
			updated = true
			r.EventRecorder.Event(promise, v1.EventTypeNormal, "ReconcileSucceeded",
				"Successfully reconciled")
		}
	}
	return updated
}

func (r *PromiseReconciler) getWorksStatus(ctx context.Context,
	promise *v1alpha1.Promise) ([]string,
	[]string,
	[]string,
	[]string,
	error,
) {
	workSelectorLabel := labels.FormatLabels(
		resourceutil.GetWorkLabels(promise.GetName(),
			"",
			"",
			"",
			v1alpha1.WorkTypePromise),
	)
	selector, err := labels.Parse(workSelectorLabel)
	if err != nil {
		logging.Error(r.Log, err, "failed parsing Works selector label", "labels", workSelectorLabel)
		return nil, nil, nil, nil, err
	}

	var works v1alpha1.WorkList
	err = r.Client.List(ctx, &works, &client.ListOptions{
		Namespace:     promise.GetNamespace(),
		LabelSelector: selector,
	})
	if err != nil {
		logging.Error(r.Log, err, "failed listing works", "labelSelector", workSelectorLabel)
		return nil, nil, nil, nil, err
	}

	var failed, misplaced, ready, pending []string
	for _, work := range works.Items {
		readyCond := meta.FindStatusCondition(work.Status.Conditions, "Ready")
		message := "Pending"
		if readyCond != nil && readyCond.Message != "" {
			message = readyCond.Message
		}
		switch message {
		case "Failing":
			failed = append(failed, work.Name)
		case "Misplaced":
			misplaced = append(misplaced, work.Name)
		case "Pending":
			pending = append(pending, work.Name)
		case "Ready":
			ready = append(ready, work.Name)
		}
	}
	return failed, misplaced, pending, ready, nil
}

func (r *PromiseReconciler) updateWorksSucceededCondition(
	promise *v1alpha1.Promise,
	failed,
	pending,
	_,
	misplaced []string,
) bool {
	cond := promise.GetCondition(string(resourceutil.WorksSucceededCondition))
	if len(failed) > 0 {
		if cond == nil || cond.Status == "True" {
			updateConditionOnPromise(promise, promiseWorksSucceededFailedCondition(failed))
			r.EventRecorder.Eventf(promise, v1.EventTypeWarning, "WorksFailing",
				"Some works associated with this promise has failed: [%s]", strings.Join(failed, ","))
			return true
		}
		return false
	}
	if len(pending) > 0 {
		if cond == nil || cond.Status != "Unknown" {
			updateConditionOnPromise(promise, promiseWorksSucceededUnknownCondition(pending))
			return true
		}
		return false
	}
	if len(misplaced) > 0 {
		if cond == nil || cond.Status != "False" || cond.Reason != "WorksMisplaced" {
			updateConditionOnPromise(promise, promiseWorksSucceededMisplacedCondition(misplaced))
			r.EventRecorder.Eventf(promise, v1.EventTypeWarning, "WorksMisplaced",
				"Some works associated with this promise are misplaced: [%s]", strings.Join(misplaced, ","))
			return true
		}
		return false
	}
	if cond == nil || cond.Status != "True" {
		updateConditionOnPromise(promise, promiseWorksSucceededStatusCondition())
		r.EventRecorder.Event(promise, v1.EventTypeNormal, "WorksSucceeded",
			"All works associated with this promise are ready")
		return true
	}
	return false
}

func (r *PromiseReconciler) reconcileResources(ctx context.Context, logger logr.Logger, promise *v1alpha1.Promise,
	rrGVK *schema.GroupVersionKind, originalPromiseAvailability string) (ctrl.Result, error) {
	logging.Debug(logger, "reconciling resource requests", "promiseName", promise.Name)
	if err := r.reconcileAllRRs(ctx, rrGVK); err != nil {
		return ctrl.Result{}, err
	}

	r.EventRecorder.Event(promise, "Normal", "ReconcilingResources", "Reconciling all resource requests")

	if _, ok := promise.Labels[resourceutil.ReconcileResourcesLabel]; ok {
		return ctrl.Result{}, r.removeReconcileResourcesLabel(ctx, promise)
	}

	logging.Debug(logger, "updating observed generation", "from", promise.Status.ObservedGeneration, "to", promise.GetGeneration())
	promise.Status.ObservedGeneration = promise.GetGeneration()

	// at the beginning of Promise reconciliation, we set promise availability to unavailable so
	// if reconciliation exits at an earlier point, promise is marked as unavailable
	// here we check if the previous promise.status.status is available
	// and set it back to available before we update the promise status to avoid flickering promise availability
	if originalPromiseAvailability == v1alpha1.PromiseStatusAvailable {
		promise.Status.Status = v1alpha1.PromiseStatusAvailable
	}

	return r.updatePromiseStatus(ctx, promise)
}

func (r *PromiseReconciler) nextReconciliation(logger logr.Logger) ctrl.Result {
	logging.Info(logger, "scheduling next reconciliation", "reconciliationInterval", r.ReconciliationInterval)
	return ctrl.Result{RequeueAfter: r.ReconciliationInterval}
}

func promiseAvailableStatusCondition(lastTransitionTime metav1.Time) metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseAvailableConditionType,
		LastTransitionTime: lastTransitionTime,
		Status:             metav1.ConditionTrue,
		Message:            "Ready to fulfil resource requests",
		Reason:             v1alpha1.PromiseAvailableConditionTrueReason,
	}
}

func promiseUnavailableStatusCondition() metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseAvailableConditionType,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionFalse,
		Message:            "Cannot fulfil resource requests",
		Reason:             v1alpha1.PromiseAvailableConditionFalseReason,
	}
}

func promiseAvailablePausedStatusCondition() metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseAvailableConditionType,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionFalse,
		Message:            "Paused",
		Reason:             pausedReconciliationReason,
	}
}

func promiseAvailableSuspendedStatusCondition() metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseAvailableConditionType,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionFalse,
		Message:            "Suspended",
		Reason:             workflowSuspendedReason,
	}
}

func promiseWorksSucceededStatusCondition() metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseWorksSucceededCondition,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionTrue,
		Message:            "All works associated with this promise are ready",
		Reason:             "WorksSucceeded",
	}
}

func promiseWorksSucceededUnknownCondition(pendingWorks []string) metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseWorksSucceededCondition,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionUnknown,
		Message:            fmt.Sprintf("Some works associated with this promise are not ready: %s", pendingWorks),
		Reason:             "WorksPending",
	}
}

func promiseWorksSucceededFailedCondition(failedWorks []string) metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseWorksSucceededCondition,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionFalse,
		Message:            fmt.Sprintf("Some works associated with this promise are not ready: %s", failedWorks),
		Reason:             "WorksFailing",
	}
}

func promiseWorksSucceededMisplacedCondition(misplacedWorks []string) metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseWorksSucceededCondition,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionFalse,
		Message:            fmt.Sprintf("Some works associated with this promise are misplaced: %s", misplacedWorks),
		Reason:             "WorksMisplaced",
	}
}

func promiseReconciledFailingCondition(reason string) metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseReconciledCondition,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionFalse,
		Message:            "Failing",
		Reason:             reason,
	}
}

func promiseReconciledPendingCondition(reason string) metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseReconciledCondition,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionUnknown,
		Message:            "Pending",
		Reason:             reason,
	}
}

func promiseReconciledPausedCondition() metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseReconciledCondition,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionUnknown,
		Message:            "Paused",
		Reason:             pausedReconciliationReason,
	}
}

func promiseReconciledSuspendedCondition() metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseReconciledCondition,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionUnknown,
		Message:            "Suspended",
		Reason:             workflowSuspendedReason,
	}
}

func promiseReconciledCondition() metav1.Condition {
	return metav1.Condition{
		Type:               v1alpha1.PromiseReconciledCondition,
		LastTransitionTime: metav1.Time{Time: time.Now()},
		Status:             metav1.ConditionTrue,
		Message:            "Reconciled",
		Reason:             "Reconciled",
	}
}

func (r *PromiseReconciler) hasPromiseRequirementsChanged(ctx context.Context, promise *v1alpha1.Promise) bool {
	previousRequiredPromises := promise.Status.RequiredPromises
	if len(promise.Spec.RequiredPromises) == 0 && len(previousRequiredPromises) == 0 {
		return false
	}

	latestCondition, latestRequirements := r.generateStatusAndMarkRequirements(ctx, promise)

	requirementsFieldChanged := updateRequirementsStatusOnPromise(promise, promise.Status.RequiredPromises, latestRequirements)
	conditionsFieldChanged := updateConditionOnPromise(promise, latestCondition)

	return conditionsFieldChanged || requirementsFieldChanged
}

func shouldReconcileResources(promise *v1alpha1.Promise) bool {
	if promise.Labels != nil && promise.Labels[resourceutil.ReconcileResourcesLabel] == "true" {
		return true
	}

	if promise.GetGeneration() != promise.Status.ObservedGeneration && promise.GetGeneration() != 1 {
		return true
	}
	return false
}

func (r *PromiseReconciler) removeReconcileResourcesLabel(ctx context.Context, promise *v1alpha1.Promise) error {
	delete(promise.Labels, resourceutil.ReconcileResourcesLabel)
	if err := r.Client.Update(ctx, promise); err != nil {
		return err
	}
	return nil
}

func updateConditionOnPromise(promise *v1alpha1.Promise, latestCondition metav1.Condition) bool {
	return meta.SetStatusCondition(&promise.Status.Conditions, latestCondition)
}

func updateRequirementsStatusOnPromise(promise *v1alpha1.Promise, oldReqs, newReqs []v1alpha1.RequiredPromiseStatus) bool {
	if len(oldReqs)+len(newReqs) == 0 || reflect.DeepEqual(oldReqs, newReqs) {
		return false
	}

	promise.Status.RequiredPromises = newReqs
	return true
}

func (r *PromiseReconciler) generateStatusAndMarkRequirements(ctx context.Context, promise *v1alpha1.Promise) (metav1.Condition, []v1alpha1.RequiredPromiseStatus) {
	condition := metav1.Condition{
		Type:               "RequirementsFulfilled",
		Status:             metav1.ConditionTrue,
		Reason:             "RequirementsInstalled",
		Message:            "Requirements fulfilled",
		LastTransitionTime: metav1.NewTime(time.Now()),
	}

	var requirements []v1alpha1.RequiredPromiseStatus
	for _, req := range promise.Spec.RequiredPromises {
		requirements = append(requirements, r.evaluateRequirement(ctx, promise, req, &condition))
	}

	if condition.Status == metav1.ConditionTrue && len(promise.Spec.RequiredPromises) > 0 {
		r.EventRecorder.Eventf(promise, v1.EventTypeNormal,
			"RequirementsFulfilled", "All required promises are available")
	}

	return condition, requirements
}

func (r *PromiseReconciler) setPromiseStatusToAvailable(ctx context.Context, promise *v1alpha1.Promise, logger logr.Logger) (ctrl.Result, error) {
	promise.Status.Status = v1alpha1.PromiseStatusAvailable
	promise.Status.Kratix.Status = v1alpha1.PromiseStatusAvailable
	timestamp := metav1.Time{Time: time.Now()}

	promise.Status.LastAvailableTime = &timestamp
	promise.Status.Kratix.LastAvailableTime = &timestamp
	logging.Info(logger, "promise status set to Available")
	updateConditionOnPromise(promise, promiseAvailableStatusCondition(timestamp))

	r.EventRecorder.Eventf(promise, "Normal", "Available", "Promise is available")
	return r.updatePromiseStatus(ctx, promise)
}

func (r *PromiseReconciler) evaluateRequirement(ctx context.Context, promise *v1alpha1.Promise, req v1alpha1.RequiredPromise, condition *metav1.Condition) v1alpha1.RequiredPromiseStatus {
	required := &v1alpha1.Promise{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: req.Name}, required)

	var state string
	switch {
	case apierrors.IsNotFound(err):
		state = requirementStateNotInstalled
		updateConditionNotFulfilled(condition, "RequirementsNotInstalled", "Requirements not fulfilled")
		r.EventRecorder.Eventf(promise, v1.EventTypeNormal,
			"RequirementsNotInstalled", fmt.Sprintf("Required Promise %s not installed or unknown state", req.Name))

	case err != nil:
		state = requirementUnknownInstallationState
		updateConditionNotFulfilled(condition, "RequirementsNotInstalled", "Unable to determine if requirements are fulfilled")
		r.EventRecorder.Eventf(promise, v1.EventTypeNormal,
			"RequirementsNotInstalled", fmt.Sprintf("Required Promise %s not installed or unknown state", required.Name))

	default:
		if required.Status.Version != req.Version || required.Status.Status != v1alpha1.PromiseStatusAvailable {
			condition.Reason, state = generateRequirementState(required.Status.Version, req.Version, required.Status.Status)
			updateConditionNotFulfilled(condition, condition.Reason, "Requirements not fulfilled")
			r.EventRecorder.Eventf(promise, v1.EventTypeNormal,
				condition.Reason, fmt.Sprintf("Waiting for required Promise %s: %s ", required.Name, state))
		} else {
			state = requirementStateInstalled
		}

		r.markRequiredPromiseAsRequired(ctx, req.Version, promise, required)
	}

	return v1alpha1.RequiredPromiseStatus{
		Name:    req.Name,
		Version: req.Version,
		State:   state,
	}
}

func (r *PromiseReconciler) reconcileDependenciesAndPromiseWorkflows(o opts, promise *v1alpha1.Promise, unstructuredPromise *unstructured.Unstructured) (passiveRequeue bool, err error) {
	if len(promise.Spec.Dependencies) > 0 {
		logging.Debug(o.logger, "applying static dependencies", "promise", promise.GetName())
		if err := r.applyWorkForStaticDependencies(o, promise); err != nil {
			logging.Error(o.logger, err, "error creating Works")
			return false, err
		}
	}

	if len(promise.Spec.Dependencies) == 0 {
		err := r.deleteWorkForStaticDependencies(o, promise)
		if err != nil {
			return false, err
		}
	}

	pipelineCount := r.getWorkflowsCount(promise)

	if pipelineCount == 0 {
		/* Promise Configure Workflows were removed, wipe any workflow statuses */
		if changed := promise.ClearPipelineExecutionStatus(); changed {
			return false, r.Client.Status().Update(o.ctx, promise)
		}

		// No workflow to run, abort
		return false, nil
	}

	if promise.Status.Workflows != pipelineCount {
		/* New pipelines have been added, regenerate the pipelines execution statuses */
		promise.Status.Workflows = pipelineCount
		return false, r.Client.Status().Update(o.ctx, promise)
	}

	if requeue, err := r.ensureKratixWorkflowStatusIsSetup(promise); err != nil || requeue {
		return requeue, err
	}

	if promise.Labels == nil {
		promise.Labels = make(map[string]string)
	}

	logging.Debug(o.logger, "Promise contains workflows.promise.configure; reconciling workflows")

	completedCond := promise.GetCondition(string(resourceutil.ConfigureWorkflowCompletedCondition))

	forcePipelineRun := passedReconciliationInterval(completedCond, r.ReconciliationInterval) &&
		promise.Labels[resourceutil.WorkflowRunFromStartLabel] != "true"

	reconciledCond := promise.GetCondition(string(resourceutil.ReconciledCondition))
	resumedFromPause := reconciledCond != nil && reconciledCond.Status == metav1.ConditionUnknown && reconciledCond.Reason == pausedReconciliationReason
	promiseSpecChanged := promise.Status.Kratix.Workflows.SuspendedGeneration != 0 && promise.GetGeneration() > promise.Status.Kratix.Workflows.SuspendedGeneration

	if restarted, err := r.restartOnReconciliationInterval(o.ctx, o.logger, promise, completedCond, forcePipelineRun); restarted || err != nil {
		return restarted, err
	}

	if resumedFromPause {
		logging.Info(o.logger, "Promise unpaused; forcing reconciliation")
		promise.Labels[resourceutil.ManualReconciliationLabel] = "true"
		promise.Labels[resourceutil.ReconcileResourcesLabel] = "true"
	}

	if shouldRequeue, suspendErr := r.reconcileSuspendedWorkflow(o, promise,
		promiseSpecChanged); shouldRequeue || suspendErr != nil {
		return shouldRequeue, suspendErr
	}

	pipelineResources, err := promise.GeneratePromisePipelines(v1alpha1.WorkflowActionConfigure, o.logger)
	if err != nil {
		return false, err
	}

	namespace := promise.GetNamespace()
	if promise.Spec.Workflows.Config.PipelineNamespace != "" {
		namespace = promise.Spec.Workflows.Config.PipelineNamespace
	}

	jobOpts := workflow.NewOpts(o.ctx, o.client, r.EventRecorder, o.logger, unstructuredPromise, pipelineResources, "promise", r.NumberOfJobsToKeep, namespace)

	logging.Debug(o.logger, "reconciling configure workflow")
	passiveRequeue, err = reconcileConfigure(jobOpts)
	if err != nil {
		return false, err
	}

	if passiveRequeue {
		return true, nil
	}

	return false, nil
}

func (r *PromiseReconciler) reconcileSuspendedWorkflow(
	o opts,
	promise *v1alpha1.Promise,
	promiseSpecChanged bool,
) (bool, error) {

	if notWorkflowSuspended(promise) {
		return false, nil
	}

	if isManualReconcile(promise) || promiseSpecChanged {
		promise.Labels[resourceutil.WorkflowRunFromStartLabel] = "true"
		if promiseSpecChanged {
			logging.Info(o.logger, "Promise spec changed while suspended; forcing reconciliation", "generation", promise.GetGeneration(), "observedGeneration", promise.Status.ObservedGeneration)
		}
		delete(promise.Labels, v1alpha1.WorkflowSuspendedLabel)
		if err := r.Client.Update(o.ctx, promise); err != nil {
			return true, err
		}
		updatedPromise := &v1alpha1.Promise{}
		if err := r.Client.Get(o.ctx, client.ObjectKeyFromObject(promise), updatedPromise); err != nil {
			return true, err
		}
		resetPromiseWorkflowPipelinesToPending(updatedPromise)
		return true, r.Client.Status().Update(o.ctx, updatedPromise)
	}

	msg := fmt.Sprintf("'%s' label set to 'true' for promise; skipping reconciliation", v1alpha1.WorkflowSuspendedLabel)
	logging.Info(r.Log, msg)
	r.EventRecorder.Event(promise, v1.EventTypeWarning, workflowSuspendedReason, msg)
	return true, r.setWorkflowSuspendedStatusCondition(o.ctx, promise)
}

// Eithers its not set, or its changed, either number of pipelines has changed, or names have changed
func (r *PromiseReconciler) ensureKratixWorkflowStatusIsSetup(promise *v1alpha1.Promise) (bool, error) {
	if len(promise.Status.Kratix.Workflows.Pipelines) != len(promise.Spec.Workflows.Promise.Configure) {
		setNewPipelineStatus(promise)
		return true, r.Client.Status().Update(context.Background(), promise)
	}

	for i, pipelineStatus := range promise.Status.Kratix.Workflows.Pipelines {
		if pipelineStatus.Name != promise.Spec.Workflows.Promise.Configure[i].GetName() {
			setNewPipelineStatus(promise)
			return true, r.Client.Status().Update(context.Background(), promise)
		}
	}

	return false, nil
}

func setNewPipelineStatus(promise *v1alpha1.Promise) {
	workflowPipelinesStatus := []v1alpha1.WorkflowPipelineStatus{}
	for _, pipeline := range promise.Spec.Workflows.Promise.Configure {
		workflowPipelinesStatus = append(workflowPipelinesStatus, v1alpha1.WorkflowPipelineStatus{
			Name:               pipeline.GetName(),
			Phase:              v1alpha1.WorkflowPhasePending,
			LastTransitionTime: metav1.NewTime(time.Now()),
		})
	}
	promise.Status.Kratix.Workflows.Pipelines = workflowPipelinesStatus
}

func (r *PromiseReconciler) reconcileAllRRs(ctx context.Context, rrGVK *schema.GroupVersionKind) error {
	//label all rr with manual reconciliation
	rrs := &unstructured.UnstructuredList{}
	rrListGVK := *rrGVK
	rrListGVK.Kind = rrListGVK.Kind + "List"
	rrs.SetGroupVersionKind(rrListGVK)
	err := r.Client.List(ctx, rrs)
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
		if err := r.Client.Update(ctx, &rr); err != nil {
			return err
		}
	}
	return nil
}

func (r *PromiseReconciler) ensureDynamicControllerIsStarted(promise *v1alpha1.Promise, rrCRD *apiextensionsv1.CustomResourceDefinition, rrGVK *schema.GroupVersionKind, canCreateResources *bool, logger logr.Logger) error {
	controllerName := promise.GetDynamicControllerName(logger)

	// The Dynamic Controller needs to be started once and only once.
	if r.dynamicControllerHasAlreadyStarted(promise, logger) {
		dynamicController := r.StartedDynamicControllers[controllerName]
		logging.Debug(logger, "reusing existing dynamic controller", "controllerName", controllerName, "watchStopped", dynamicController.WatchStopped)
		dynamicController.GVK = rrGVK
		dynamicController.CRD = rrCRD
		dynamicController.Client = r.Client
		dynamicController.Scheme = r.Scheme
		dynamicController.PromiseIdentifier = promise.GetName()
		dynamicController.Log = r.Log.WithName(promise.GetName())
		dynamicController.UID = string(promise.GetUID())[0:5]
		dynamicController.CanCreateResources = canCreateResources
		dynamicController.EventRecorder = r.Manager.GetEventRecorderFor("ResourceRequestController")
		dynamicController.NumberOfJobsToKeep = r.NumberOfJobsToKeep
		dynamicController.ReconciliationInterval = r.ReconciliationInterval
		dynamicController.PromiseUpgrade = r.PromiseUpgrade
		dynamicController.PromiseDestinationSelectors = promise.Spec.DestinationSelectors

		if dynamicController.WatchStopped {
			logging.Debug(logger, "restarting dynamic controller watch", "controllerName", controllerName, "gvk", dynamicController.GVK.String())
			if err := r.restartDynamicControllerWatch(dynamicController); err != nil {
				return err
			}
		}

		return nil
	}
	logging.Info(logger, "starting dynamic controller")

	//temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
	//once resolved, delete dynamic controller rather than disable
	dynamicResourceRequestController := &DynamicResourceRequestController{
		Client:                      r.Client,
		Scheme:                      r.Scheme,
		GVK:                         rrGVK,
		CRD:                         rrCRD,
		PromiseIdentifier:           promise.GetName(),
		PromiseDestinationSelectors: promise.Spec.DestinationSelectors,
		Log:                         r.Log.WithName(promise.GetName()),
		UID:                         string(promise.GetUID())[0:5],
		WatchStopped:                false,
		CanCreateResources:          canCreateResources,
		NumberOfJobsToKeep:          r.NumberOfJobsToKeep,
		ReconciliationInterval:      r.ReconciliationInterval,
		EventRecorder:               r.Manager.GetEventRecorderFor("ResourceRequestController"),
		PromiseUpgrade:              r.PromiseUpgrade,
	}

	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(*rrGVK)

	dynamicController, err := ctrl.NewControllerManagedBy(r.Manager).
		For(unstructuredCRD).
		Watches(
			&batchv1.Job{},
			handler.EnqueueRequestsFromMapFunc(r.jobEventHandler(promise)),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				// Only watch Jobs that are managed by Kratix
				labels := obj.GetLabels()
				return labels != nil && labels[v1alpha1.ManagedByLabel] == v1alpha1.ManagedByLabelValue
			})),
		).
		Watches(
			&v1alpha1.Work{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				work := obj.(*v1alpha1.Work)
				rrName, labelExists := work.Labels[v1alpha1.ResourceNameLabel]
				if !labelExists || work.Labels[v1alpha1.PromiseNameLabel] != promise.GetName() {
					return nil
				}

				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: work.Namespace,
						Name:      rrName,
					},
				}}
			}),
		).
		Watches(
			&v1alpha1.ResourceBinding{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				resourceBinding := obj.(*v1alpha1.ResourceBinding)
				rrName, labelExists := resourceBinding.Labels[v1alpha1.ResourceNameLabel]
				if !labelExists || resourceBinding.Labels[v1alpha1.PromiseNameLabel] != promise.GetName() {
					return nil
				}

				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: resourceBinding.Namespace,
						Name:      rrName,
					},
				}}
			})).
		Build(dynamicResourceRequestController)
	if err != nil {
		return err
	}

	dynamicResourceRequestController.Controller = dynamicController
	r.StartedDynamicControllers[controllerName] = dynamicResourceRequestController
	return nil
}

func (r *PromiseReconciler) dynamicControllerHasAlreadyStarted(promise *v1alpha1.Promise, logger logr.Logger) bool {
	_, ok := r.StartedDynamicControllers[promise.GetDynamicControllerName(logger)]
	return ok
}

func (r *PromiseReconciler) stopDynamicControllerForDeletedPromise(ctx context.Context, promise *v1alpha1.Promise, logger logr.Logger) error {
	controllerName := promise.GetDynamicControllerName(logger)
	dynamicController, exists := r.StartedDynamicControllers[controllerName]
	if !exists {
		return nil
	}

	if dynamicController.WatchStopped {
		logging.Debug(logger, "dynamic controller watch already stopped", "controllerName", controllerName, "gvk", dynamicController.GVK.String())
		return nil
	}

	informerObject := &unstructured.Unstructured{}
	informerObject.SetGroupVersionKind(*dynamicController.GVK)
	if err := r.Manager.GetCache().RemoveInformer(ctx, informerObject); err != nil {
		var errResourceNotCached *ctrlcache.ErrResourceNotCached
		if stderrors.As(err, &errResourceNotCached) {
			logging.Debug(logger, "dynamic controller informer already absent", "gvk", dynamicController.GVK.String())
		} else {
			return err
		}
	}

	dynamicController.WatchStopped = true
	logging.Debug(logger, "dynamic controller watch stopped", "controllerName", controllerName, "gvk", dynamicController.GVK.String())
	return nil
}

func (r *PromiseReconciler) restartDynamicControllerWatch(dynamicController *DynamicResourceRequestController) error {
	informerObject := &unstructured.Unstructured{}
	informerObject.SetGroupVersionKind(*dynamicController.GVK)
	if err := dynamicController.Controller.Watch(
		source.Kind(r.Manager.GetCache(), informerObject, &handler.TypedEnqueueRequestForObject[*unstructured.Unstructured]{}),
	); err != nil {
		return err
	}

	dynamicController.WatchStopped = false
	return nil
}

// jobEventHandler creates a handler that processes Job events and triggers reconciliation
// of the associated resource when Jobs change status or are deleted.
func (r *PromiseReconciler) jobEventHandler(promise *v1alpha1.Promise) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		job := obj.(*batchv1.Job)

		// Extract resource information from Job annotations
		annotations := job.GetAnnotations()
		resourceNamespace, hasNamespace := annotations[v1alpha1.JobResourceNamespaceAnnotation]
		resourceName, hasName := annotations[v1alpha1.JobResourceNameAnnotation]
		_, hasKind := annotations[v1alpha1.JobResourceKindAnnotation]
		_, hasAPIVersion := annotations[v1alpha1.JobResourceAPIVersionAnnotation]

		// Only process Jobs that have the required annotations
		if !hasNamespace || !hasName || !hasKind || !hasAPIVersion {
			return nil
		}

		// Check if this Job belongs to a resource managed by this promise
		jobLabels := job.GetLabels()
		if jobLabels == nil || jobLabels[v1alpha1.PromiseNameLabel] != promise.GetName() {
			return nil
		}

		// Only process resource workflow Jobs (not promise workflow Jobs)
		if jobLabels[v1alpha1.WorkflowTypeLabel] != string(v1alpha1.WorkflowTypeResource) {
			return nil
		}

		// Create a reconcile request for the associated resource
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Namespace: resourceNamespace,
				Name:      resourceName,
			},
		}}
	}
}

// createResourcesForDynamicControllerIfTheyDontExist(ctx, promiseName, logger)
// fetch promise # maybe redundant?
// fetch the promises CRDS
// do the rest as is
func (r *PromiseReconciler) createResourcesForDynamicControllerIfTheyDontExist(ctx context.Context, promise *v1alpha1.Promise,
	rrCRD *apiextensionsv1.CustomResourceDefinition, rrGVK *schema.GroupVersionKind, logger logr.Logger) error {
	cr := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: promise.GetControllerResourceName(),
		},
	}

	logging.Debug(logger, "creating/updating cluster role", "clusterRoleName", cr.GetName())

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, &cr, func() error {
		cr.Rules = promise.GenerateFullAccessForRR(rrGVK.Group, rrCRD.Spec.Names.Plural)
		cr.Labels = labels.Merge(cr.Labels, promise.GenerateSharedLabels())
		// If spec.clusterRolesLabels is set, cascade those labels to the generated ClusterRole.
		cr.Labels = labels.Merge(cr.Labels, map[string]string(promise.GenerateLabelsForClusterRole()))
		return nil
	})

	if err != nil {
		return fmt.Errorf("error creating/updating cluster role: %w", err)
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: promise.GetControllerResourceName(),
		},
	}

	logging.Debug(logger, "creating/updating cluster role binding", "clusterRoleBinding", crb.GetName())
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, crb, func() error {
		crb.RoleRef = rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     cr.Name,
		}
		crb.Subjects = []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: v1alpha1.SystemNamespace,
				Name:      "kratix-platform-controller-manager",
			},
		}
		crb.Labels = labels.Merge(crb.Labels, promise.GenerateSharedLabels())
		return nil
	})

	if err != nil {
		return fmt.Errorf("error creating/updating cluster role binding: %w", err)
	}

	logging.Info(logger, "finished creating resources for dynamic controller")
	return nil
}

func (r *PromiseReconciler) ensureCRDExists(ctx context.Context, promise *v1alpha1.Promise, rrCRD *apiextensionsv1.CustomResourceDefinition, logger logr.Logger) (*ctrl.Result, error) {

	_, err := r.ApiextensionsClient.
		CustomResourceDefinitions().
		Create(ctx, rrCRD, metav1.CreateOptions{})

	if err == nil {
		return &fastRequeue, nil
	}

	if !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("error creating crd: %w", err)
	}

	logging.Debug(logger, "CRD already exists", "crdName", rrCRD.Name)
	existingCRD, err := r.ApiextensionsClient.CustomResourceDefinitions().Get(ctx, rrCRD.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	existingCRD.Spec.Versions = rrCRD.Spec.Versions
	existingCRD.Spec.Conversion = rrCRD.Spec.Conversion
	existingCRD.Spec.PreserveUnknownFields = rrCRD.Spec.PreserveUnknownFields
	existingCRD.Labels = labels.Merge(existingCRD.Labels, rrCRD.Labels)
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
			logging.Debug(logger, "CRD established", "crdName", rrCRD.Name)
			return nil, nil
		}
	}

	logging.Debug(logger, "CRD not yet established", "crdName", rrCRD.Name, "statusConditions", updatedCRD.Status.Conditions)

	return &fastRequeue, nil
}

func (r *PromiseReconciler) updateStatus(promise *v1alpha1.Promise, kind, group, version string) (bool, error) {
	apiVersion := strings.ToLower(group + "/" + version)

	if promise.Status.Kind == kind &&
		promise.Status.Kratix.Kind == kind &&
		promise.Status.APIVersion == apiVersion &&
		promise.Status.Kratix.APIVersion == apiVersion {
		return false, nil
	}

	promise.Status.Kind = kind
	promise.Status.Kratix.Kind = kind
	promise.Status.APIVersion = apiVersion
	promise.Status.Kratix.APIVersion = apiVersion
	return true, r.Client.Status().Update(context.TODO(), promise)
}

func (r *PromiseReconciler) deletePromise(o opts, promise *v1alpha1.Promise) (ctrl.Result, error) {
	logging.Debug(o.logger, "finalizers existing", "finalizers", promise.GetFinalizers())
	if resourceutil.FinalizersAreDeleted(promise, promiseFinalizers) {
		logging.Debug(o.logger, "finalizers all deleted")
		return ctrl.Result{}, nil
	}

	if controllerutil.ContainsFinalizer(promise, runDeleteWorkflowsFinalizer) {
		logging.Info(o.logger, "running promise delete workflows")
		unstructuredPromise, err := promise.ToUnstructured()
		if err != nil {
			return ctrl.Result{}, err
		}
		pipelines, err := promise.GeneratePromisePipelines(v1alpha1.WorkflowActionDelete, o.logger)
		if err != nil {
			return ctrl.Result{}, err
		}

		namespace := promise.GetNamespace()
		if promise.Spec.Workflows.Config.PipelineNamespace != "" {
			namespace = promise.Spec.Workflows.Config.PipelineNamespace
		}
		jobOpts := workflow.NewOpts(o.ctx, o.client, r.EventRecorder, o.logger, unstructuredPromise, pipelines, "promise", r.NumberOfJobsToKeep, namespace)

		requeue, err := reconcileDelete(jobOpts)
		if err != nil {
			if stderrors.Is(err, workflow.ErrDeletePipelineFailed) {
				r.EventRecorder.Event(promise, "Warning", "Failed Pipeline", "The Delete Pipeline has failed")
				condition := metav1.Condition{
					Type:               string(resourceutil.DeleteWorkflowCompletedCondition),
					Status:             metav1.ConditionFalse,
					Message:            "The Delete Pipeline has failed",
					Reason:             resourceutil.DeleteWorkflowCompletedFailedReason,
					LastTransitionTime: metav1.NewTime(time.Now()),
				}
				updateConditionOnPromise(promise, condition)
				if err := r.Client.Status().Update(o.ctx, promise); err != nil {
					logging.Error(o.logger, err, "failed to update promise status", "promise", promise.GetName())
				}
			}
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
		logging.Debug(o.logger, "deleting workflow jobs for finalizer", "finalizer", removeAllWorkflowJobsFinalizer)
		err := r.deletePromiseWorkflowJobs(o, promise, removeAllWorkflowJobsFinalizer)
		if err != nil {
			return ctrl.Result{}, err
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(promise, resourceRequestCleanupFinalizer) {
		logging.Debug(o.logger, "deleting resources for finalizer", "finalizer", resourceRequestCleanupFinalizer)
		err := r.deleteResourceRequests(o, promise)
		if err != nil {
			return ctrl.Result{}, err
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(promise, revisionCleanupFinalizer) {
		logging.Debug(o.logger, "deleting revision for finalizer", "finalizer", revisionCleanupFinalizer)
		err := r.deleteRevisions(o, promise)
		if err != nil {
			return ctrl.Result{}, err
		}
		return fastRequeue, nil
	}

	if err := r.stopDynamicControllerForDeletedPromise(o.ctx, promise, o.logger); err != nil {
		logging.Error(o.logger, err, "failed to stop dynamic controller watch")
		return defaultRequeue, nil
	}

	if controllerutil.ContainsFinalizer(promise, dynamicControllerDependantResourcesCleanupFinalizer) {
		logging.Debug(o.logger, "deleting dependent resources for finalizer", "finalizer", dynamicControllerDependantResourcesCleanupFinalizer)
		err := r.deleteDynamicControllerAndWorkflowResources(o, promise)
		if err != nil {
			return defaultRequeue, nil //nolint:nilerr // requeue rather than exponential backoff
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(promise, dependenciesCleanupFinalizer) {
		logging.Debug(o.logger, "deleting work for finalizer", "finalizer", dependenciesCleanupFinalizer)
		err := r.deleteWork(o, promise)
		if err != nil {
			return ctrl.Result{}, err
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(promise, crdCleanupFinalizer) {
		logging.Debug(o.logger, "deleting CRDs for finalizer", "finalizer", crdCleanupFinalizer)
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

	jobLabels := map[string]string{
		v1alpha1.PromiseNameLabel:  promise.GetName(),
		v1alpha1.WorkflowTypeLabel: string(v1alpha1.WorkflowTypePromise),
	}

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, &jobGVK, jobLabels)
	if err != nil {
		return err
	}

	// TODO: this part will be deprecated when we stop using the legacy labels
	jobLegacyLabels := map[string]string{
		v1alpha1.PromiseNameLabel: promise.GetName(),
		v1alpha1.WorkTypeLabel:    v1alpha1.WorkTypePromise,
	}
	legacyResourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, &jobGVK, jobLegacyLabels)
	if err != nil {
		return err
	}

	if !resourcesRemaining || !legacyResourcesRemaining {
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
			resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, &gvk, promise.GenerateSharedLabels())
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

	requestList := &unstructured.UnstructuredList{}
	requestList.SetGroupVersionKind(*rrGVK)
	if err := o.client.List(o.ctx, requestList); err != nil {
		return err
	}

	if len(requestList.Items) == 0 {
		controllerutil.RemoveFinalizer(promise, resourceRequestCleanupFinalizer)
		return r.Client.Update(o.ctx, promise)
	}

	parentAnnotations := promise.GetAnnotations()
	for i := range requestList.Items {
		rr := &requestList.Items[i]
		if err := ensureTraceAnnotations(o.ctx, o.client, rr, parentAnnotations); err != nil {
			return err
		}
		if err := o.client.Delete(o.ctx, rr, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}

	var canCreateResources bool
	err = r.ensureDynamicControllerIsStarted(promise, rrCRD, rrGVK, &canCreateResources, o.logger)
	if err != nil {
		return err
	}

	return nil
}

func (r *PromiseReconciler) deleteCRDs(o opts, promise *v1alpha1.Promise) error {
	_, rrCRD, err := promise.GetAPI()
	if err != nil {
		logging.Error(o.logger, err, "Failed unmarshalling CRD, skipping deletion")
		controllerutil.RemoveFinalizer(promise, crdCleanupFinalizer)
		if err := r.Client.Update(o.ctx, promise); err != nil {
			return err
		}
		return nil
	}

	_, err = r.ApiextensionsClient.CustomResourceDefinitions().Get(o.ctx, rrCRD.GetName(), metav1.GetOptions{})

	if apierrors.IsNotFound(err) {
		controllerutil.RemoveFinalizer(promise, crdCleanupFinalizer)
		return r.Client.Update(o.ctx, promise)
	}

	return r.ApiextensionsClient.
		CustomResourceDefinitions().
		Delete(o.ctx, rrCRD.GetName(), metav1.DeleteOptions{})
}

func (r *PromiseReconciler) deleteRevisions(o opts, promise *v1alpha1.Promise) error {
	revisionList := &v1alpha1.PromiseRevisionList{}
	if err := o.client.List(o.ctx, revisionList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(promise.GenerateSharedLabels()),
	}); err != nil {
		return err
	}

	for i := range revisionList.Items {
		revision := &revisionList.Items[i]
		if err := o.client.Delete(o.ctx, revision, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}

	controllerutil.RemoveFinalizer(promise, revisionCleanupFinalizer)
	return r.Client.Update(o.ctx, promise)
}

func (r *PromiseReconciler) deleteWork(o opts, promise *v1alpha1.Promise) error {
	workGVK := schema.GroupVersionKind{
		Group:   v1alpha1.GroupVersion.Group,
		Version: v1alpha1.GroupVersion.Version,
		Kind:    "Work",
	}

	workList := &unstructured.UnstructuredList{}
	workList.SetGroupVersionKind(workGVK)
	selector := labels.SelectorFromSet(promise.GenerateSharedLabels())
	if err := o.client.List(o.ctx, workList, &client.ListOptions{LabelSelector: selector}); err != nil {
		return err
	}

	if len(workList.Items) == 0 {
		controllerutil.RemoveFinalizer(promise, dependenciesCleanupFinalizer)
		return r.Client.Update(o.ctx, promise)
	}

	parentAnnotations := promise.GetAnnotations()
	for i := range workList.Items {
		work := &workList.Items[i]
		if err := ensureTraceAnnotations(o.ctx, o.client, work, parentAnnotations); err != nil {
			return err
		}
		if err := o.client.Delete(o.ctx, work, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil && !apierrors.IsNotFound(err) {
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

func (r *PromiseReconciler) promiseFinalizers(promise *v1alpha1.Promise) []string {
	if promise == nil {
		return nil
	}

	desired := make(map[string]struct{}, len(promiseFinalizers))

	desired[removeAllWorkflowJobsFinalizer] = struct{}{}

	desired[dependenciesCleanupFinalizer] = struct{}{}
	if r.PromiseUpgrade {
		desired[revisionCleanupFinalizer] = struct{}{}
	}

	if promise.ContainsAPI() {
		desired[resourceRequestCleanupFinalizer] = struct{}{}
		desired[dynamicControllerDependantResourcesCleanupFinalizer] = struct{}{}
		desired[crdCleanupFinalizer] = struct{}{}
	}

	if promise.HasPipeline(v1alpha1.WorkflowTypePromise, v1alpha1.WorkflowActionDelete) {
		desired[runDeleteWorkflowsFinalizer] = struct{}{}
	}

	finalizers := make([]string, 0, len(desired))
	for _, name := range promiseFinalizers {
		if _, ok := desired[name]; ok {
			finalizers = append(finalizers, name)
		}
	}
	return finalizers
}

func generateCRDAndGVK(promise *v1alpha1.Promise, logger logr.Logger) (*apiextensionsv1.CustomResourceDefinition, *schema.GroupVersionKind, error) {
	rrGVK, rrCRD, err := promise.GetAPI()
	if err != nil {
		logging.Error(logger, err, "Failed unmarshalling CRD")
		return nil, nil, err
	}
	rrCRD.Labels = labels.Merge(rrCRD.Labels, promise.GenerateSharedLabels())

	setStatusFieldsOnCRD(rrCRD)

	return rrCRD, rrGVK, nil
}

func setStatusFieldsOnCRD(rrCRD *apiextensionsv1.CustomResourceDefinition) {
	for i := range rrCRD.Spec.Versions {
		rrCRD.Spec.Versions[i].Subresources = &apiextensionsv1.CustomResourceSubresources{
			Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
		}

		if len(rrCRD.Spec.Versions[i].AdditionalPrinterColumns) == 0 {
			rrCRD.Spec.Versions[i].AdditionalPrinterColumns = []apiextensionsv1.CustomResourceColumnDefinition{
				{
					Name:     "message",
					Type:     "string",
					JSONPath: ".status.message",
				},
				{
					Name:     "status",
					Type:     "string",
					JSONPath: ".status.conditions[?(@.type==\"Reconciled\")].message",
				},
			}
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
				"workflows": {
					Type:   "integer",
					Format: "int64",
				},
				"workflowsSucceeded": {
					Type:   "integer",
					Format: "int64",
				},
				"workflowsFailed": {
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
				"kratix": {
					Type: "object",
					Properties: map[string]apiextensionsv1.JSONSchemaProps{
						"workflows": {
							Type: "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"lastSuccessfulConfigureWorkflowTime": {
									Type: "string",
								},
								"pipelines": {
									Type: "array",
									Items: &apiextensionsv1.JSONSchemaPropsOrArray{
										Schema: &apiextensionsv1.JSONSchemaProps{
											Type: "object",
											Properties: map[string]apiextensionsv1.JSONSchemaProps{
												"name": {
													Type: "string",
												},
												"phase": {
													Type: "string",
												},
												"message": {
													Type: "string",
												},
												"lastTransitionTime": {
													Type:   "string",
													Format: "datetime",
												},
											},
										},
									},
								},
								"suspendedGeneration": {
									Type:   "integer",
									Format: "int64",
								},
							},
						},
					},
				},
			},
		}
	}
}

func (r *PromiseReconciler) applyWorkForStaticDependencies(o opts, promise *v1alpha1.Promise) error {
	name := objectutil.GenerateObjectName(promise.GetName() + "-static-deps")
	work, err := v1alpha1.NewPromiseDependenciesWork(promise, name)
	if err != nil {
		return err
	}
	work.SetLabels(
		labels.Merge(
			work.GetLabels(),
			resourceutil.GetWorkLabels(promise.GetName(), "", "", "", v1alpha1.WorkTypeStaticDependency),
		),
	)

	existingWork, err := resourceutil.GetWork(r.Client, v1alpha1.SystemNamespace, work.GetLabels())
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

		ann := existingWork.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		ann[lastUpdatedAtAnnotation] = time.Now().Local().String()
		existingWork.SetAnnotations(ann)

		err = r.Client.Update(o.ctx, existingWork)
	}

	if err != nil {
		return err
	}

	logging.Debug(o.logger, "resource reconciled", "operation", op, "namespace", work.GetNamespace(), "name", work.GetName(), "gvk", work.GroupVersionKind().String())
	return nil
}

func (r *PromiseReconciler) deleteWorkForStaticDependencies(o opts, promise *v1alpha1.Promise) error {
	labels := resourceutil.GetWorkLabels(promise.GetName(), "", "", "", v1alpha1.WorkTypeStaticDependency)

	existingWork, err := resourceutil.GetWork(r.Client, v1alpha1.SystemNamespace, labels)
	if err != nil {
		return err
	}

	if existingWork == nil {
		return nil
	}

	logging.Debug(o.logger, "deleting work for static dependencies", "namespace", existingWork.GetNamespace(), "name", existingWork.GetName())
	return r.Client.Delete(o.ctx, existingWork)
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
		logging.Error(r.Log, err, "error updating promise required by promise", "promise", promise.GetName(), "requiredPromise", requiredPromise.GetName())
	}
}

func (r *PromiseReconciler) updatePromiseStatus(ctx context.Context, promise *v1alpha1.Promise) (ctrl.Result, error) {
	logging.Debug(r.Log, "updating Promise status", "promise", promise.Name, "status", promise.Status.Kratix.Status)
	err := r.Client.Status().Update(ctx, promise)
	if apierrors.IsConflict(err) {
		logging.Debug(r.Log, "failed to update Promise status due to update conflict; requeueing")
		return fastRequeue, nil
	}
	return ctrl.Result{}, err
}

func (r *PromiseReconciler) getWorkflowsCount(promise *v1alpha1.Promise) int64 {
	if !promise.HasPipeline(v1alpha1.WorkflowTypePromise, v1alpha1.WorkflowActionConfigure) {
		return int64(0)
	}
	return int64(len(promise.Spec.Workflows.Promise.Configure))
}

func updateConditionNotFulfilled(condition *metav1.Condition, reason, message string) {
	if condition.Status != metav1.ConditionUnknown {
		condition.Status = metav1.ConditionFalse
	}
	condition.Reason = reason
	condition.Message = message
}

func generateRequirementState(fetchedVersion, requiredVersion, availability string) (string, string) {
	if fetchedVersion != requiredVersion {
		return "RequirementsNotInstalled", requirementStateNotInstalledAtSpecifiedVersion
	}
	if availability != v1alpha1.PromiseStatusAvailable {
		return "RequirementsNotAvailable", requirementStateNotAvailable
	}
	return "", ""
}

func addPromiseSpanAttributes(traceCtx *reconcileTrace, promise *v1alpha1.Promise) {
	traceCtx.AddAttributes(
		attribute.String("kratix.promise.name", promise.GetName()),
		attribute.Int64("kratix.promise.generation", promise.GetGeneration()),
		attribute.String("kratix.action", traceCtx.Action()),
	)
}

func passedReconciliationInterval(completedCond *metav1.Condition, reconciliationInterval time.Duration) bool {
	return completedCond != nil &&
		completedCond.Status == metav1.ConditionTrue &&
		time.Since(completedCond.LastTransitionTime.Time) > reconciliationInterval
}

func (r *PromiseReconciler) restartOnReconciliationInterval(
	ctx context.Context,
	logger logr.Logger,
	promise *v1alpha1.Promise,
	completedCond *metav1.Condition,
	forcePipelineRun bool,
) (bool, error) {
	if forcePipelineRun && !isManualReconcile(promise) {
		logging.Trace(logger, "pipeline completed too long ago; forcing reconciliation", "lastTransitionTime", completedCond.LastTransitionTime.String())
		promise.Labels[resourceutil.ManualReconciliationLabel] = "true"
		return true, r.Client.Update(ctx, promise)
	}

	return false, nil
}
