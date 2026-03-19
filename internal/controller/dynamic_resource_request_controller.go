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

package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/objectutil"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"

	"time"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"

	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crcontroller "sigs.k8s.io/controller-runtime/pkg/controller"

	"go.opentelemetry.io/otel/attribute"
)

const (
	workFinalizer                     = v1alpha1.KratixPrefix + "work-cleanup"
	resourceBindingFinalizer          = v1alpha1.KratixPrefix + "resource-binding-cleanup"
	resourcePromiseVersionStatus      = "promiseVersion"
	resourceBindingVersionStatus      = "resourceBindingVersion"
	promiseRevisionLookupFailedReason = "FailedPromiseRevisionLookup"
)

type DynamicResourceRequestController struct {
	//use same naming conventions as other controllers
	Client                      client.Client
	GVK                         *schema.GroupVersionKind
	Scheme                      *runtime.Scheme
	PromiseIdentifier           string
	Log                         logr.Logger
	UID                         string
	WatchStopped                bool
	CRD                         *apiextensionsv1.CustomResourceDefinition
	Controller                  crcontroller.Controller
	PromiseDestinationSelectors []v1alpha1.PromiseScheduling
	CanCreateResources          *bool
	NumberOfJobsToKeep          int
	ReconciliationInterval      time.Duration
	EventRecorder               record.EventRecorder
	PromiseUpgrade              bool
}

