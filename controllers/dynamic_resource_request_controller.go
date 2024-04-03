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
	"github.com/syntasso/kratix/lib/pipeline"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"

	batchv1 "k8s.io/api/batch/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	workFinalizer            = v1alpha1.KratixPrefix + "work-cleanup"
	workflowsFinalizer       = v1alpha1.KratixPrefix + "workflows-cleanup"
	deleteWorkflowsFinalizer = v1alpha1.KratixPrefix + "delete-workflows"
)

var rrFinalizers = []string{workFinalizer, removeAllWorkflowJobsFinalizer, runDeleteWorkflowsFinalizer}

type DynamicResourceRequestController struct {
	//use same naming conventions as other controllers
	Client                      client.Client
	GVK                         *schema.GroupVersionKind
	Scheme                      *runtime.Scheme
	PromiseIdentifier           string
	ConfigurePipelines          []v1alpha1.Pipeline
	DeletePipelines             []v1alpha1.Pipeline
	Log                         logr.Logger
	UID                         string
	Enabled                     *bool
	CRD                         *apiextensionsv1.CustomResourceDefinition
	PromiseDestinationSelectors []v1alpha1.PromiseScheduling
	CanCreateResources          *bool
}

//+kubebuilder:rbac:groups="batch",resources=jobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create

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
	unstructuredPromise, err := promise.ToUnstructured()
	if err != nil {
		logger.Error(err, "Failed converting Promise to Unstructured")
		return ctrl.Result{}, err
	}

	if err := r.Client.Get(ctx, req.NamespacedName, rr); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed getting Promise CRD")
		return defaultRequeue, nil
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
		return r.deleteResources(opts, rr, resourceRequestIdentifier)
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

	var pipelines []workflow.Pipeline
	for _, p := range r.ConfigurePipelines {
		pipelineResources, err := pipeline.NewConfigureResource(
			rr,
			unstructuredPromise,
			r.CRD.Spec.Names.Plural,
			p,
			resourceRequestIdentifier,
			r.PromiseIdentifier,
			r.PromiseDestinationSelectors,
			opts.logger,
		)
		if err != nil {
			return ctrl.Result{}, err
		}

		//TODO smelly, refactor. Should we merge the lib/pipeline package with lib/workflow?
		//TODO if we dont do that, backfil unit tests for dynamic and promise controllers to assert the job is correct
		pipelines = append(pipelines, workflow.Pipeline{
			Job:                  pipelineResources[4].(*batchv1.Job),
			JobRequiredResources: pipelineResources[0:4],
			Name:                 p.Name,
		})
	}

	jobOpts := workflow.NewOpts(ctx, r.Client, logger, rr, pipelines, "resource")

	finished, err := reconcileConfigure(jobOpts)
	if err == nil && finished {
		return ctrl.Result{}, nil
	}
	return defaultRequeue, err

}

func (r *DynamicResourceRequestController) deleteResources(o opts, resourceRequest *unstructured.Unstructured, resourceRequestIdentifier string) (ctrl.Result, error) {
	if resourceutil.FinalizersAreDeleted(resourceRequest, rrFinalizers) {
		return ctrl.Result{}, nil
	}

	if controllerutil.ContainsFinalizer(resourceRequest, runDeleteWorkflowsFinalizer) {
		var pipelines []workflow.Pipeline
		for _, p := range r.DeletePipelines {
			pipelineResources := pipeline.NewDeleteResource(
				resourceRequest, p, resourceRequestIdentifier, r.PromiseIdentifier, r.CRD.Spec.Names.Plural,
			)

			pipelines = append(pipelines, workflow.Pipeline{
				Job:                  pipelineResources[3].(*batchv1.Job),
				JobRequiredResources: pipelineResources[0:3],
				Name:                 p.Name,
			})
		}

		jobOpts := workflow.NewOpts(o.ctx, o.client, o.logger, resourceRequest, pipelines, "resource")
		finished, err := reconcileDelete(jobOpts, pipelines)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !finished {
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
			return defaultRequeue, err
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

	jobLabels := pipeline.LabelsForAllResourceWorkflows(resourceRequestIdentifier, r.PromiseIdentifier)

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(o, jobGVK, jobLabels)
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
