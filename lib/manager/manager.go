package manager

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"gopkg.in/yaml.v2"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type opts struct {
	ctx    context.Context
	client client.Client
	logger logr.Logger
}

type WorkflowOpts struct {
	opts
	parentObject *unstructured.Unstructured
	pipelines    []Pipeline
	source       string
}

type Pipeline struct {
	Name      string
	Resources []client.Object
	Labels    map[string]string
}

func NewWorkflowOpts(ctx context.Context, client client.Client, logger logr.Logger, obj *unstructured.Unstructured, pipelines []Pipeline, source string) WorkflowOpts {
	return WorkflowOpts{
		opts: opts{
			ctx:    ctx,
			client: client,
			logger: logger,
		},
		parentObject: obj,
		source:       source,
		pipelines:    pipelines,
	}
}

func ReconcileConfigurePipeline(w WorkflowOpts) (bool, error) {
	originalLogger := w.logger
	for _, pipeline := range w.pipelines {
		w.logger.Info("pipelines: " + pipeline.Name)
	}
	for _, pipeline := range w.pipelines {
		labels := getLabelsForJobs(pipeline)
		pipeline.Labels = labels
		w.logger = originalLogger.WithName(pipeline.Name).WithValues("labels", pipeline.Labels)
		w.logger.Info("Reconciling pipeline " + pipeline.Name)
		finished, err := reconcileConfigurePipeline(w, pipeline)
		if err == nil && finished {
			w.logger.Info("Pipeline reconciled, moving to next", "name", pipeline.Name)
			continue
		}
		return finished, err
	}
	return true, nil
}

func getLabelsForJobs(pipeline Pipeline) map[string]string {
	for _, resource := range pipeline.Resources {
		if job, ok := resource.(*batchv1.Job); ok {
			labels := job.DeepCopy().GetLabels()
			//TODO use const
			delete(labels, "kratix-resource-hash")
			return labels
		}
	}
	//TODO fix
	panic("whoops")
}

func reconcileConfigurePipeline(w WorkflowOpts, pipeline Pipeline) (bool, error) {
	namespace := w.parentObject.GetNamespace()
	if namespace == "" {
		namespace = v1alpha1.SystemNamespace
	}

	pipelineJobs, err := getJobsWithLabels(w.opts, pipeline.Labels, namespace)
	if err != nil {
		w.logger.Info("Failed getting Promise pipeline jobs", "error", err)
		return false, nil
	}

	// No jobs indicates this is the first reconciliation loop of this resource request
	if len(pipelineJobs) == 0 {
		w.logger.Info("No jobs found, creating workflow Job")
		return false, createConfigurePipeline(w, pipeline)
	}

	existingPipelineJob, err := resourceutil.PipelineWithDesiredSpecExists(w.logger, w.parentObject, pipelineJobs)
	if err != nil {
		return false, nil
	}

	if resourceutil.IsThereAPipelineRunning(w.logger, pipelineJobs) {
		/* Suspend all pipelines if the promise was updated */
		for _, job := range resourceutil.SuspendablePipelines(w.logger, pipelineJobs) {
			//Don't suspend a the job that is the desired spec
			if existingPipelineJob != nil && job.GetName() != existingPipelineJob.GetName() {
				trueBool := true
				patch := client.MergeFrom(job.DeepCopy())
				job.Spec.Suspend = &trueBool
				w.logger.Info("Suspending inactive job", "job", job.GetName())
				err := w.client.Patch(w.ctx, &job, patch)
				if err != nil {
					w.logger.Error(err, "failed to patch Job", "job", job.GetName())
				}
			}
		}

		// Wait the pipeline to complete
		w.logger.Info("Job already inflight for workflow, waiting for it to be inactive")
		return false, nil
	}

	if isManualReconciliation(w.parentObject.GetLabels()) || existingPipelineJob == nil {
		w.logger.Info("Creating job for workflow", "manualTrigger", isManualReconciliation(w.parentObject.GetLabels()))
		return false, createConfigurePipeline(w, pipeline)
	}

	w.logger.Info("Job already exists and is complete for workflow")
	if w.source == "promise" {
		err := deleteConfigMap(w, pipeline)
		if err != nil {
			return false, err
		}
	}

	//delete 5 old jobs
	err = deleteAllButLastFiveJobs(w, pipelineJobs)
	if err != nil {
		w.logger.Error(err, "failed to delete old jobs")
	}

	return true, nil
}