//+kubebuilder:rbac:groups="batch",resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile reconciles a Dynamically Generated Resource object.
func (r *DynamicResourceRequestController) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	if r.WatchStopped {
		// WatchStopped means the controller's informer no longer watches the CRD.
		// This effectively shuts down the controller because it is no longer
		// watching the CRD.
		//
		// The one exception is that it still watches Jobs, Work, and
		// ResourceBindings with the corresponding labels. In practice, none of
		// those should exist when the Promise is deleted, so this should be
		// harmless.
		return ctrl.Result{}, nil
	}

	baseLogger := r.Log.WithValues(
		"controller", "dynamicResourceRequest",
		"uid", r.UID,
		"name", req.Name,
		"namespace", req.Namespace,
	)
	baseLogger = withPromiseAndResourceRequest(baseLogger, r.PromiseIdentifier, req.Namespace, req.Name)

	rr := &unstructured.Unstructured{}
	rr.SetGroupVersionKind(*r.GVK)

	promise := &v1alpha1.Promise{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: r.PromiseIdentifier}, promise); err != nil {
		logging.Error(baseLogger, err, "failed to get promise")
		return ctrl.Result{}, err
	}

	if err := r.Client.Get(ctx, req.NamespacedName, rr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logging.Error(baseLogger, err, "failed to get resource request")
		return defaultRequeue, nil
	}

	baseLogger = baseLogger.WithValues(
		"generation", rr.GetGeneration(),
	)

	spanName := fmt.Sprintf("%s/DynamicResourceRequestReconcile", r.PromiseIdentifier)
	ctx, logger, traceCtx := setupReconcileTrace(ctx, "dynamic-resource-request-controller", spanName, rr, baseLogger)
	defer finishReconcileTrace(traceCtx, &retErr)()

	logging.Info(logger, "reconciliation started")
	defer logReconcileDuration(logger, time.Now(), result, retErr)()

	addDynamicResourceRequestSpanAttributes(traceCtx, promise, rr)

	if err := persistReconcileTrace(traceCtx, r.Client, logger); err != nil {
		logging.Error(logger, err, "failed to persist trace annotations")
		return ctrl.Result{}, err
	}

	if v, ok := promise.Labels[pauseReconciliationLabel]; ok && v == "true" {
		msg := fmt.Sprintf("'%s' label set to 'true' for promise; pausing reconciliation for this resource request",
			pauseReconciliationLabel)
		logging.Info(logger, msg)
		return ctrl.Result{}, r.setPausedReconciliationStatusConditions(ctx, rr, msg)
	}

	if v, ok := rr.GetLabels()[pauseReconciliationLabel]; ok && v == "true" {
		msg := fmt.Sprintf("'%s' label set to 'true' for this resource request; pausing reconciliation",
			pauseReconciliationLabel)
		logging.Info(logger, msg)
		return ctrl.Result{}, r.setPausedReconciliationStatusConditions(ctx, rr, msg)
	}

	resourceLabels := getResourceLabels(rr)
	if resourceLabels[v1alpha1.PromiseNameLabel] != r.PromiseIdentifier {
		if err := r.setPromiseLabels(ctx, promise.GetName(), rr, resourceLabels, logger); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	opts := opts{client: r.Client, ctx: ctx, logger: logger}

	var (
		promiseRevisionUsed *v1alpha1.PromiseRevision
		bindingVersion      string
	)
	if r.PromiseUpgrade {
		logging.Trace(baseLogger,
			"PromiseUpgrade feature flag set to true; will reconcile with a PromiseRevision.")

		var err error
		promiseRevisionUsed, bindingVersion, err = getPromiseRevisionToUse(ctx, rr, baseLogger, r, promise)
		if err != nil {
			return ctrl.Result{}, err
		}

		promise.Spec = promiseRevisionUsed.Spec.PromiseSpec
		logging.Debug(baseLogger,
			"Found PromiseRevision from ResourceRequest", "revision name", promiseRevisionUsed.Name)
		r.EventRecorder.Eventf(rr, v1.EventTypeNormal, "ReconcileStarted",
			fmt.Sprintf("reconciling resource request with promise revision %s", promiseRevisionUsed.Name))
	}

	if !rr.GetDeletionTimestamp().IsZero() {
		logging.Info(logger, "deleting resource request")
		return r.deleteResources(opts, promise, rr)
	}

	if !*r.CanCreateResources {
		return r.ensurePromiseIsUnavailable(ctx, rr, logger)
	}

	if resourceutil.IsPromiseMarkedAsUnavailable(rr) {
		if err := r.ensurePromiseIsAvailable(ctx, rr, logger); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if resourceutil.FinalizersAreMissing(
		rr, []string{workFinalizer, removeAllWorkflowJobsFinalizer, runDeleteWorkflowsFinalizer},
	) {
		if err := addFinalizers(opts, rr, r.getRRFinalizers()); err != nil {
			if apierrors.IsConflict(err) {
				return fastRequeue, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if r.PromiseUpgrade {
		err := r.updateResourceBinding(ctx, logger, rr, promise)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	logging.Info(logger, "resource contains configure workflow(s); reconciling workflows")
	completedCond := resourceutil.GetCondition(rr, resourceutil.ConfigureWorkflowCompletedCondition)
	forcePipelineRun := shouldForcePipelineRun(completedCond, r.ReconciliationInterval) &&
		rr.GetLabels()[resourceutil.WorkflowRestartLabel] != "true"
	isWorkflowSuspended := rr.GetLabels()[v1alpha1.WorkflowSuspendLabel] == "true"

	if restarted, err := r.restartOnReconciliationInterval(opts.ctx, logger, rr,
		completedCond, forcePipelineRun, isWorkflowSuspended); restarted || err != nil {
		return ctrl.Result{}, err
	}

	pipelineResources, err := promise.GenerateResourcePipelines(v1alpha1.WorkflowActionConfigure, rr, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	if updated, err := r.ensureConfigureWorkflowStatus(ctx, logger, rr, pipelineResources); updated || err != nil {
		return ctrl.Result{}, err
	}

	if shouldRequeue, err := r.reconcileSuspendedWorkflow(ctx, logger, rr,
		pipelineResources, completedCond, forcePipelineRun); shouldRequeue || err != nil {
		return ctrl.Result{}, err
	}

	namespace := rr.GetNamespace()
	if promise.WorkflowPipelineNamespaceSet() {
		namespace = promise.Spec.Workflows.Config.PipelineNamespace
	}
	jobOpts := workflow.NewOpts(
		ctx,
		r.Client,
		r.EventRecorder,
		logger,
		rr,
		pipelineResources,
		"resource",
		r.NumberOfJobsToKeep,
		namespace,
	)

	passiveRequeue, err := reconcileConfigure(jobOpts)
	if err != nil {
		return ctrl.Result{}, err
	}

	if passiveRequeue {
		return ctrl.Result{}, nil
	}

	if rr.GetGeneration() != resourceutil.GetObservedGeneration(rr) {
		return ctrl.Result{}, updateObservedGeneration(rr, opts, logger)
	}

	if !promise.HasPipeline(v1alpha1.WorkflowTypeResource, v1alpha1.WorkflowActionConfigure) {
		return r.nextReconciliation(logger), r.cleanupWorkflowCountersAndExecution(ctx, logger, rr)
	}

	rrNamespace := ""
	if promise.WorkflowPipelineNamespaceSet() {
		rrNamespace = rr.GetNamespace()
	}
	workLabels := resourceutil.GetWorkLabels(r.PromiseIdentifier, rr.GetName(), rrNamespace, "", v1alpha1.WorkTypeResource)

	statusUpdate, err := r.generateResourceStatus(ctx, logger, rr, int64(len(pipelineResources)), workLabels, bindingVersion, promiseRevisionUsed)
	if err != nil {
		return ctrl.Result{}, err
	}

	if statusUpdate {
		return ctrl.Result{}, r.Client.Status().Update(ctx, rr)
	}

	workflowCompletedCondition := resourceutil.GetCondition(rr, resourceutil.ConfigureWorkflowCompletedCondition)
	if workflowsCompletedSuccessfully(workflowCompletedCondition) {
		if shouldUpdateLastSuccessfulConfigureWorkflowTime(workflowCompletedCondition, rr) {
			if err := updateLastSuccessfulConfigureWorkflowTime(workflowCompletedCondition, rr, opts); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
		return r.nextReconciliation(logger), nil
	}

	if r.PromiseUpgrade {
		if r.updatePromiseVersionStatus(logger, rr, bindingVersion, promiseRevisionUsed) {
			return ctrl.Result{}, r.Client.Status().Update(ctx, rr)
		}
	}

	return ctrl.Result{}, nil
}

func (r *DynamicResourceRequestController) updateResourceBinding(ctx context.Context, logger logr.Logger, rr *unstructured.Unstructured, promise *v1alpha1.Promise) error {
	resourceBinding := &v1alpha1.ResourceBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objectutil.GenerateDeterministicObjectName(fmt.Sprintf("%s-%s", rr.GetName(), promise.GetName())),
			Namespace: rr.GetNamespace(),
		},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, resourceBinding, func() error {
		resourceBinding.SetLabels(resourceBindingLabels(rr, promise))
		if resourceBinding.Spec.Version == "" {
			resourceBinding.Spec.Version = "latest"
			existingPromiseVersion := resourceutil.GetStatus(rr, resourceBindingVersionStatus)
			if existingPromiseVersion != "" {
				resourceBinding.Spec.Version = existingPromiseVersion
			}
		}

		resourceBinding.Spec.PromiseRef = v1alpha1.PromiseRef{Name: promise.GetName()}
		resourceBinding.Spec.ResourceRef = v1alpha1.ResourceRef{
			Name:      rr.GetName(),
			Namespace: rr.GetNamespace(),
		}

		return nil
	})
	if err != nil {
		return err
	}

	logging.Debug(logger, "ResourceBinding reconciled for Resource",
		"operation", op,
		"promiseName", resourceBinding.Spec.PromiseRef.Name,
		"promiseVersion", resourceBinding.Spec.Version,
		"resourceName", resourceBinding.Spec.ResourceRef.Name,
		"resourceNamespace", resourceBinding.Spec.ResourceRef.Namespace,
	)

	if op == "created" {
		r.EventRecorder.Event(rr, v1.EventTypeNormal, "BindingCreated",
			fmt.Sprintf("Binding %s created for promise %s version %s",
				resourceBinding.GetName(),
				promise.GetName(),
				resourceBinding.Spec.Version))
	}

	return nil
}

func (r *DynamicResourceRequestController) ensureConfigureWorkflowStatus(
	ctx context.Context,
	logger logr.Logger,
	rr *unstructured.Unstructured,
	pipelineResources []v1alpha1.PipelineJobResources,
) (updated bool, err error) {
	statusChanged := false
	if resourceutil.GetWorkflowsCounterStatus(rr, "workflows") != int64(len(pipelineResources)) {
		resourceutil.SetStatus(rr, logger, "workflows", int64(len(pipelineResources)))
		statusChanged = true
	}

	workflowStatusChanged, err := ensureRRKratixWorkflowStatusIsSetup(rr, pipelineResources)
	if err != nil {
		return false, err
	}
	if workflowStatusChanged {
		statusChanged = true
	}

	if statusChanged {
		return true, r.Client.Status().Update(ctx, rr)
	}

	return false, nil
}

func (r *DynamicResourceRequestController) reconcileSuspendedWorkflow(
	ctx context.Context,
	logger logr.Logger,
	rr *unstructured.Unstructured,
	pipelineResources []v1alpha1.PipelineJobResources,
	completedCond *clusterv1.Condition,
	forcePipelineRun bool,
) (shouldRequeue bool, err error) {
	isWorkflowSuspended := rr.GetLabels()[v1alpha1.WorkflowSuspendLabel] == "true"
	resourceSpecChanged := resourceutil.GetKratixWorkflowsInt64Status(rr, "suspendedGeneration") != 0 &&
		rr.GetGeneration() > resourceutil.GetKratixWorkflowsInt64Status(rr, "suspendedGeneration")

	if isWorkflowSuspended && (forcePipelineRun || r.manualReconciliationLabelSet(rr) || resourceSpecChanged) {
		if forcePipelineRun {
			logging.Trace(logger, "resource configure pipeline completed too long ago while suspended; forcing reconciliation",
				"lastTransitionTime", completedCond.LastTransitionTime.String())
		}
		if resourceSpecChanged {
			logging.Info(logger, "Resource request spec changed while suspended; forcing reconciliation",
				"generation", rr.GetGeneration(), "observedGeneration", resourceutil.GetObservedGeneration(rr))
		}
		resourceLabels := rr.GetLabels()
		if resourceLabels == nil {
			resourceLabels = map[string]string{}
		}
		resourceLabels[resourceutil.WorkflowRestartLabel] = "true"
		delete(resourceLabels, v1alpha1.WorkflowSuspendLabel)
		rr.SetLabels(resourceLabels)
		if err := r.Client.Update(ctx, rr); err != nil {
			return true, err
		}
		updatedRR := &unstructured.Unstructured{}
		updatedRR.SetGroupVersionKind(*r.GVK)
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(rr), updatedRR); err != nil {
			return true, err
		}
		if err := resourceutil.ResetPipelineStatusToPending(updatedRR, pipelineResources); err != nil {
			return true, err
		}
		return true, r.Client.Status().Update(ctx, updatedRR)
	}

	if isWorkflowSuspended {
		msg := fmt.Sprintf("'%s' label set to 'true' for resource request; skipping reconciliation", v1alpha1.WorkflowSuspendLabel)
		logging.Info(logger, msg)
		r.EventRecorder.Event(rr, v1.EventTypeWarning, workflowSuspendedReason, msg)
		return true, r.setWorkflowSuspendedStatusCondition(ctx, rr)
	}

	return false, nil
}

func (r *DynamicResourceRequestController) generateResourceStatus(ctx context.Context, logger logr.Logger, rr *unstructured.Unstructured,
	numberOfPipelines int64, workLabels map[string]string, bindingVersion string, promiseRevision *v1alpha1.PromiseRevision) (bool, error) {
	failed, misplaced, pending, ready, err := r.getWorksStatus(ctx, logger, rr, workLabels)
	if err != nil {
		return false, err
	}
	worksSucceededUpdate := r.updateWorksSucceededCondition(rr, failed, pending, ready, misplaced)
	reconciledUpdate := r.updateReconciledCondition(rr)
	workflowsCounterStatusUpdate := r.generateWorkflowsCounterStatus(logger, rr, numberOfPipelines)
	promiseVersionUpdate := r.updatePromiseVersionStatus(logger, rr, bindingVersion, promiseRevision)

	return worksSucceededUpdate || reconciledUpdate || workflowsCounterStatusUpdate || promiseVersionUpdate, nil
}

func (r *DynamicResourceRequestController) updateWorksSucceededCondition(rr *unstructured.Unstructured, failed, pending, _, misplaced []string) bool {
	cond := resourceutil.GetCondition(rr, resourceutil.WorksSucceededCondition)
	if len(failed) > 0 {
		if cond == nil || cond.Status == v1.ConditionTrue {
			resourceutil.MarkResourceRequestAsWorksFailed(rr, failed)
			r.EventRecorder.Event(rr, v1.EventTypeWarning, "WorksFailing",
				fmt.Sprintf("Some works associated with this resource failed: [%s]", strings.Join(failed, ",")))
			return true
		}
		return false
	}
	if len(pending) > 0 {
		if cond == nil || cond.Status != v1.ConditionUnknown {
			resourceutil.MarkResourceRequestAsWorksPending(rr, pending)
			return true
		}
		return false
	}
	if len(misplaced) > 0 {
		if cond == nil || cond.Status != v1.ConditionFalse || cond.Reason != "WorksMisplaced" {
			resourceutil.MarkResourceRequestAsWorksMisplaced(rr, misplaced)
			r.EventRecorder.Event(rr, v1.EventTypeWarning, "WorksMisplaced",
				fmt.Sprintf("Some works associated with this resource are misplaced: [%s]", strings.Join(misplaced, ",")))
			return true
		}
		return false
	}
	if cond == nil || cond.Status != v1.ConditionTrue {
		resourceutil.MarkResourceRequestAsWorksSucceeded(rr)
		r.EventRecorder.Event(rr, v1.EventTypeNormal, "WorksSucceeded",
			"All works associated with this resource are ready")
		return true
	}
	return false
}

func (r *DynamicResourceRequestController) updateReconciledCondition(rr *unstructured.Unstructured) bool {
	worksSucceeded := resourceutil.GetCondition(rr, resourceutil.WorksSucceededCondition)
	workflowCompleted := resourceutil.GetCondition(rr, resourceutil.ConfigureWorkflowCompletedCondition)
	reconciled := resourceutil.GetCondition(rr, resourceutil.ReconciledCondition)

	var updated bool
	if workflowCompleted != nil &&
		workflowCompleted.Status == v1.ConditionFalse && workflowCompleted.Reason == "PipelinesInProgress" {
		if reconciled == nil || reconciled.Status != v1.ConditionUnknown {
			resourceutil.MarkReconciledPending(rr, "WorkflowPending")
			updated = true
		}
	} else if workflowCompleted != nil && workflowCompleted.Status == v1.ConditionFalse {
		if reconciled == nil || reconciled.Status != v1.ConditionFalse ||
			reconciled.Reason != resourceutil.ConfigureWorkflowCompletedFailedReason {
			resourceutil.MarkReconciledFailing(rr, resourceutil.ConfigureWorkflowCompletedFailedReason)
			updated = true
		}
	} else if worksSucceeded != nil && worksSucceeded.Status == v1.ConditionUnknown {
		if reconciled == nil || reconciled.Status != v1.ConditionUnknown {
			resourceutil.MarkReconciledPending(rr, "WorksPending")
			updated = true
		}
	} else if worksSucceeded != nil && worksSucceeded.Status == v1.ConditionFalse {
		if reconciled == nil || reconciled.Status != v1.ConditionFalse {
			resourceutil.MarkReconciledFailing(rr, "WorksFailing")
			updated = true
		}
	} else if workflowCompleted != nil && worksSucceeded != nil &&
		workflowCompleted.Status == v1.ConditionTrue && worksSucceeded.Status == v1.ConditionTrue {
		if reconciled == nil || reconciled.Status != v1.ConditionTrue {
			resourceutil.MarkReconciledTrue(rr)
			updated = true
			r.EventRecorder.Event(rr, v1.EventTypeNormal, "ReconcileSucceeded",
				"Successfully reconciled")
		}
	}
	return updated
}

func (r *DynamicResourceRequestController) updatePromiseVersionStatus(logger logr.Logger, rr *unstructured.Unstructured, bindingVersion string, promiseRevision *v1alpha1.PromiseRevision) bool {
	logging.Trace(logger, "Checking if we need to update the promise version in the status")
	if !r.PromiseUpgrade || promiseRevision == nil {
		logging.Trace(logger, "Feature flag disabled or no PromiseRevision: no update promise version required")
		return false
	}

	versionUpdated := false
	currentVersion := resourceutil.GetStatus(rr, resourcePromiseVersionStatus)
	if currentVersion != promiseRevision.Spec.Version {
		resourceutil.SetStatus(rr, logger, resourcePromiseVersionStatus, promiseRevision.Spec.Version)
		r.EventRecorder.Eventf(rr, v1.EventTypeNormal, "ReconcileSucceeded",
			"Resource request reconciled with promise %s version %s",
			promiseRevision.Spec.PromiseRef.Name,
			promiseRevision.Spec.Version)
		versionUpdated = true
	}

	currentBindingVersion := resourceutil.GetStatus(rr, resourceBindingVersionStatus)
	if currentBindingVersion != bindingVersion {
		resourceutil.SetStatus(rr, logger, resourceBindingVersionStatus, bindingVersion)
		r.EventRecorder.Eventf(rr, v1.EventTypeNormal, "ResourceBindingVersionUpdated",
			"Resource binding version updated to %s", bindingVersion)
		versionUpdated = true
	}

	return versionUpdated
}

func (r *DynamicResourceRequestController) setPausedReconciliationStatusConditions(ctx context.Context, rr *unstructured.Unstructured, eventMsg string) error {
	reconciled := resourceutil.GetCondition(rr, resourceutil.ReconciledCondition)
	if reconciled == nil || reconciled.Status != "Unknown" || reconciled.Message != "Paused" {
		resourceutil.MarkReconciledPaused(rr)
		r.EventRecorder.Event(rr, v1.EventTypeWarning, pausedReconciliationReason, eventMsg)
		return r.Client.Status().Update(ctx, rr)
	}
	return nil
}

func (r *DynamicResourceRequestController) setWorkflowSuspendedStatusCondition(ctx context.Context, rr *unstructured.Unstructured) error {
	reconciled := resourceutil.GetCondition(rr, resourceutil.ReconciledCondition)
	if reconciled == nil ||
		reconciled.Status != v1.ConditionUnknown ||
		reconciled.Reason != workflowSuspendedReason ||
		reconciled.Message != "Suspended" {
		resourceutil.MarkReconciledSuspended(rr)
		return r.Client.Status().Update(ctx, rr)
	}
	return nil
}

func (r *DynamicResourceRequestController) getWorksStatus(ctx context.Context, logger logr.Logger, rr *unstructured.Unstructured, workLabels map[string]string) ([]string, []string, []string, []string, error) {
	workSelectorLabel := labels.FormatLabels(workLabels)
	selector, err := labels.Parse(workSelectorLabel)
	if err != nil {
		logging.Debug(logger, "failed parsing works selector label", "labels", workSelectorLabel)
		return nil, nil, nil, nil, err
	}

	var works v1alpha1.WorkList
	err = r.Client.List(ctx, &works, &client.ListOptions{
		Namespace:     rr.GetNamespace(),
		LabelSelector: selector,
	})

	if err != nil {
		logging.Error(logger, err, "failed listing works", "namespace", rr.GetNamespace(), "labelSelector", workSelectorLabel)
		return nil, nil, nil, nil, err
	}

	var failed, misplaced, ready, pending []string
	for _, work := range works.Items {
		readyCond := apiMeta.FindStatusCondition(work.Status.Conditions, "Ready")
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

func (r *DynamicResourceRequestController) generateWorkflowsCounterStatus(logger logr.Logger, rr *unstructured.Unstructured, numOfPipelines int64) bool {
	completedCond := resourceutil.GetCondition(rr, resourceutil.ConfigureWorkflowCompletedCondition)

	desiredWorkflows := numOfPipelines
	var desiredWorkflowsSucceeded int64

	if completedCond != nil && completedCond.Status == v1.ConditionTrue {
		desiredWorkflowsSucceeded = numOfPipelines
	}

	if resourceutil.GetWorkflowsCounterStatus(rr, "workflows") != desiredWorkflows ||
		resourceutil.GetWorkflowsCounterStatus(rr, "workflowsSucceeded") != desiredWorkflowsSucceeded {

		resourceutil.SetStatus(rr, logger,
			"workflows", desiredWorkflows,
			"workflowsSucceeded", desiredWorkflowsSucceeded,
			"workflowsFailed", int64(0),
		)

		return true
	}
	return false
}

func (r *DynamicResourceRequestController) cleanupWorkflowCountersAndExecution(ctx context.Context, logger logr.Logger,
	rr *unstructured.Unstructured) error {
	if resourceutil.GetWorkflowsCounterStatus(rr, "workflows") != 0 ||
		resourceutil.GetWorkflowsCounterStatus(rr, "workflowsSucceeded") != 0 ||
		resourceutil.GetWorkflowsCounterStatus(rr, "workflowsFailed") != 0 {

		resourceutil.SetStatus(rr, logger, "workflows", int64(0), "workflowsSucceeded", int64(0), "workflowsFailed", int64(0))
		unstructured.RemoveNestedField(rr.Object, "status", "kratix", "workflows", "pipelines")
		return r.Client.Status().Update(ctx, rr)
	}
	return nil
}

func ensureRRKratixWorkflowStatusIsSetup(rr *unstructured.Unstructured, pipelines []v1alpha1.PipelineJobResources) (bool, error) {
	existingPipelines, found, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "pipelines")
	if err != nil {
		return false, err
	}

	if !found || len(existingPipelines) != len(pipelines) {
		return true, resourceutil.ResetPipelineStatusToPending(rr, pipelines)
	}

	for i, pipeline := range pipelines {
		pipelineStatus, ok := existingPipelines[i].(map[string]any)
		if !ok || pipelineStatus["name"] != pipeline.Name {
			return true, resourceutil.ResetPipelineStatusToPending(rr, pipelines)
		}
	}

	return false, nil
}

func (r *DynamicResourceRequestController) deleteResources(o opts, promise *v1alpha1.Promise, resourceRequest *unstructured.Unstructured) (ctrl.Result, error) {
	if resourceutil.FinalizersAreDeleted(resourceRequest, r.getRRFinalizers()) {
		return ctrl.Result{}, nil
	}

	namespace := resourceRequest.GetNamespace()
	if promise.WorkflowPipelineNamespaceSet() {
		namespace = promise.Spec.Workflows.Config.PipelineNamespace
	}

	if controllerutil.ContainsFinalizer(resourceRequest, runDeleteWorkflowsFinalizer) {
		pipelineResources, err := promise.GenerateResourcePipelines(v1alpha1.WorkflowActionDelete, resourceRequest, o.logger)
		if err != nil {
			return ctrl.Result{}, err
		}

		jobOpts := workflow.NewOpts(o.ctx, o.client, r.EventRecorder, o.logger, resourceRequest, pipelineResources, "resource", r.NumberOfJobsToKeep, namespace)
		requeue, err := reconcileDelete(jobOpts)
		if err != nil {
			if errors.Is(err, workflow.ErrDeletePipelineFailed) {
				r.EventRecorder.Event(resourceRequest, "Warning", "Failed Pipeline", "The Delete Pipeline has failed")
				resourceutil.MarkDeleteWorkflowAsFailed(o.logger, resourceRequest)
				if err := r.Client.Status().Update(o.ctx, resourceRequest); err != nil {
					logging.Error(o.logger, err, "failed to update resource request status", "promise", promise.GetName(),
						"namespace", resourceRequest.GetNamespace(), "resource", resourceRequest.GetName())
				}
			}
			return ctrl.Result{}, err
		}
		if requeue {
			return defaultRequeue, nil
		}

		controllerutil.RemoveFinalizer(resourceRequest, runDeleteWorkflowsFinalizer)
		if err = o.client.Update(o.ctx, resourceRequest); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.ContainsFinalizer(resourceRequest, workFinalizer) {
		err := r.deleteWork(o, resourceRequest, workFinalizer, namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(resourceRequest, removeAllWorkflowJobsFinalizer) {
		err := r.deleteWorkflows(o, resourceRequest, removeAllWorkflowJobsFinalizer)
		if err != nil {
			return ctrl.Result{}, err
		}
		return fastRequeue, nil
	}

	if r.PromiseUpgrade {
		if controllerutil.ContainsFinalizer(resourceRequest, resourceBindingFinalizer) {
			err := r.deleteResourceBinding(o, resourceRequest, promise, resourceBindingFinalizer)
			if err != nil {
				return ctrl.Result{}, err
			}
			return fastRequeue, nil
		}
	}

	return fastRequeue, nil
}

func (r *DynamicResourceRequestController) deleteResourceBinding(o opts, rr *unstructured.Unstructured, promise *v1alpha1.Promise, finalizer string) error {
	resourceBinding := &v1alpha1.ResourceBinding{}

	namespacedName := types.NamespacedName{
		Name:      objectutil.GenerateDeterministicObjectName(fmt.Sprintf("%s-%s", rr.GetName(), promise.GetName())),
		Namespace: rr.GetNamespace(),
	}

	err := r.Client.Get(o.ctx, namespacedName, resourceBinding)
	if err != nil {
		if apierrors.IsNotFound(err) {
			controllerutil.RemoveFinalizer(rr, finalizer)
			if err := r.Client.Update(o.ctx, rr); err != nil {
				logging.Error(o.logger, err, "failed updating resource request while removing finalizer")
				return err
			}
			return nil
		}
	}

	err = r.Client.Delete(o.ctx, resourceBinding)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			logging.Warn(o.logger, "could not delete ResourceBinding; retrying", "reason", err.Error())
			return err
		}
	}

	return nil
}

func (r *DynamicResourceRequestController) deleteWork(o opts, resourceRequest *unstructured.Unstructured, finalizer, namespace string) error {
	works, err := resourceutil.GetAllWorksForResource(r.Client, namespace, r.PromiseIdentifier, resourceRequest.GetName())
	if err != nil {
		return err
	}

	if len(works) == 0 {
		// only remove finalizer at this point because deletion success is guaranteed
		controllerutil.RemoveFinalizer(resourceRequest, finalizer)
		if err := r.Client.Update(o.ctx, resourceRequest); err != nil {
			return err
		}
		return nil
	}

	parentAnnotations := resourceRequest.GetAnnotations()
	for i := range works {
		work := &works[i]
		if err := ensureTraceAnnotations(o.ctx, r.Client, work, parentAnnotations); err != nil {
			return err
		}
		if err := r.Client.Delete(o.ctx, work); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			logging.Warn(o.logger, "could not delete work; retrying", "reason", err.Error())
			return err
		}
	}

	return nil
}

func (r *DynamicResourceRequestController) deleteWorkflows(o opts, resourceRequest *unstructured.Unstructured, finalizer string) error {
	jobGVK := schema.GroupVersionKind{
		Group:   batchv1.SchemeGroupVersion.Group,
		Version: batchv1.SchemeGroupVersion.Version,
		Kind:    "Job",
	}

	jobLabels := map[string]string{
		v1alpha1.PromiseNameLabel:  r.PromiseIdentifier,
		v1alpha1.ResourceNameLabel: resourceRequest.GetName(),
		v1alpha1.WorkflowTypeLabel: string(v1alpha1.WorkflowTypeResource),
	}

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, &jobGVK, jobLabels)
	if err != nil {
		return err
	}

	// TODO: this part will be deprecated when we stop using the legacy labels
	jobLegacyLabels := map[string]string{
		v1alpha1.PromiseNameLabel:  r.PromiseIdentifier,
		v1alpha1.ResourceNameLabel: resourceRequest.GetName(),
		v1alpha1.WorkTypeLabel:     v1alpha1.WorkTypeResource,
	}
	legacyResourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, &jobGVK, jobLegacyLabels)
	if err != nil {
		return err
	}

	if !resourcesRemaining || !legacyResourcesRemaining {
		controllerutil.RemoveFinalizer(resourceRequest, finalizer)
		if err := r.Client.Update(o.ctx, resourceRequest); err != nil {
			return err
		}
	}

	return nil
}

func workflowsCompletedSuccessfully(workflowCompletedCondition *clusterv1.Condition) bool {
	return workflowCompletedCondition != nil &&
		workflowCompletedCondition.Status == v1.ConditionTrue &&
		workflowCompletedCondition.Reason == resourceutil.PipelinesExecutedSuccessfully
}

func (r *DynamicResourceRequestController) nextReconciliation(logger logr.Logger) ctrl.Result {
	logging.Info(logger, "scheduling next reconciliation", "reconciliationInterval", r.ReconciliationInterval)
	return ctrl.Result{RequeueAfter: r.ReconciliationInterval}
}

func (r *DynamicResourceRequestController) restartOnReconciliationInterval(
	ctx context.Context,
	logger logr.Logger,
	rr *unstructured.Unstructured,
	completedCond *clusterv1.Condition,
	forcePipelineRun bool,
	isWorkflowSuspended bool,
) (bool, error) {
	if forcePipelineRun && !r.manualReconciliationLabelSet(rr) && !isWorkflowSuspended {
		logging.Debug(
			logger,
			"resource configure pipeline completed too long ago; forcing reconciliation",
			"lastTransitionTime",
			completedCond.LastTransitionTime.String(),
		)
		if err := r.updateManualReconciliationLabel(ctx, rr); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (r *DynamicResourceRequestController) manualReconciliationLabelSet(rr *unstructured.Unstructured) bool {
	resourceLabels := rr.GetLabels()
	return resourceLabels[resourceutil.ManualReconciliationLabel] == "true"
}

func (r *DynamicResourceRequestController) updateManualReconciliationLabel(ctx context.Context, rr *unstructured.Unstructured) error {
	resourceLabels := rr.GetLabels()
	resourceLabels[resourceutil.ManualReconciliationLabel] = "true"
	rr.SetLabels(resourceLabels)

	return r.Client.Update(ctx, rr)
}

func (r *DynamicResourceRequestController) ensurePromiseIsUnavailable(ctx context.Context,
	rr *unstructured.Unstructured,
	logger logr.Logger,
) (ctrl.Result, error) {
	if !resourceutil.IsPromiseMarkedAsUnavailable(rr) {
		logging.Trace(logger, "cannot create resources; setting PromiseAvailable to false in resource status")
		resourceutil.MarkPromiseConditionAsNotAvailable(rr, logger)

		return ctrl.Result{}, r.Client.Status().Update(ctx, rr)
	}

	return slowRequeue, nil
}

func (r *DynamicResourceRequestController) ensurePromiseIsAvailable(ctx context.Context,
	rr *unstructured.Unstructured,
	logger logr.Logger,
) error {
	resourceutil.MarkPromiseConditionAsAvailable(rr, logger)
	return r.Client.Status().Update(ctx, rr)
}

var errResourceBindingNotFound = fmt.Errorf("cannot find any ResourceBinding for Resource")

func (r *DynamicResourceRequestController) fetchResourceBinding(
	ctx context.Context,
	rr *unstructured.Unstructured,
	promise *v1alpha1.Promise) (*v1alpha1.ResourceBinding, error) {
	bindings := &v1alpha1.ResourceBindingList{}
	if err := r.Client.List(ctx, bindings, &client.ListOptions{
		Namespace:     rr.GetNamespace(),
		LabelSelector: labels.SelectorFromSet(resourceBindingLabels(rr, promise)),
	}); err != nil {
		return nil, err
	}

	if len(bindings.Items) > 1 {
		return nil, fmt.Errorf("found multiple ResourceBindings for Resource %s in namespace %s;"+
			"there should be one ResourceBinding per Resource", rr.GetName(), rr.GetNamespace())
	} else if len(bindings.Items) == 0 {
		return nil, errResourceBindingNotFound
	}

	return &bindings.Items[0], nil
}

func getPromiseRevisionToUse(ctx context.Context, rr *unstructured.Unstructured, baseLogger logr.Logger, r *DynamicResourceRequestController, promise *v1alpha1.Promise) (*v1alpha1.PromiseRevision, string, error) {
	var promiseRevisionToUse *v1alpha1.PromiseRevision

	statusPromiseVersion := resourceutil.GetStatus(rr, resourcePromiseVersionStatus)
	resourceBinding, err := r.fetchResourceBinding(ctx, rr, promise)
	if err != nil && !errors.Is(err, errResourceBindingNotFound) {
		baseLogger.Error(err, "failed to fetch ResourceBinding for ResourceRequest")
		return nil, "", err
	}

	bindingVersion := "latest"
	if resourceBinding != nil {
		logging.Debug(baseLogger, "fetched ResourceBinding for ResourceRequest", "resourceBinding", resourceBinding.Spec.Version)
		bindingVersion = resourceBinding.Spec.Version
	}
	promiseRevisionToUse, err = fetchRevision(ctx, r.Client, promise, resourceBinding, statusPromiseVersion)
	if err != nil {
		baseLogger.Error(err, "failed to fetch PromiseRevision for ResourceRequest")
		r.EventRecorder.Eventf(rr, v1.EventTypeWarning, promiseRevisionLookupFailedReason, err.Error())
		return nil, "", err
	}

	return promiseRevisionToUse, bindingVersion, nil
}

func resourceBindingLabels(rr *unstructured.Unstructured, promise *v1alpha1.Promise) map[string]string {
	l := promise.GenerateSharedLabels()
	l[v1alpha1.ResourceNameLabel] = rr.GetName()
	return l
}

func latestRevision(ctx context.Context, c client.Client, promise *v1alpha1.Promise) (*v1alpha1.PromiseRevision, error) {
	revisionList := &v1alpha1.PromiseRevisionList{}
	if err := c.List(ctx, revisionList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(promise.GenerateSharedLabels()),
	}); err != nil {
		return nil, err
	}

	for i := range revisionList.Items {
		revision := &revisionList.Items[i]
		if revision.Status.Latest {
			return revision, nil
		}
	}

	return nil, fmt.Errorf("cannot find any PromiseRevision for Promise %s with status.latest set to true",
		promise.GetName())
}

func fetchRevision(ctx context.Context, c client.Client, promise *v1alpha1.Promise,
	binding *v1alpha1.ResourceBinding, promiseVersionFromRRStatus string) (*v1alpha1.PromiseRevision, error) {

	desiredVersion := promiseVersionFromRRStatus

	if binding != nil {
		if binding.Spec.Version == "latest" {
			return latestRevision(ctx, c, promise)
		}
		desiredVersion = binding.Spec.Version
	}

	// this is a brand new request
	if desiredVersion == "" {
		return latestRevision(ctx, c, promise)
	}

	revisionList := &v1alpha1.PromiseRevisionList{}
	if err := c.List(ctx, revisionList, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(promise.GenerateSharedLabels()),
	}); err != nil {
		return nil, err
	}

	for i := range revisionList.Items {
		revision := &revisionList.Items[i]
		if revision.Spec.Version == desiredVersion {
			return revision, nil
		}
	}

	return nil, fmt.Errorf("cannot find a PromiseRevision for Promise %s with version %s",
		promise.GetName(), desiredVersion)
}

func updateObservedGeneration(
	rr *unstructured.Unstructured, opts opts, logger logr.Logger,
) error {
	resourceutil.SetStatus(rr, logger, "observedGeneration", rr.GetGeneration())
	return opts.client.Status().Update(opts.ctx, rr)
}

func shouldForcePipelineRun(completedCond *clusterv1.Condition, reconciliationInterval time.Duration) bool {
	return completedCond != nil &&
		completedCond.Status == v1.ConditionTrue &&
		time.Since(completedCond.LastTransitionTime.Time) > reconciliationInterval
}

func (r *DynamicResourceRequestController) setPromiseLabels(ctx context.Context, promiseName string, rr *unstructured.Unstructured, resourceLabels map[string]string, logger logr.Logger) error {
	resourceLabels[v1alpha1.PromiseNameLabel] = promiseName
	rr.SetLabels(resourceLabels)
	if err := r.Client.Update(ctx, rr); err != nil {
		logging.Error(logger, err, "failed updating resource request with promise label")
		return err
	}
	return nil
}

func (r *DynamicResourceRequestController) getRRFinalizers() []string {
	rrFinalizers := []string{workFinalizer, removeAllWorkflowJobsFinalizer, runDeleteWorkflowsFinalizer}
	if r.PromiseUpgrade {
		rrFinalizers = append(rrFinalizers, resourceBindingFinalizer)
	}
	return rrFinalizers
}

func getResourceLabels(rr *unstructured.Unstructured) map[string]string {
	labels := rr.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	return labels
}

func shouldUpdateLastSuccessfulConfigureWorkflowTime(
	workflowCompletedCondition *clusterv1.Condition,
	rr *unstructured.Unstructured,
) bool {
	lastTransitionTime := workflowCompletedCondition.LastTransitionTime.Format(time.RFC3339)
	lastSuccessfulLegacy := resourceutil.GetStatus(rr, "lastSuccessfulConfigureWorkflowTime")
	lastSuccessfulKratix := resourceutil.GetKratixWorkflowsStatus(rr, "lastSuccessfulConfigureWorkflowTime")

	return lastTransitionTime != lastSuccessfulLegacy || lastTransitionTime != lastSuccessfulKratix
}

func updateLastSuccessfulConfigureWorkflowTime(workflowCompletedCondition *clusterv1.Condition, rr *unstructured.Unstructured, opts opts) error {
	lastTransitionTime := workflowCompletedCondition.LastTransitionTime.Format(time.RFC3339)
	resourceutil.SetStatus(rr, opts.logger, "lastSuccessfulConfigureWorkflowTime", lastTransitionTime)
	if err := resourceutil.SetKratixWorkflowsStatus(rr, "lastSuccessfulConfigureWorkflowTime", lastTransitionTime); err != nil {
		return err
	}
	return opts.client.Status().Update(opts.ctx, rr)
}

func addDynamicResourceRequestSpanAttributes(traceCtx *reconcileTrace, promise *v1alpha1.Promise, rr *unstructured.Unstructured) {
	traceCtx.AddAttributes(
		attribute.String("kratix.promise.name", promise.GetName()),
		attribute.String("kratix.resource_request.name", rr.GetName()),
		attribute.String("kratix.resource_request.namespace", rr.GetNamespace()),
		attribute.String("kratix.action", traceCtx.Action()),
	)
}
