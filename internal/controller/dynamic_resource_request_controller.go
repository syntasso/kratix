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

	"k8s.io/client-go/tools/record"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
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
)

const workFinalizer = v1alpha1.KratixPrefix + "work-cleanup"

var rrFinalizers = []string{workFinalizer, removeAllWorkflowJobsFinalizer, runDeleteWorkflowsFinalizer}

type DynamicResourceRequestController struct {
	//use same naming conventions as other controllers
	Client                      client.Client
	GVK                         *schema.GroupVersionKind
	Scheme                      *runtime.Scheme
	PromiseIdentifier           string
	Log                         logr.Logger
	UID                         string
	Enabled                     *bool
	CRD                         *apiextensionsv1.CustomResourceDefinition
	PromiseDestinationSelectors []v1alpha1.PromiseScheduling
	CanCreateResources          *bool
	NumberOfJobsToKeep          int
	EventRecorder               record.EventRecorder
}

//+kubebuilder:rbac:groups="batch",resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile reconciles a Dynamically Generated Resource object.
func (r *DynamicResourceRequestController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !*r.Enabled {
		// temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
		// once resolved, this won't be necessary since the dynamic controller will be deleted
		return ctrl.Result{}, nil
	}

	resourceRequestIdentifier := fmt.Sprintf("%s-%s", r.PromiseIdentifier, req.Name)
	logger := r.Log.WithValues(
		"uid", r.UID,
		"promiseID", r.PromiseIdentifier,
		"namespace", req.NamespacedName,
		"resourceRequest", resourceRequestIdentifier,
	)

	rr := &unstructured.Unstructured{}
	rr.SetGroupVersionKind(*r.GVK)

	promise := &v1alpha1.Promise{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: r.PromiseIdentifier}, promise); err != nil {
		logger.Error(err, "Failed getting Promise")
		return ctrl.Result{}, err
	}

	if err := r.Client.Get(ctx, req.NamespacedName, rr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed getting Promise CRD")
		return defaultRequeue, nil
	}

	resourceLabels := getResourceLabels(rr)
	if resourceLabels[v1alpha1.PromiseNameLabel] != r.PromiseIdentifier {
		return r.setPromiseLabels(ctx, promise.GetName(), rr, resourceLabels, logger)
	}

	opts := opts{client: r.Client, ctx: ctx, logger: logger}

	if !rr.GetDeletionTimestamp().IsZero() {
		return r.deleteResources(opts, promise, rr, resourceRequestIdentifier)
	}

	if !*r.CanCreateResources {
		return r.ensurePromiseIsUnavailable(ctx, rr, logger)
	}

	if resourceutil.IsPromiseMarkedAsUnavailable(rr) {
		return r.ensurePromiseIsAvailable(ctx, rr, logger)
	}

	if resourceutil.FinalizersAreMissing(
		rr, []string{workFinalizer, removeAllWorkflowJobsFinalizer, runDeleteWorkflowsFinalizer},
	) {
		return addFinalizers(opts, rr, []string{workFinalizer, removeAllWorkflowJobsFinalizer, runDeleteWorkflowsFinalizer})
	}

	logger.Info("Resource contains configure workflow(s), reconciling workflows")
	completedCond := resourceutil.GetCondition(rr, resourceutil.ConfigureWorkflowCompletedCondition)
	if shouldForcePipelineRun(completedCond) && !r.manualReconciliationLabelSet(rr) {
		logger.Info(
			"Resource configure pipeline completed too long ago... forcing the reconciliation",
			"lastTransitionTime",
			completedCond.LastTransitionTime.Time.String(),
		)
		return r.updateManualReconciliationLabel(opts.ctx, rr)
	}

	pipelineResources, err := promise.GenerateResourcePipelines(v1alpha1.WorkflowActionConfigure, rr, logger)
	if err != nil {
		return ctrl.Result{}, err
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
	)

	abort, err := reconcileConfigure(jobOpts)
	if err != nil || abort {
		return ctrl.Result{}, err
	}

	if rr.GetGeneration() != resourceutil.GetObservedGeneration(rr) {
		return ctrl.Result{}, updateObservedGeneration(rr, opts, logger)
	}

	if !promise.HasPipeline(v1alpha1.WorkflowTypeResource, v1alpha1.WorkflowActionConfigure) {
		return r.nextReconciliation(logger)
	}

	workflowCompletedCondition := resourceutil.GetCondition(rr, resourceutil.ConfigureWorkflowCompletedCondition)
	if workflowsCompletedSuccessfully(workflowCompletedCondition) {
		if shouldUpdateLastSuccessfulConfigureWorkflowTime(workflowCompletedCondition, rr) {
			return updateLastSuccessfulConfigureWorkflowTime(workflowCompletedCondition, rr, opts, logger)
		}
		return r.nextReconciliation(logger)
	}

	return ctrl.Result{}, nil
}