const numberOfJobsToKeep = 5

func deleteAllButLastFiveJobs(j WorkflowOpts, pipelineJobs []batchv1.Job) error {
	if len(pipelineJobs) <= numberOfJobsToKeep {
		return nil
	}

	// Sort jobs by creation time
	pipelineJobs = resourceutil.SortJobsByCreationDateTime(pipelineJobs)

	// Delete all but the last 5 jobs
	for i := 0; i < len(pipelineJobs)-numberOfJobsToKeep; i++ {
		job := pipelineJobs[i]
		j.logger.Info("Deleting old job", "job", job.GetName())
		if err := j.client.Delete(j.ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			if !errors.IsNotFound(err) {
				j.logger.Info("failed to delete job", "job", job.GetName(), "error", err)
				return nil
			}
		}
	}

	return nil
}

func deleteConfigMap(j WorkflowOpts, pipeline Pipeline) error {
	configMap := &v1.ConfigMap{}
	for _, resource := range pipeline.Resources {
		if _, ok := resource.(*v1.ConfigMap); ok {
			configMap = resource.(*v1.ConfigMap)
			break
		}
	}

	j.logger.Info("Removing configmap", "name", configMap.GetName())
	if err := j.client.Delete(j.ctx, configMap); err != nil {
		if !errors.IsNotFound(err) {
			j.logger.Info("failed to delete configmap", "name", configMap.GetName(), "error", err)
			return err
		}
	}

	return nil
}

func createConfigurePipeline(j WorkflowOpts, pipeline Pipeline) error {
	//TODO whats the point of the return value?
	_, err := setPipelineCompletedConditionStatus(j.opts, j.parentObject)
	if err != nil {
		return err
	}

	j.logger.Info("Triggering Promise pipeline")

	applyResources(j.opts, pipeline.Resources...)

	if isManualReconciliation(j.parentObject.GetLabels()) {
		newLabels := j.parentObject.GetLabels()
		delete(newLabels, resourceutil.ManualReconciliationLabel)
		j.parentObject.SetLabels(newLabels)
		if err := j.client.Update(j.ctx, j.parentObject); err != nil {
			return err
		}
	}

	return nil
}

func setPipelineCompletedConditionStatus(o opts, obj *unstructured.Unstructured) (bool, error) {
	switch resourceutil.GetPipelineCompletedConditionStatus(obj) {
	case v1.ConditionTrue:
		fallthrough
	case v1.ConditionUnknown:
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

func ReconcileDeletePipeline(jobOpts WorkflowOpts, pipeline Pipeline) (bool, error) {
	jobOpts.logger.Info("Reconciling Delete Pipeline")
	pipeline.Labels = getLabelsForJobs(pipeline)
	existingDeletePipeline, err := getDeletePipeline(jobOpts.opts, jobOpts.parentObject.GetNamespace(), pipeline.Labels)
	if err != nil {
		return false, err
	}

	if existingDeletePipeline == nil {
		jobOpts.logger.Info("Creating Delete Pipeline. The pipeline will now execute...")

		//TODO retrieve error information from applyResources to return to the caller
		applyResources(jobOpts.opts, pipeline.Resources...)

		return false, nil
	}

	jobOpts.logger.Info("Checking status of Delete Pipeline")
	if existingDeletePipeline.Status.Succeeded > 0 {
		jobOpts.logger.Info("Delete Pipeline Completed")
		//TODO use const
		controllerutil.RemoveFinalizer(jobOpts.parentObject, v1alpha1.KratixPrefix+"delete-workflows")
		if err := jobOpts.client.Update(jobOpts.ctx, jobOpts.parentObject); err != nil {
			return false, err
		}
		return true, nil
	}

	jobOpts.logger.Info("Delete Pipeline not finished", "status", existingDeletePipeline.Status)
	return false, nil
}

func getDeletePipeline(o opts, namespace string, labels map[string]string) (*batchv1.Job, error) {
	jobs, err := getJobsWithLabels(o, labels, namespace)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return &jobs[0], nil
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

func isManualReconciliation(labels map[string]string) bool {
	if labels == nil {
		return false
	}
	val, exists := labels[resourceutil.ManualReconciliationLabel]
	return exists && val == "true"
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
