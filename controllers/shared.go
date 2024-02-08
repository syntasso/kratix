package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/writers"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"
)

const (
	promiseVersionLabel            = v1alpha1.KratixPrefix + "promise-version"
	promiseReleaseNameLabel        = v1alpha1.KratixPrefix + "promise-release-name"
	removeAllWorkflowJobsFinalizer = v1alpha1.KratixPrefix + "workflows-cleanup"
	runDeleteWorkflowsFinalizer    = v1alpha1.KratixPrefix + "delete-workflows"
)

type StateStore interface {
	client.Object
	GetSecretRef() *v1.SecretReference
}

type opts struct {
	ctx    context.Context
	client client.Client
	logger logr.Logger
}

type jobOpts struct {
	opts
	obj               *unstructured.Unstructured
	pipelineLabels    map[string]string
	pipelineResources []client.Object
	source            string
}

func ensureConfigurePipelineIsReconciled(j jobOpts) (*ctrl.Result, error) {
	namespace := j.obj.GetNamespace()
	if namespace == "" {
		namespace = v1alpha1.KratixSystemNamespace
	}

	pipelineJobs, err := getJobsWithLabels(j.opts, j.pipelineLabels, namespace)
	if err != nil {
		j.logger.Info("Failed getting Promise pipeline jobs", "error", err)
		return &slowRequeue, nil
	}

	// No jobs indicates this is the first reconciliation loop of this resource request
	if len(pipelineJobs) == 0 {
		j.logger.Info("No jobs found, creating workflow Job")
		return &fastRequeue, createConfigurePipeline(j)
	}

	existingPipelineJob, err := resourceutil.PipelineWithDesiredSpecExists(j.logger, j.obj, pipelineJobs)
	if err != nil {
		return &slowRequeue, nil
	}

	if resourceutil.IsThereAPipelineRunning(j.logger, pipelineJobs) {
		/* Suspend all pipelines if the promise was updated */
		for _, job := range resourceutil.SuspendablePipelines(j.logger, pipelineJobs) {
			//Don't suspend a the job that is the desired spec
			if existingPipelineJob != nil && job.GetName() != existingPipelineJob.GetName() {
				trueBool := true
				patch := client.MergeFrom(job.DeepCopy())
				job.Spec.Suspend = &trueBool
				j.logger.Info("Suspending inactive job", "job", job.GetName())
				err := j.client.Patch(j.ctx, &job, patch)
				if err != nil {
					j.logger.Error(err, "failed to patch Job", "job", job.GetName())
				}
			}
		}

		// Wait the pipeline to complete
		j.logger.Info("Job already inflight for workflow, waiting for it to be inactive")
		return &slowRequeue, nil
	}

	if isManualReconciliation(j.obj.GetLabels()) || existingPipelineJob == nil {
		j.logger.Info("Creating job for workflow", "manualTrigger", isManualReconciliation(j.obj.GetLabels()))
		return &fastRequeue, createConfigurePipeline(j)
	}

	j.logger.Info("Job already exists and is complete for workflow")
	if j.source == "promise" {
		return deleteConfigMap(j)
	}

	//delete 5 old jobs
	err = deleteAllButLastFiveJobs(j)
	if err != nil {
		j.logger.Error(err, "failed to delete old jobs")
	}

	return nil, nil
}

const numberOfJobsToKeep = 5

func deleteAllButLastFiveJobs(j jobOpts) error {
	namespace := j.obj.GetNamespace()
	if namespace == "" {
		namespace = v1alpha1.KratixSystemNamespace
	}

	pipelineJobs, err := getJobsWithLabels(j.opts, j.pipelineLabels, namespace)
	if err != nil {
		j.logger.Info("Failed getting Promise pipeline jobs", "error", err)
		return nil
	}

	if len(pipelineJobs) <= numberOfJobsToKeep {
		return nil
	}

	// Sort jobs by creation time
	pipelineJobs = resourceutil.SortJobsByCreationDateTime(pipelineJobs)

	// Delete all but the last 5 jobs
	for i := 0; i < len(pipelineJobs)-numberOfJobsToKeep; i++ {
		job := pipelineJobs[i]
		j.logger.Info("Deleting old job", "job", job.GetName())
		if err := j.client.Delete(j.ctx, &job); err != nil {
			if !errors.IsNotFound(err) {
				j.logger.Info("failed to delete job", "job", job.GetName(), "error", err)
				return nil
			}
		}
	}

	return nil
}

