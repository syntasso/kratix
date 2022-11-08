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
	"os"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/uuid"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

type dynamicResourceRequestController struct {
	client                 client.Client
	gvk                    *schema.GroupVersionKind
	scheme                 *runtime.Scheme
	promiseIdentifier      string
	promiseClusterSelector labels.Set
	xaasRequestPipeline    []string
	log                    logr.Logger
	finalizers             []string
}

//+kubebuilder:rbac:groups="",resources=pods,verbs=create;list;watch;delete
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create

//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=create;escalate;bind
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=create

type operation struct {
	err    error
	result ctrl.Result
}

func (o *operation) isFinished() bool {
	return o.result.IsZero()
}

func (o *operation) errored() bool {
	return o.err != nil
}

func (r *dynamicResourceRequestController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := r.log.WithValues(r.promiseIdentifier, req.NamespacedName)

	resourceRequestIdentifier := fmt.Sprintf("%s-%s-%s", r.promiseIdentifier, req.Namespace, req.Name)

	unstructuredCRD := &unstructured.Unstructured{}
	unstructuredCRD.SetGroupVersionKind(*r.gvk)

	err := r.client.Get(ctx, req.NamespacedName, unstructuredCRD)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed getting Promise CRD")
		return ctrl.Result{}, nil
	}

	workFinalizer := fmt.Sprintf("finalizers.%s.resource-request.kratix.io/work-cleanup", strings.ToLower(unstructuredCRD.GetKind()))
	pipelineFinalizer := fmt.Sprintf("finalizers.%s.resource-request.kratix.io/pipeline-cleanup", strings.ToLower(unstructuredCRD.GetKind()))

	if !unstructuredCRD.GetDeletionTimestamp().IsZero() {
		operation := r.deleteResources(ctx, unstructuredCRD, resourceRequestIdentifier, workFinalizer, pipelineFinalizer, logger)
		return operation.result, operation.err
	}

	// Reconcile necessary finalizers
	if !controllerutil.ContainsFinalizer(unstructuredCRD, workFinalizer) ||
		!controllerutil.ContainsFinalizer(unstructuredCRD, pipelineFinalizer) {
		logger.Info("Adding missing finalizers",
			"expectedFinalizers", []string{workFinalizer, pipelineFinalizer},
			"existingFinalizers", unstructuredCRD.GetFinalizers(),
		)
		//refactor shared func
		controllerutil.AddFinalizer(unstructuredCRD, workFinalizer)
		controllerutil.AddFinalizer(unstructuredCRD, pipelineFinalizer)
		if err := r.client.Update(ctx, unstructuredCRD); err != nil {
			logger.Error(err, "failed to add finalizers")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if r.pipelineHasExecuted(resourceRequestIdentifier) {
		logger.Info("Cannot execute update on pre-existing pipeline for Promise resource request " + resourceRequestIdentifier)
		return ctrl.Result{}, nil
	}

	workCreatorCommand := fmt.Sprintf("./work-creator -identifier %s -input-directory /work-creator-files", resourceRequestIdentifier)

	resourceRequestCommand := fmt.Sprintf("kubectl get %s.%s %s --namespace %s -oyaml > /output/object.yaml", strings.ToLower(r.gvk.Kind), r.gvk.Group, req.Name, req.Namespace)

	pod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "request-pipeline-" + r.promiseIdentifier + "-" + getShortUuid(),
			Namespace: "default",
			Labels: map[string]string{
				"kratix-promise-id":                  r.promiseIdentifier,
				"kratix-promise-resource-request-id": resourceRequestIdentifier,
			},
		},
		Spec: v1.PodSpec{
			RestartPolicy:      v1.RestartPolicyOnFailure,
			ServiceAccountName: r.promiseIdentifier + "-sa",
			Containers: []v1.Container{
				{
					Name: "writer",
					//Image:   "syntasso/kratix-platform-work-creator:dev",
					Image:   os.Getenv("WC_IMG"),
					Command: []string{"sh", "-c", workCreatorCommand},
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: "/work-creator-files/input",
							Name:      "output",
						},
						{
							MountPath: "/work-creator-files/metadata",
							Name:      "metadata",
						},
						{
							MountPath: "/work-creator-files/kratix-system",
							Name:      "promise-cluster-selectors",
						},
					},
				},
			},
			InitContainers: []v1.Container{
				{
					Name:    "reader",
					Image:   "bitnami/kubectl:1.20.10",
					Command: []string{"sh", "-c", resourceRequestCommand},
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: "/output",
							Name:      "input",
						},
					},
				},
				{
					Name:  "xaas-request-pipeline-stage-1",
					Image: r.xaasRequestPipeline[0],
					//Command: Supplied by the image author via ENTRYPOINT/CMD
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: "/input",
							Name:      "input",
						},
						{
							MountPath: "/output",
							Name:      "output",
						},
						{
							MountPath: "/metadata",
							Name:      "metadata",
						},
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "input",
					VolumeSource: v1.VolumeSource{
						EmptyDir: &v1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: "output",
					VolumeSource: v1.VolumeSource{
						EmptyDir: &v1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: "metadata",
					VolumeSource: v1.VolumeSource{
						EmptyDir: &v1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: "promise-cluster-selectors",
					VolumeSource: v1.VolumeSource{
						ConfigMap: &v1.ConfigMapVolumeSource{
							LocalObjectReference: v1.LocalObjectReference{
								Name: "cluster-selectors-" + r.promiseIdentifier,
							},
							Items: []v1.KeyToPath{
								{
									Key:  "selectors",
									Path: "promise-cluster-selectors",
								},
							},
						},
					},
				},
			},
		},
	}

	logger.Info("Creating Pipeline for Promise resource request: " + resourceRequestIdentifier + ". The pipeline will now execute...")
	err = r.client.Create(ctx, &pod)
	if err != nil {
		logger.Error(err, "Error creating Pod")
		y, _ := yaml.Marshal(&pod)
		logger.Error(err, string(y))
	}

	return ctrl.Result{}, nil
}

