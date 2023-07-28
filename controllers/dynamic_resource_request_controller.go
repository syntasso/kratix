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
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/pipeline"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	conditionsutil "sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	workFinalizer              = kratixPrefix + "work-cleanup"
	workflowsFinalizer         = kratixPrefix + "workflows-cleanup"
	deleteWorkflowsFinalizer   = kratixPrefix + "delete-workflows"
	PipelineCompletedCondition = clusterv1.ConditionType("PipelineCompleted")
)

var rrFinalizers = []string{workFinalizer, workflowsFinalizer, deleteWorkflowsFinalizer}

type dynamicResourceRequestController struct {
	//use same naming conventions as other controllers
	Client             client.Client
	gvk                *schema.GroupVersionKind
	scheme             *runtime.Scheme
	promiseIdentifier  string
	promiseScheduling  []v1alpha1.SchedulingConfig
	configurePipelines []v1alpha1.Pipeline
	deletePipelines    []v1alpha1.Pipeline
	log                logr.Logger
	finalizers         []string
	uid                string
	enabled            *bool
	crd                *apiextensionsv1.CustomResourceDefinition
}

//+kubebuilder:rbac:groups="",resources=pods,verbs=create;list;watch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create

func (r *dynamicResourceRequestController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if !*r.enabled {
		//temporary fix until https://github.com/kubernetes-sigs/controller-runtime/issues/1884 is resolved
		//once resolved, this won't be necessary since the dynamic controller will be deleted
		return ctrl.Result{}, nil
	}

	logger := r.log.WithValues("uid", r.uid, r.promiseIdentifier, req.NamespacedName)
	resourceRequestIdentifier := fmt.Sprintf("%s-%s-%s", r.promiseIdentifier, req.Namespace, req.Name)

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

	if !r.hasCondition(PipelineCompletedCondition, rr) {
		r.setStatus(rr, logger, "message", "Pending")
		r.setCondition(clusterv1.Condition{
			Type:               PipelineCompletedCondition,
			Status:             v1.ConditionFalse,
			Message:            "Pipeline has not completed",
			Reason:             "PipelineNotCompleted",
			LastTransitionTime: metav1.NewTime(time.Now()),
		}, rr, logger)
		return ctrl.Result{}, r.Client.Status().Update(ctx, rr)
	}

	//check if the pipeline has already been created. If it has exit out. All the lines
	//below this call are for the one-time creation of the pipeline.
	created, err := r.configurePipelinePodHasBeenCreated(resourceRequestIdentifier, rr.GetNamespace(), logger)
	if err != nil {
		return ctrl.Result{}, err
	}

	if created {
		logger.Info("Cannot execute update on pre-existing pipeline for Promise resource request " + resourceRequestIdentifier)
		return ctrl.Result{}, nil
	}

	resources, err := pipeline.NewConfigurePipeline(
		rr,
		r.crd.Spec.Names,
		r.configurePipelines,
		resourceRequestIdentifier,
		r.promiseIdentifier,
		r.promiseScheduling,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Creating Pipeline resources", "resourceRequest", resourceRequestIdentifier)
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
		existingDeletePipelinePod, err := r.getDeletePipelinePod(ctx, resourceRequestIdentifier, resourceRequest.GetNamespace(), logger)
		if err != nil {
			return defaultRequeue, err
		}

		if existingDeletePipelinePod == nil {
			deletePipelinePod := pipeline.NewDeletePipeline(resourceRequest, r.deletePipelines, resourceRequestIdentifier, r.promiseIdentifier)
			logger.Info("Creating Delete Pipeline for Promise resource request: " + resourceRequestIdentifier + ". The pipeline will now execute...")
			err = r.Client.Create(ctx, &deletePipelinePod)
			if err != nil {
				logger.Error(err, "Error creating Pod")
				y, _ := yaml.Marshal(&deletePipelinePod)
				logger.Error(err, string(y))
				return ctrl.Result{}, err
			}
			return defaultRequeue, nil
		}

		logger.Info("Checking status of Delete Pipeline for Promise resource request: " + resourceRequestIdentifier)
		if pipelineIsComplete(*existingDeletePipelinePod) {
			logger.Info("Delete Pipeline Completed for Promise resource request: " + resourceRequestIdentifier)
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
		err := r.deletePipeline(ctx, resourceRequest, resourceRequestIdentifier, workflowsFinalizer, logger)
		if err != nil {
			return defaultRequeue, err
		}
		return fastRequeue, nil
	}

	return fastRequeue, nil
}

func (r *dynamicResourceRequestController) getDeletePipelinePod(ctx context.Context, resourceRequestIdentifier, namespace string, logger logr.Logger) (*v1.Pod, error) {
	pods, err := r.getPodsWithLabels(
		pipeline.DeletePipelineLabels(resourceRequestIdentifier, r.promiseIdentifier),
		namespace,
		logger,
	)
	if err != nil || len(pods) == 0 {
		return nil, err
	}
	return &pods[0], nil
}

func (r *dynamicResourceRequestController) configurePipelinePodHasBeenCreated(resourceRequestIdentifier, namespace string, logger logr.Logger) (bool, error) {
	pods, err := r.getPodsWithLabels(
		pipeline.ConfigurePipelineLabels(resourceRequestIdentifier, r.promiseIdentifier),
		namespace,
		logger,
	)
	if err != nil {
		return false, err
	}
	return len(pods) > 0, nil
}

func (r *dynamicResourceRequestController) getPodsWithLabels(podLabels map[string]string, namespace string, logger logr.Logger) ([]v1.Pod, error) {
	selectorLabels := labels.FormatLabels(podLabels)
	selector, err := labels.Parse(selectorLabels)

	if err != nil {
		return nil, fmt.Errorf("error parsing labels %v: %w", podLabels, err)
	}

	listOps := &client.ListOptions{
		LabelSelector: selector,
		Namespace:     namespace,
	}

	pods := &v1.PodList{}
	err = r.Client.List(context.Background(), pods, listOps)
	if err != nil {
		logger.Error(err, "error listing pods", "selectors", selector.String())
		return nil, err
	}
	return pods.Items, nil
}

func pipelineIsComplete(pod v1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == "Initialized" {
			return condition.Status == "True" && condition.Reason == "PodCompleted" &&
				pod.Status.Phase == v1.PodSucceeded
		}
	}
	return false
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

func (r *dynamicResourceRequestController) deletePipeline(ctx context.Context, resourceRequest *unstructured.Unstructured, resourceRequestIdentifier, finalizer string, logger logr.Logger) error {
	podGVK := schema.GroupVersionKind{
		Group:   v1.SchemeGroupVersion.Group,
		Version: v1.SchemeGroupVersion.Version,
		Kind:    "Pod",
	}

	podLabels := pipeline.Labels(resourceRequestIdentifier, r.promiseIdentifier)

	resourcesRemaining, err := deleteAllResourcesWithKindMatchingLabel(ctx, r.Client, podGVK, podLabels, logger)
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

func (r *dynamicResourceRequestController) hasCondition(conditionType clusterv1.ConditionType, rr *unstructured.Unstructured) bool {
	getter := conditionsutil.UnstructuredGetter(rr)
	condition := conditionsutil.Get(getter, conditionType)
	return condition != nil
}

func (r *dynamicResourceRequestController) setCondition(condition clusterv1.Condition, rr *unstructured.Unstructured, logger logr.Logger) {
	setter := conditionsutil.UnstructuredSetter(rr)
	conditionsutil.Set(setter, &condition)
	logger.Info("set conditions", "condition", condition.Type, "value", condition.Status)
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