func deleteConfigMap(j jobOpts) (*ctrl.Result, error) {
	configMap := &v1.ConfigMap{}
	for _, resource := range j.pipelineResources {
		if _, ok := resource.(*v1.ConfigMap); ok {
			configMap = resource.(*v1.ConfigMap)
			break
		}
	}

	j.logger.Info("Removing configmap", "name", configMap.GetName())
	if err := j.client.Delete(j.ctx, configMap); err != nil {
		if !errors.IsNotFound(err) {
			j.logger.Info("failed to delete configmap", "name", configMap.GetName(), "error", err)
			return &fastRequeue, nil
		}
	}

	return nil, nil
}

func createConfigurePipeline(j jobOpts) error {
	updated, err := setPipelineCompletedConditionStatus(j.opts, j.obj)
	if err != nil {
		return err
	}

	if updated {
		return nil
	}

	j.logger.Info("Triggering Promise pipeline")

	applyResources(j.opts, j.pipelineResources...)

	if isManualReconciliation(j.obj.GetLabels()) {
		newLabels := j.obj.GetLabels()
		delete(newLabels, resourceutil.ManualReconciliationLabel)
		j.obj.SetLabels(newLabels)
		if err := j.client.Update(j.ctx, j.obj); err != nil {
			return err
		}
	}

	return nil
}

func ensureDeletePipelineIsReconciled(jobOpts jobOpts) (ctrl.Result, error) {
	jobOpts.logger.Info("labels", "labels", jobOpts.pipelineLabels)
	existingDeletePipeline, err := getDeletePipeline(jobOpts.opts, jobOpts.obj.GetNamespace(), jobOpts.pipelineLabels)
	if err != nil {
		return defaultRequeue, err
	}

	if existingDeletePipeline == nil {
		jobOpts.logger.Info("Creating Delete Pipeline. The pipeline will now execute...")

		//TODO retrieve error information from applyResources to return to the caller
		applyResources(jobOpts.opts, jobOpts.pipelineResources...)

		return defaultRequeue, nil
	}

	jobOpts.logger.Info("Checking status of Delete Pipeline")
	if existingDeletePipeline.Status.Succeeded > 0 {
		jobOpts.logger.Info("Delete Pipeline Completed")
		controllerutil.RemoveFinalizer(jobOpts.obj, runDeleteWorkflowsFinalizer)
		if err := jobOpts.client.Update(jobOpts.ctx, jobOpts.obj); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		jobOpts.logger.Info("Delete Pipeline not finished", "status", existingDeletePipeline.Status)
	}

	return fastRequeue, nil
}

func getDeletePipeline(o opts, namespace string, labels map[string]string) (*batchv1.Job, error) {
	jobs, err := getJobsWithLabels(o, labels, namespace)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return &jobs[0], nil
}

// pass in nil resourceLabels to delete all resources of the GVK
func deleteAllResourcesWithKindMatchingLabel(o opts, gvk schema.GroupVersionKind, resourceLabels map[string]string) (bool, error) {
	resourceList := &unstructured.UnstructuredList{}
	resourceList.SetGroupVersionKind(gvk)
	listOptions := client.ListOptions{LabelSelector: labels.SelectorFromSet(resourceLabels)}
	err := o.client.List(o.ctx, resourceList, &listOptions)
	if err != nil {
		return true, err
	}

	o.logger.Info("deleting resources", "gvk", resourceList.GroupVersionKind(), "withLabels", resourceLabels, "resources", resourceutil.GetResourceNames(resourceList.Items))

	for _, resource := range resourceList.Items {
		err = o.client.Delete(o.ctx, &resource, client.PropagationPolicy(metav1.DeletePropagationBackground))
		o.logger.Info("deleting resource", "res", resource.GetName(), "gvk", resource.GroupVersionKind())
		if err != nil && !errors.IsNotFound(err) {
			o.logger.Error(err, "Error deleting resource, will try again in 5 seconds", "name", resource.GetName(), "kind", resource.GetKind())
			return true, err
		}
		o.logger.Info("successfully triggered deletion of resource", "name", resource.GetName(), "kind", resource.GetKind())
	}

	return len(resourceList.Items) != 0, nil
}

