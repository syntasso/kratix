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
	"k8s.io/apimachinery/pkg/labels"
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
	workFinalizer            = kratixPrefix + "work-cleanup"
	workflowsFinalizer       = kratixPrefix + "workflows-cleanup"
	deleteWorkflowsFinalizer = kratixPrefix + "delete-workflows"
)

var rrFinalizers = []string{workFinalizer, workflowsFinalizer, deleteWorkflowsFinalizer}

type dynamicResourceRequestController struct {
	//use same naming conventions as other controllers
	Client                      client.Client
	gvk                         *schema.GroupVersionKind
	scheme                      *runtime.Scheme
	promiseIdentifier           string
	promiseDestinationSelectors []v1alpha1.Selector
	configurePipelines          []v1alpha1.Pipeline
	deletePipelines             []v1alpha1.Pipeline
	log                         logr.Logger
	finalizers                  []string
	uid                         string
	enabled                     *bool
	crd                         *apiextensionsv1.CustomResourceDefinition
}

//+kubebuilder:rbac:groups="batch",resources=jobs,verbs=create;list;watch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create

func (r *dynamicResourceRequestController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !*r.enabled {
		//temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
		//once resolved, this won't be necessary since the dynamic controller will be deleted
		return ctrl.Result{}, nil
	}

	resourceRequestIdentifier := fmt.Sprintf("%s-%s-%s", r.promiseIdentifier, req.Namespace, req.Name)
	logger := r.log.WithValues(
		"uid", r.uid,
		"promiseID", r.promiseIdentifier,
		"namespace", req.NamespacedName,
		"resourceRequest", resourceRequestIdentifier,
	)

	rr := &unstructured.Unstructured{}
	rr.SetGroupVersionKind(*r.gvk)

	err := r.Client.Get(ctx, req.NamespacedName, rr)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed getting Promise CRD")
		return defaultRequeue, nil
	}

	if !rr.GetDeletionTimestamp().IsZero() {
		return r.deleteResources(ctx, rr, resourceRequestIdentifier, logger)
	}

	// Reconcile necessary finalizers
	if finalizersAreMissing(rr, []string{workFinalizer, workflowsFinalizer, deleteWorkflowsFinalizer}) {
		return addFinalizers(ctx, r.Client, rr, []string{workFinalizer, workflowsFinalizer, deleteWorkflowsFinalizer}, logger)
	}

	pipelineJobs, err := r.getPipelineJobs(resourceRequestIdentifier, rr.GetNamespace(), logger)
	if err != nil {
		logger.Info("Failed getting pipeline jobs", "error", err)
		return slowRequeue, nil
	}

	// No jobs indicates this is the first reconciliation loop of this resource request
	if len(pipelineJobs) == 0 {
		return r.createConfigurePipeline(ctx, rr, resourceRequestIdentifier, logger)
	}

	if resourceutil.IsThereAPipelineRunning(logger, pipelineJobs) {
		return slowRequeue, nil
	}

	found, err := resourceutil.PipelineForRequestExists(logger, rr, pipelineJobs)
	if err != nil {
		return slowRequeue, nil
	}

	if found {
		return ctrl.Result{}, nil
	}

	return r.createConfigurePipeline(ctx, rr, resourceRequestIdentifier, logger)

}

