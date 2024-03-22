package workflow

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

type Opts struct {
	ctx          context.Context
	client       client.Client
	logger       logr.Logger
	parentObject *unstructured.Unstructured
	Pipelines    []Pipeline
	source       string
}

type Pipeline struct {
	Name      string
	Resources []client.Object
	Labels    map[string]string
}

func NewOpts(ctx context.Context, client client.Client, logger logr.Logger, obj *unstructured.Unstructured, pipelines []Pipeline, source string) Opts {
	return Opts{
		ctx:          ctx,
		client:       client,
		logger:       logger,
		parentObject: obj,
		source:       source,
		Pipelines:    pipelines,
	}
}

func ReconcileConfigure(opts Opts) (bool, error) {
	originalLogger := opts.logger
	for _, pipeline := range opts.Pipelines {
		opts.logger.Info("pipelines: " + pipeline.Name)
	}
	for _, pipeline := range opts.Pipelines {
		labels := getLabelsForJobs(pipeline)
		pipeline.Labels = labels
		opts.logger = originalLogger.WithName(pipeline.Name).WithValues("labels", pipeline.Labels)
		opts.logger.Info("Reconciling pipeline " + pipeline.Name)
		finished, err := reconcileConfigurePipeline(opts, pipeline)
		if err == nil && finished {
			opts.logger.Info("Pipeline reconciled, moving to next", "name", pipeline.Name)
			continue
		}
		return false, err
	}
	return true, nil
}