// finalizers must be less than 64 characters
func addFinalizers(o opts, resource client.Object, finalizers []string) (ctrl.Result, error) {
	o.logger.Info("Adding missing finalizers",
		"expectedFinalizers", finalizers,
		"existingFinalizers", resource.GetFinalizers(),
	)
	for _, finalizer := range finalizers {
		controllerutil.AddFinalizer(resource, finalizer)
	}
	if err := o.client.Update(o.ctx, resource); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func fetchObjectAndSecret(o opts, stateStoreRef client.ObjectKey, stateStore StateStore) (*v1.Secret, error) {
	if err := o.client.Get(o.ctx, stateStoreRef, stateStore); err != nil {
		o.logger.Error(err, "unable to fetch resource", "resourceKind", stateStore.GetObjectKind(), "stateStoreRef", stateStoreRef)
		return nil, err
	}

	if stateStore.GetSecretRef() == nil {
		return nil, nil
	}

	namespace := stateStore.GetSecretRef().Namespace
	if namespace == "" {
		namespace = v1alpha1.KratixSystemNamespace
	}

	secret := &v1.Secret{}
	secretRef := types.NamespacedName{
		Name:      stateStore.GetSecretRef().Name,
		Namespace: namespace,
	}

	if err := o.client.Get(o.ctx, secretRef, secret); err != nil {
		o.logger.Error(err, "unable to fetch resource", "resourceKind", stateStore.GetObjectKind(), "secretRef", secretRef)
		return nil, err
	}

	return secret, nil
}

func newWriter(o opts, destination v1alpha1.Destination) (writers.StateStoreWriter, error) {
	stateStoreRef := client.ObjectKey{
		Name:      destination.Spec.StateStoreRef.Name,
		Namespace: destination.Namespace,
	}

	var writer writers.StateStoreWriter
	var err error
	switch destination.Spec.StateStoreRef.Kind {
	case "BucketStateStore":
		stateStore := &v1alpha1.BucketStateStore{}
		secret, fetchErr := fetchObjectAndSecret(o, stateStoreRef, stateStore)
		if fetchErr != nil {
			return nil, fetchErr
		}
		var data map[string][]byte = nil
		if secret != nil {
			data = secret.Data
		}

		writer, err = writers.NewS3Writer(o.logger.WithName("writers").WithName("BucketStateStoreWriter"), stateStore.Spec, destination, data)
	case "GitStateStore":
		stateStore := &v1alpha1.GitStateStore{}
		secret, fetchErr := fetchObjectAndSecret(o, stateStoreRef, stateStore)
		if fetchErr != nil {
			return nil, fetchErr
		}

		writer, err = writers.NewGitWriter(o.logger.WithName("writers").WithName("GitStateStoreWriter"), stateStore.Spec, destination, secret.Data)
	default:
		return nil, fmt.Errorf("unsupported kind %s", destination.Spec.StateStoreRef.Kind)
	}

	if err != nil {
		//TODO: should this be a retryable error?
		o.logger.Error(err, "unable to create StateStoreWriter")
		return nil, err
	}
	return writer, nil
}

func getJobsWithLabels(o opts, jobLabels map[string]string, namespace string) ([]batchv1.Job, error) {
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
	err = o.client.List(o.ctx, jobs, listOps)
	if err != nil {
		o.logger.Error(err, "error listing jobs", "selectors", selector.String())
		return nil, err
	}
	return jobs.Items, nil
}

// TODO return error info (summary of errors from resources?) to the caller, instead of just logging
func applyResources(o opts, resources ...client.Object) {
	o.logger.Info("Reconciling pipeline resources")

	for _, resource := range resources {
		logger := o.logger.WithValues("gvk", resource.GetObjectKind().GroupVersionKind(), "name", resource.GetName(), "namespace", resource.GetNamespace(), "labels", resource.GetLabels())

		logger.Info("Reconciling")
		if err := o.client.Create(o.ctx, resource); err != nil {
			if errors.IsAlreadyExists(err) {
				logger.Info("Resource already exists, will update")
				if err = o.client.Update(o.ctx, resource); err == nil {
					continue
				}
			}

			logger.Error(err, "Error reconciling on resource")
			y, _ := yaml.Marshal(&resource)
			logger.Error(err, string(y))
		} else {
			logger.Info("Resource created")
		}
	}
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func shortID(id string) string {
	return id[0:5]
}