func (r *DynamicResourceRequestController) deleteResources(o opts, promise *v1alpha1.Promise, resourceRequest *unstructured.Unstructured, resourceRequestIdentifier string) (ctrl.Result, error) {
	if resourceutil.FinalizersAreDeleted(resourceRequest, rrFinalizers) {
		return ctrl.Result{}, nil
	}

	if controllerutil.ContainsFinalizer(resourceRequest, runDeleteWorkflowsFinalizer) {
		pipelineResources, err := promise.GenerateResourcePipelines(v1alpha1.WorkflowActionDelete, resourceRequest, o.logger)
		if err != nil {
			return ctrl.Result{}, err
		}

		jobOpts := workflow.NewOpts(o.ctx, o.client, r.EventRecorder, o.logger, resourceRequest, pipelineResources, "resource", r.NumberOfJobsToKeep)
		requeue, err := reconcileDelete(jobOpts)
		if err != nil {
			if errors.Is(err, workflow.ErrDeletePipelineFailed) {
				r.EventRecorder.Event(resourceRequest, "Warning", "Failed Pipeline", "The Delete Pipeline has failed")
				resourceutil.MarkDeleteWorkflowAsFailed(o.logger, resourceRequest)
				if err := r.Client.Status().Update(o.ctx, resourceRequest); err != nil {
					o.logger.Error(err, "Failed to update resource request status", "promise", promise.GetName(),
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
		err := r.deleteWork(o, resourceRequest, resourceRequestIdentifier, workFinalizer)
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

	return fastRequeue, nil
}

func (r *DynamicResourceRequestController) deleteWork(o opts, resourceRequest *unstructured.Unstructured, workName string, finalizer string) error {
	works, err := resourceutil.GetAllWorksForResource(r.Client, resourceRequest.GetNamespace(), r.PromiseIdentifier, resourceRequest.GetName())
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

	for _, work := range works {
		err = r.Client.Delete(o.ctx, &work)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			o.logger.Error(err, "Error deleting Work %s, will try again", "workName", workName)
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

func (r *DynamicResourceRequestController) nextReconciliation(logger logr.Logger) (ctrl.Result, error) {
	logger.Info("Scheduling next reconciliation", "DefaultReconciliationInterval", DefaultReconciliationInterval)
	return ctrl.Result{RequeueAfter: DefaultReconciliationInterval}, nil
}

func (r *DynamicResourceRequestController) manualReconciliationLabelSet(rr *unstructured.Unstructured) bool {
	resourceLabels := rr.GetLabels()
	return resourceLabels[resourceutil.ManualReconciliationLabel] == "true"
}

func (r *DynamicResourceRequestController) updateManualReconciliationLabel(ctx context.Context, rr *unstructured.Unstructured) (ctrl.Result, error) {
	resourceLabels := rr.GetLabels()
	resourceLabels[resourceutil.ManualReconciliationLabel] = "true"
	rr.SetLabels(resourceLabels)

	return ctrl.Result{}, r.Client.Update(ctx, rr)
}

func (r *DynamicResourceRequestController) ensurePromiseIsUnavailable(ctx context.Context,
	rr *unstructured.Unstructured,
	logger logr.Logger,
) (ctrl.Result, error) {
	if !resourceutil.IsPromiseMarkedAsUnavailable(rr) {
		logger.Info("Cannot create resources; setting PromiseAvailable to false in resource status")
		resourceutil.MarkPromiseConditionAsNotAvailable(rr, logger)

		return ctrl.Result{}, r.Client.Status().Update(ctx, rr)
	}

	return slowRequeue, nil
}

func (r *DynamicResourceRequestController) ensurePromiseIsAvailable(ctx context.Context,
	rr *unstructured.Unstructured,
	logger logr.Logger,
) (ctrl.Result, error) {
	resourceutil.MarkPromiseConditionAsAvailable(rr, logger)
	return ctrl.Result{}, r.Client.Status().Update(ctx, rr)
}

func updateObservedGeneration(
	rr *unstructured.Unstructured, opts opts, logger logr.Logger,
) error {
	resourceutil.SetStatus(rr, logger, "observedGeneration", rr.GetGeneration())
	return opts.client.Status().Update(opts.ctx, rr)
}

func shouldForcePipelineRun(completedCond *clusterv1.Condition) bool {
	return completedCond != nil &&
		completedCond.Status == v1.ConditionTrue &&
		time.Since(completedCond.LastTransitionTime.Time) > DefaultReconciliationInterval
}

func (r *DynamicResourceRequestController) setPromiseLabels(ctx context.Context, promiseName string, rr *unstructured.Unstructured, resourceLabels map[string]string, logger logr.Logger) (ctrl.Result, error) {
	resourceLabels[v1alpha1.PromiseNameLabel] = promiseName
	rr.SetLabels(resourceLabels)
	if err := r.Client.Update(ctx, rr); err != nil {
		logger.Error(err, "Failed updating resource request with Promise label")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
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
	lastSuccessfulTime := resourceutil.GetStatus(rr, "lastSuccessfulConfigureWorkflowTime")
	return lastTransitionTime != lastSuccessfulTime
}

func updateLastSuccessfulConfigureWorkflowTime(workflowCompletedCondition *clusterv1.Condition, rr *unstructured.Unstructured, opts opts, logger logr.Logger) (ctrl.Result, error) {
	resourceutil.SetStatus(rr, logger, "lastSuccessfulConfigureWorkflowTime", workflowCompletedCondition.LastTransitionTime.Format(time.RFC3339))
	return ctrl.Result{}, opts.client.Status().Update(opts.ctx, rr)
}