func ReconcileDelete(opts Opts, pipeline Pipeline) (bool, error) {
	opts.logger.Info("Reconciling Delete Pipeline")
	pipeline.Labels = getLabelsForJobs(pipeline)
	existingDeletePipeline, err := getDeletePipeline(opts, opts.parentObject.GetNamespace(), pipeline.Labels)
	if err != nil {
		return false, err
	}

	if existingDeletePipeline == nil {
		opts.logger.Info("Creating Delete Pipeline. The pipeline will now execute...")

		//TODO retrieve error information from applyResources to return to the caller
		applyResources(opts, pipeline.Resources...)

		return false, nil
	}

	opts.logger.Info("Checking status of Delete Pipeline")
	if existingDeletePipeline.Status.Succeeded > 0 {
		opts.logger.Info("Delete Pipeline Completed")
		//TODO use const
		controllerutil.RemoveFinalizer(opts.parentObject, v1alpha1.KratixPrefix+"delete-workflows")
		if err := opts.client.Update(opts.ctx, opts.parentObject); err != nil {
			return false, err
		}
		return true, nil
	}

	opts.logger.Info("Delete Pipeline not finished", "status", existingDeletePipeline.Status)
	return false, nil
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

func reconcileConfigurePipeline(opts Opts, pipeline Pipeline) (bool, error) {
	namespace := opts.parentObject.GetNamespace()
	if namespace == "" {
		namespace = v1alpha1.SystemNamespace
	}

	//TODO this only searchs for jobs for a given pipeline, it should search for all?
	// Create request
	// A starts
	// A finished
	// B starts
	// Requests gets updated
	// A-new starts
	// B and A-new are now both in parallel?
	pipelineJobs, err := getJobsWithLabels(opts, pipeline.Labels, namespace)
	if err != nil {
		opts.logger.Info("Failed getting Promise pipeline jobs", "error", err)
		return false, nil
	}

	// No jobs indicates this is the first reconciliation loop of this resource request
	if len(pipelineJobs) == 0 {
		opts.logger.Info("No jobs found, creating workflow Job")
		return false, createConfigurePipeline(opts, pipeline)
	}

	existingPipelineJob, err := resourceutil.PipelineWithDesiredSpecExists(opts.logger, opts.parentObject, pipelineJobs)
	if err != nil {
		return false, nil
	}

	//TODO how does this change with multiple workflows?
	if resourceutil.IsThereAPipelineRunning(opts.logger, pipelineJobs) {
		/* Suspend all pipelines if the promise was updated */
		for _, job := range resourceutil.SuspendablePipelines(opts.logger, pipelineJobs) {
			//Don't suspend a the job that is the desired spec
			if existingPipelineJob != nil && job.GetName() != existingPipelineJob.GetName() {
				trueBool := true
				patch := client.MergeFrom(job.DeepCopy())
				job.Spec.Suspend = &trueBool
				opts.logger.Info("Suspending inactive job", "job", job.GetName())
				err := opts.client.Patch(opts.ctx, &job, patch)
				if err != nil {
					opts.logger.Error(err, "failed to patch Job", "job", job.GetName())
				}
			}
		}

		// Wait the pipeline to complete
		opts.logger.Info("Job already inflight for workflow, waiting for it to be inactive")
		return false, nil
	}

	if isManualReconciliation(opts.parentObject.GetLabels()) || existingPipelineJob == nil {
		opts.logger.Info("Creating job for workflow", "manualTrigger", isManualReconciliation(opts.parentObject.GetLabels()))
		return false, createConfigurePipeline(opts, pipeline)
	}

	opts.logger.Info("Job already exists and is complete for workflow")
	if opts.source == "promise" {
		err := deleteConfigMap(opts, pipeline)
		if err != nil {
			return false, err
		}
	}

	//delete 5 old jobs
	err = deleteAllButLastFiveJobs(opts, pipelineJobs)
	if err != nil {
		opts.logger.Error(err, "failed to delete old jobs")
	}

	return true, nil
}

const numberOfJobsToKeep = 5

func deleteAllButLastFiveJobs(opts Opts, pipelineJobs []batchv1.Job) error {
	if len(pipelineJobs) <= numberOfJobsToKeep {
		return nil
	}

	// Sort jobs by creation time
	pipelineJobs = resourceutil.SortJobsByCreationDateTime(pipelineJobs)

	// Delete all but the last 5 jobs
	for i := 0; i < len(pipelineJobs)-numberOfJobsToKeep; i++ {
		job := pipelineJobs[i]
		opts.logger.Info("Deleting old job", "job", job.GetName())
		if err := opts.client.Delete(opts.ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			if !errors.IsNotFound(err) {
				opts.logger.Info("failed to delete job", "job", job.GetName(), "error", err)
				return nil
			}
		}
	}

	return nil
}

func deleteConfigMap(opts Opts, pipeline Pipeline) error {
	configMap := &v1.ConfigMap{}
	for _, resource := range pipeline.Resources {
		if _, ok := resource.(*v1.ConfigMap); ok {
			configMap = resource.(*v1.ConfigMap)
			break
		}
	}

	opts.logger.Info("Removing configmap", "name", configMap.GetName())
	if err := opts.client.Delete(opts.ctx, configMap); err != nil {
		if !errors.IsNotFound(err) {
			opts.logger.Info("failed to delete configmap", "name", configMap.GetName(), "error", err)
			return err
		}
	}

	return nil
}

func createConfigurePipeline(opts Opts, pipeline Pipeline) error {
	//TODO whats the point of the return value?
	_, err := setPipelineCompletedConditionStatus(opts, opts.parentObject)
	if err != nil {
		return err
	}

	opts.logger.Info("Triggering Promise pipeline")

	applyResources(opts, pipeline.Resources...)

	if isManualReconciliation(opts.parentObject.GetLabels()) {
		newLabels := opts.parentObject.GetLabels()
		delete(newLabels, resourceutil.ManualReconciliationLabel)
		opts.parentObject.SetLabels(newLabels)
		if err := opts.client.Update(opts.ctx, opts.parentObject); err != nil {
			return err
		}
	}

	return nil
}

func setPipelineCompletedConditionStatus(opts Opts, obj *unstructured.Unstructured) (bool, error) {
	switch resourceutil.GetPipelineCompletedConditionStatus(obj) {
	case v1.ConditionTrue:
		fallthrough
	case v1.ConditionUnknown:
		resourceutil.SetStatus(obj, opts.logger, "message", "Pending")
		resourceutil.MarkPipelineAsRunning(opts.logger, obj)
		err := opts.client.Status().Update(opts.ctx, obj)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func getDeletePipeline(opts Opts, namespace string, labels map[string]string) (*batchv1.Job, error) {
	jobs, err := getJobsWithLabels(opts, labels, namespace)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return &jobs[0], nil
}

func getJobsWithLabels(opts Opts, jobLabels map[string]string, namespace string) ([]batchv1.Job, error) {
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
	err = opts.client.List(opts.ctx, jobs, listOps)
	if err != nil {
		opts.logger.Error(err, "error listing jobs", "selectors", selector.String())
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
func applyResources(opts Opts, resources ...client.Object) {
	opts.logger.Info("Reconciling pipeline resources")

	for _, resource := range resources {
		logger := opts.logger.WithValues("gvk", resource.GetObjectKind().GroupVersionKind(), "name", resource.GetName(), "namespace", resource.GetNamespace(), "labels", resource.GetLabels())

		logger.Info("Reconciling")
		if err := opts.client.Create(opts.ctx, resource); err != nil {
			if errors.IsAlreadyExists(err) {
				logger.Info("Resource already exists, will update")
				if err = opts.client.Update(opts.ctx, resource); err == nil {
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