func (r *dynamicResourceRequestController) createConfigurePipeline(ctx context.Context, rr *unstructured.Unstructured, rrID string, logger logr.Logger) (ctrl.Result, error) {
	switch resourceutil.GetPipelineCompletedConditionStatus(rr) {
	case corev1.ConditionTrue:
		fallthrough
	case corev1.ConditionUnknown:
		r.setStatus(rr, logger, "message", "Pending")
		resourceutil.MarkPipelineAsRunning(logger, rr)
		return ctrl.Result{}, r.Client.Status().Update(ctx, rr)
	}

	logger.Info("Triggering pipeline")

	resources, err := pipeline.NewConfigurePipeline(
		rr,
		r.crd.Spec.Names,
		r.configurePipelines,
		rrID,
		r.promiseIdentifier,
		r.promiseDestinationSelectors,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Creating Pipeline resources")
	for _, resource := range resources {
		logger.Info("Creating resource", resource.GetObjectKind().GroupVersionKind().Kind, resource.GetName())
		if err = r.Client.Create(ctx, resource); err != nil {
			if errors.IsAlreadyExists(err) {
				logger.Info("Resource already exists, skipping", resource.GetObjectKind().GroupVersionKind().Kind, resource.GetName())
				continue
			}
			logger.Error(err, "Error creating resource", resource.GetObjectKind().GroupVersionKind().Kind, resource.GetName())
			y, _ := yaml.Marshal(&resource)
			logger.Error(err, string(y))
		}

	}

	return ctrl.Result{}, nil
}

func (r *dynamicResourceRequestController) deleteResources(ctx context.Context, resourceRequest *unstructured.Unstructured, resourceRequestIdentifier string, logger logr.Logger) (ctrl.Result, error) {
	if finalizersAreDeleted(resourceRequest, rrFinalizers) {
		return ctrl.Result{}, nil
	}

	if controllerutil.ContainsFinalizer(resourceRequest, deleteWorkflowsFinalizer) {
		existingDeletePipeline, err := r.getDeletePipeline(ctx, resourceRequestIdentifier, resourceRequest.GetNamespace(), logger)
		if err != nil {
			return defaultRequeue, err
		}

		if existingDeletePipeline == nil {
			deletePipeline := pipeline.NewDeletePipeline(resourceRequest, r.deletePipelines, resourceRequestIdentifier, r.promiseIdentifier)
			logger.Info("Creating Delete Pipeline. The pipeline will now execute...")
			err = r.Client.Create(ctx, &deletePipeline)
			if err != nil {
				logger.Error(err, "Error creating delete pipeline")
				y, _ := yaml.Marshal(&deletePipeline)
				logger.Error(err, string(y))
				return ctrl.Result{}, err
			}
			return defaultRequeue, nil
		}

		logger.Info("Checking status of Delete Pipeline")
		if existingDeletePipeline.Status.Succeeded > 0 {
			logger.Info("Delete Pipeline Completed")
			controllerutil.RemoveFinalizer(resourceRequest, deleteWorkflowsFinalizer)
			if err := r.Client.Update(ctx, resourceRequest); err != nil {
				return ctrl.Result{}, err
			}
		}

		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(resourceRequest, workFinalizer) {
		err := r.deleteWork(ctx, resourceRequest, resourceRequestIdentifier, workFinalizer, logger)
		if err != nil {
			return defaultRequeue, err
		}
		return fastRequeue, nil
	}

	if controllerutil.ContainsFinalizer(resourceRequest, workflowsFinalizer) {
		err := r.deleteWorkflows(ctx, resourceRequest, resourceRequestIdentifier, workflowsFinalizer, logger)
		if err != nil {
			return defaultRequeue, err
		}
		return fastRequeue, nil
	}

	return fastRequeue, nil
}

func (r *dynamicResourceRequestController) getDeletePipeline(ctx context.Context, resourceRequestIdentifier, namespace string, logger logr.Logger) (*batchv1.Job, error) {
	jobs, err := r.getJobsWithLabels(
		pipeline.DeletePipelineLabels(resourceRequestIdentifier, r.promiseIdentifier),
		namespace,
		logger,
	)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return &jobs[0], nil
}

func (r *dynamicResourceRequestController) getPipelineJobs(rrID, namespace string, logger logr.Logger) ([]batchv1.Job, error) {
	jobs, err := r.getJobsWithLabels(
		pipeline.ConfigurePipelineLabels(rrID, r.promiseIdentifier),
		namespace,
		logger,
	)
	if err != nil {
		return nil, err
	}
	return jobs, nil
}

func (r *dynamicResourceRequestController) getJobsWithLabels(jobLabels map[string]string, namespace string, logger logr.Logger) ([]batchv1.Job, error) {
	selectorLabels := labels.FormatLabels(jobLabels)
	selector, err := labels.Parse(selectorLabels)

	if err != nil {
		return nil, fmt.Errorf("error parsing labels %v: %w", jobLabels, err)
	}

	listOps := &client.ListOptions{
		LabelSelector: selector,
		Namespace:     namespace,
	}

	jobs := &batchv1.JobList{}
	err = r.Client.List(context.Background(), jobs, listOps)
	if err != nil {
		logger.Error(err, "error listing jobs", "selectors", selector.String())
		return nil, err
	}
	return jobs.Items, nil
}

func (r *dynamicResourceRequestController) deleteWork(ctx context.Context, resourceRequest *unstructured.Unstructured, workName string, finalizer string, logger logr.Logger) error {
	work := &v1alpha1.Work{}
	err := r.Client.Get(ctx, types.NamespacedName{
		Namespace: resourceRequest.GetNamespace(),
		Name:      workName,
	}, work)

	if err != nil {
		if errors.IsNotFound(err) {
			// only remove finalizer at this point because deletion success is guaranteed
			controllerutil.RemoveFinalizer(resourceRequest, finalizer)
			if err := r.Client.Update(ctx, resourceRequest); err != nil {
				return err
			}
			return nil
		}

		logger.Error(err, "Error locating Work, will try again in 5 seconds", "workName", workName)
		return err
	}

	err = r.Client.Delete(ctx, work)
	if err != nil {
		if errors.IsNotFound(err) {
			// only remove finalizer at this point because deletion success is guaranteed
			controllerutil.RemoveFinalizer(resourceRequest, finalizer)
			if err := r.Client.Update(ctx, resourceRequest); err != nil {
				return err
			}
			return nil
		}

		logger.Error(err, "Error deleting Work %s, will try again in 5 seconds", "workName", workName)
		return err
	}

	return nil
}

func (r *dynamicResourceRequestController) deleteWorkflows(ctx context.Context, resourceRequest *unstructured.Unstructured, resourceRequestIdentifier, finalizer string, logger logr.Logger) error {
	jobGVK := schema.GroupVersionKind{
		Group:   batchv1.SchemeGroupVersion.Group,
		Version: batchv1.SchemeGroupVersion.Version,
		Kind:    "Job",
	}

	jobLabels := pipeline.Labels(resourceRequestIdentifier, r.promiseIdentifier)

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(ctx, r.Client, jobGVK, jobLabels, logger)
	if err != nil {
		return err
	}

	if !resourcesRemaining {
		controllerutil.RemoveFinalizer(resourceRequest, finalizer)
		if err := r.Client.Update(ctx, resourceRequest); err != nil {
			return err
		}
	}

	return nil
}

func (r *dynamicResourceRequestController) setStatus(rr *unstructured.Unstructured, logger logr.Logger, statuses ...string) {
	if len(statuses) == 0 {
		return
	}

	if len(statuses)%2 != 0 {
		logger.Info("invalid status; expecting key:value pair", "status", statuses)
		return
	}

	nestedMap := map[string]interface{}{}
	for i := 0; i < len(statuses); i += 2 {
		key := statuses[i]
		value := statuses[i+1]
		nestedMap[key] = value
	}

	err := unstructured.SetNestedMap(rr.Object, nestedMap, "status")

	if err != nil {
		logger.Info("failed to set status; ignoring", "map", nestedMap)
	}
}