func (r *dynamicResourceRequestController) pipelineHasExecuted(resourceRequestIdentifier string) bool {
	isPromise, _ := labels.NewRequirement("kratix-promise-resource-request-id", selection.Equals, []string{resourceRequestIdentifier})
	selector := labels.NewSelector().
		Add(*isPromise)

	listOps := &client.ListOptions{
		Namespace:     "default",
		LabelSelector: selector,
	}

	ol := &v1.PodList{}
	err := r.client.List(context.Background(), ol, listOps)
	if err != nil {
		fmt.Println(err.Error())
		return false
	}
	return len(ol.Items) > 0
}

func (r *dynamicResourceRequestController) deleteResources(ctx context.Context, resourceRequest *unstructured.Unstructured, resourceRequestIdentifier string, workFinalizer string, pipelineFinalizer string, logger logr.Logger) operation {
	operation := r.deleteWork(ctx, resourceRequest, resourceRequestIdentifier, workFinalizer, logger)
	if operation.errored() || !operation.isFinished() {
		return operation
	}

	return r.deletePipeline(ctx, resourceRequest, resourceRequestIdentifier, pipelineFinalizer, logger)
}

func (r *dynamicResourceRequestController) deleteWork(ctx context.Context, resourceRequest *unstructured.Unstructured, workName string, finalizer string, logger logr.Logger) operation {
	if !controllerutil.ContainsFinalizer(resourceRequest, finalizer) {
		return operation{err: nil, result: ctrl.Result{}}
	}

	work := &v1alpha1.Work{}
	err := r.client.Get(ctx, types.NamespacedName{
		Namespace: "default",
		Name:      workName,
	}, work)
	if err != nil {
		if errors.IsNotFound(err) {
			// only remove finalizer at this point because deletion success is guaranteed
			controllerutil.RemoveFinalizer(resourceRequest, finalizer)
			if err := r.client.Update(ctx, resourceRequest); err != nil {
				return operation{err: err, result: ctrl.Result{RequeueAfter: 5 * time.Second}}
			}
			return operation{err: nil, result: ctrl.Result{}}
		}

		logger.Error(err, "Error locating Work, will try again in 5 seconds", "workName", workName)
		return operation{err: err, result: ctrl.Result{RequeueAfter: 5 * time.Second}}
	}

	err = r.client.Delete(ctx, work)
	if err != nil {
		if errors.IsNotFound(err) {
			// only remove finalizer at this point because deletion success is guaranteed
			controllerutil.RemoveFinalizer(resourceRequest, finalizer)
			if err := r.client.Update(ctx, resourceRequest); err != nil {
				return operation{err: err, result: ctrl.Result{RequeueAfter: 5 * time.Second}}
			}
			return operation{err: nil, result: ctrl.Result{}}
		}

		logger.Error(err, "Error deleting Work %s, will try again in 5 seconds", "workName", workName)
		return operation{err: err, result: ctrl.Result{RequeueAfter: 5 * time.Second}}
	}

	// requeue to ensure finalizer is removed
	return operation{err: nil, result: ctrl.Result{RequeueAfter: 5 * time.Second}}
}

func (r *dynamicResourceRequestController) deletePipeline(ctx context.Context, resourceRequest *unstructured.Unstructured, resourceRequestIdentifier, finalizer string, logger logr.Logger) operation {
	if !controllerutil.ContainsFinalizer(resourceRequest, finalizer) {
		return operation{err: nil, result: ctrl.Result{}}
	}
	pods := &v1.PodList{}
	podLabels := map[string]string{
		"kratix-promise-id":                  r.promiseIdentifier,
		"kratix-promise-resource-request-id": resourceRequestIdentifier,
	}
	listOptions := client.ListOptions{LabelSelector: labels.SelectorFromSet(podLabels)}

	err := r.client.List(ctx, pods, &listOptions)
	if err != nil {
		return operation{err: err, result: ctrl.Result{RequeueAfter: 5 * time.Second}}
	}

	if len(pods.Items) == 0 {
		controllerutil.RemoveFinalizer(resourceRequest, finalizer)
		if err := r.client.Update(ctx, resourceRequest); err != nil {
			return operation{err: err, result: ctrl.Result{RequeueAfter: 5 * time.Second}}
		}
		return operation{err: nil, result: ctrl.Result{}}
	}

	for _, pod := range pods.Items {
		err = r.client.Delete(ctx, &pod)
		if err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "Error deleting Pod, will try again in 5 seconds", "pod", pod.GetName())
			return operation{err: err, result: ctrl.Result{RequeueAfter: 5 * time.Second}}
		}
	}
	// requeue to ensure finalizer is removed
	return operation{err: nil, result: ctrl.Result{RequeueAfter: 5 * time.Second}}

}

func getShortUuid() string {
	envUuid, present := os.LookupEnv("TEST_PROMISE_CONTROLLER_POD_IDENTIFIER_UUID")
	if present {
		return envUuid
	} else {
		return string(uuid.NewUUID()[0:5])
	}
}
