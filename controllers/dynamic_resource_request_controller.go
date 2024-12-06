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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/migrations"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"

	"time"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
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
}

//+kubebuilder:rbac:groups="batch",resources=jobs,verbs=get;list;watch;create;update;patch;delete

func (r *DynamicResourceRequestController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !*r.Enabled {
		//temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
		//once resolved, this won't be necessary since the dynamic controller will be deleted
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
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed getting Promise CRD")
		return defaultRequeue, nil
	}

	requeue, err := migrations.RemoveDeprecatedConditions(ctx, r.Client, rr, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	if requeue != nil {
		return *requeue, nil
	}

	resourceLabels := rr.GetLabels()
	if resourceLabels == nil {
		resourceLabels = map[string]string{}
	}
	if resourceLabels[v1alpha1.PromiseNameLabel] != r.PromiseIdentifier {
		resourceLabels[v1alpha1.PromiseNameLabel] = promise.GetName()
		rr.SetLabels(resourceLabels)
		if err := r.Client.Update(ctx, rr); err != nil {
			logger.Error(err, "Failed updating resource request with Promise label")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	opts := opts{
		client: r.Client,
		ctx:    ctx,
		logger: logger,
	}

	if !rr.GetDeletionTimestamp().IsZero() {
		return r.deleteResources(opts, promise, rr, resourceRequestIdentifier)
	}

	if !*r.CanCreateResources {
		if !resourceutil.IsPromiseMarkedAsUnavailable(rr) {
			logger.Info("Cannot create resources; setting PromiseAvailable to false in resource status")
			resourceutil.MarkPromiseConditionAsNotAvailable(rr, logger)

			return ctrl.Result{}, r.Client.Status().Update(ctx, rr)
		}

		return slowRequeue, nil
	}

	if resourceutil.IsPromiseMarkedAsUnavailable(rr) {
		resourceutil.MarkPromiseConditionAsAvailable(rr, logger)
		return ctrl.Result{}, r.Client.Status().Update(ctx, rr)
	}

	// Reconcile necessary finalizers
	if resourceutil.FinalizersAreMissing(rr, []string{workFinalizer, removeAllWorkflowJobsFinalizer, runDeleteWorkflowsFinalizer}) {
		return addFinalizers(opts, rr, []string{workFinalizer, removeAllWorkflowJobsFinalizer, runDeleteWorkflowsFinalizer})
	}

	pipelineResources, err := promise.GenerateResourcePipelines(v1alpha1.WorkflowActionConfigure, rr, logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	jobOpts := workflow.NewOpts(ctx, r.Client, logger, rr, pipelineResources, "resource", r.NumberOfJobsToKeep)

	abort, err := reconcileConfigure(jobOpts)
	if err != nil || abort {
		return ctrl.Result{}, err
	}

	if rr.GetGeneration() != resourceutil.GetObservedGeneration(rr) {
		resourceutil.SetStatus(rr, logger, "observedGeneration", rr.GetGeneration())
		return ctrl.Result{}, opts.client.Status().Update(opts.ctx, rr)
	}

	workflowCompletedCondition := resourceutil.GetCondition(rr, resourceutil.ConfigureWorkflowCompletedCondition)
	if workflowCompletedCondition != nil && workflowCompletedCondition.Status == v1.ConditionTrue && workflowCompletedCondition.Reason == resourceutil.PipelinesExecutedSuccessfully {
		lastTransitionTime := workflowCompletedCondition.LastTransitionTime.Format(time.RFC3339)
		lastSuccessfulTime := resourceutil.GetStatus(rr, "lastSuccessfulConfigureWorkflowTime")

		if lastSuccessfulTime != lastTransitionTime {
			resourceutil.SetStatus(rr, logger, "lastSuccessfulConfigureWorkflowTime", lastTransitionTime)
			return ctrl.Result{}, opts.client.Status().Update(opts.ctx, rr)
		}
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

		jobOpts := workflow.NewOpts(o.ctx, o.client, o.logger, resourceRequest, pipelineResources, "resource", r.NumberOfJobsToKeep)
		requeue, err := reconcileDelete(jobOpts)
		if err != nil {
			return ctrl.Result{}, err
		}
		if requeue {
			return defaultRequeue, nil
		}

		controllerutil.RemoveFinalizer(resourceRequest, runDeleteWorkflowsFinalizer)
		if err := o.client.Update(o.ctx, resourceRequest); err != nil {
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
		err := r.deleteWorkflows(o, resourceRequest, resourceRequestIdentifier, removeAllWorkflowJobsFinalizer)
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
			if !errors.IsNotFound(err) {
				return err
			}
			o.logger.Error(err, "Error deleting Work %s, will try again", "workName", workName)
			return err
		}
	}

	return nil
}

func (r *DynamicResourceRequestController) deleteWorkflows(o opts, resourceRequest *unstructured.Unstructured, resourceRequestIdentifier, finalizer string) error {
	jobGVK := schema.GroupVersionKind{
		Group:   batchv1.SchemeGroupVersion.Group,
		Version: batchv1.SchemeGroupVersion.Version,
		Kind:    "Job",
	}

	jobLabels := map[string]string{
		v1alpha1.PromiseNameLabel:  r.PromiseIdentifier,
		v1alpha1.ResourceNameLabel: resourceRequest.GetName(),
		v1alpha1.WorkTypeLabel:     v1alpha1.WorkTypeResource,
	}

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, &jobGVK, jobLabels)
	if err != nil {
		return err
	}

	if !resourcesRemaining {
		controllerutil.RemoveFinalizer(resourceRequest, finalizer)
		if err := r.Client.Update(o.ctx, resourceRequest); err != nil {
			return err
		}
	}

	return nil
}
