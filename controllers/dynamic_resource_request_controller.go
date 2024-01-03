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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/pipeline"
	"github.com/syntasso/kratix/lib/resourceutil"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	workFinalizer            = v1alpha1.KratixPrefix + "work-cleanup"
	workflowsFinalizer       = v1alpha1.KratixPrefix + "workflows-cleanup"
	deleteWorkflowsFinalizer = v1alpha1.KratixPrefix + "delete-workflows"
)

var rrFinalizers = []string{workFinalizer, workflowsFinalizer, deleteWorkflowsFinalizer}

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
	PromiseWorkflowSelectors    *v1alpha1.WorkloadGroupScheduling
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

	err := r.Client.Get(ctx, req.NamespacedName, rr)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed getting Promise CRD")
		return defaultRequeue, nil
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
	if resourceutil.FinalizersAreMissing(rr, []string{workFinalizer, workflowsFinalizer, deleteWorkflowsFinalizer}) {
		return addFinalizers(opts, rr, []string{workFinalizer, workflowsFinalizer, deleteWorkflowsFinalizer})
	}

	pipelineResources, err := pipeline.NewConfigureResource(
		rr,
		r.CRD.Spec.Names.Plural,
		r.ConfigurePipelines,
		resourceRequestIdentifier,
		r.PromiseIdentifier,
		r.PromiseDestinationSelectors,
		r.PromiseWorkflowSelectors,
		opts.logger,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	jobOpts := jobOpts{
		opts:              opts,
		obj:               rr,
		pipelineLabels:    pipeline.LabelsForConfigureResource(resourceRequestIdentifier, r.PromiseIdentifier),
		pipelineResources: pipelineResources,
	}
	requeue, err := ensurePipelineIsReconciled(jobOpts)
	if err != nil {
		return ctrl.Result{}, err
	}

	if requeue != nil {
		return *requeue, nil
	}

	return ctrl.Result{}, nil
}

func isManualReconciliation(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	val, exists := labels[resourceutil.ManualReconciliationLabel]
	return exists && val == "true"
}

func setPipelineCompletedConditionStatus(o opts, obj *unstructured.Unstructured) (bool, error) {
	switch resourceutil.GetPipelineCompletedConditionStatus(obj) {
	case corev1.ConditionTrue:
		fallthrough
	case corev1.ConditionUnknown:
		resourceutil.SetStatus(obj, o.logger, "message", "Pending")
		resourceutil.MarkPipelineAsRunning(o.logger, obj)
		err := o.client.Status().Update(o.ctx, obj)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (r *DynamicResourceRequestController) deleteResources(o opts, resourceRequest *unstructured.Unstructured, resourceRequestIdentifier string) (ctrl.Result, error) {
	if resourceutil.FinalizersAreDeleted(resourceRequest, rrFinalizers) {
		return ctrl.Result{}, nil
	}

	if controllerutil.ContainsFinalizer(resourceRequest, deleteWorkflowsFinalizer) {
		existingDeletePipeline, err := r.getDeletePipeline(o, resourceRequestIdentifier, resourceRequest.GetNamespace())
		if err != nil {
			return defaultRequeue, err
		}

		if existingDeletePipeline == nil {
			deletePipeline := pipeline.NewDeletePipeline(resourceRequest, r.DeletePipelines, resourceRequestIdentifier, r.PromiseIdentifier)
			o.logger.Info("Creating Delete Pipeline. The pipeline will now execute...")
			err = r.Client.Create(o.ctx, &deletePipeline)
			if err != nil {
				o.logger.Error(err, "Error creating delete pipeline")
				y, _ := yaml.Marshal(&deletePipeline)
				o.logger.Error(err, string(y))
				return ctrl.Result{}, err
			}
			return defaultRequeue, nil
		}

		o.logger.Info("Checking status of Delete Pipeline")
		if existingDeletePipeline.Status.Succeeded > 0 {
			o.logger.Info("Delete Pipeline Completed")
			controllerutil.RemoveFinalizer(resourceRequest, deleteWorkflowsFinalizer)
			if err := r.Client.Update(o.ctx, resourceRequest); err != nil {
				return ctrl.Result{}, err
			}
		}

		o.logger.Info("Delete Pipeline not finished", "status", existingDeletePipeline.Status)

		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(resourceRequest, workFinalizer) {
		err := r.deleteWork(o, resourceRequest, resourceRequestIdentifier, workFinalizer)
		if err != nil {
			return defaultRequeue, err
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(resourceRequest, workflowsFinalizer) {
		err := r.deleteWorkflows(o, resourceRequest, resourceRequestIdentifier, workflowsFinalizer)
		if err != nil {
			return defaultRequeue, err
		}
		return fastRequeue, nil
	}

	return fastRequeue, nil
}

func (r *DynamicResourceRequestController) getDeletePipeline(o opts, resourceRequestIdentifier, namespace string) (*batchv1.Job, error) {
	jobs, err := getJobsWithLabels(o, pipeline.LabelsForDeleteResource(resourceRequestIdentifier, r.PromiseIdentifier), namespace)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return &jobs[0], nil
}

func (r *DynamicResourceRequestController) deleteWork(o opts, resourceRequest *unstructured.Unstructured, workName string, finalizer string) error {
	work := &v1alpha1.Work{}
	err := r.Client.Get(o.ctx, types.NamespacedName{
		Namespace: resourceRequest.GetNamespace(),
		Name:      workName,
	}, work)

	if err != nil {
		if errors.IsNotFound(err) {
			// only remove finalizer at this point because deletion success is guaranteed
			controllerutil.RemoveFinalizer(resourceRequest, finalizer)
			if err := r.Client.Update(o.ctx, resourceRequest); err != nil {
				return err
			}
			return nil
		}

		o.logger.Error(err, "Error locating Work, will try again in 5 seconds", "workName", workName)
		return err
	}

	err = r.Client.Delete(o.ctx, work)
	if err != nil {
		if errors.IsNotFound(err) {
			// only remove finalizer at this point because deletion success is guaranteed
			controllerutil.RemoveFinalizer(resourceRequest, finalizer)
			if err := r.Client.Update(o.ctx, resourceRequest); err != nil {
				return err
			}
			return nil
		}

		o.logger.Error(err, "Error deleting Work %s, will try again in 5 seconds", "workName", workName)
		return err
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
