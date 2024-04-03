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
)

type Opts struct {
	ctx          context.Context
	client       client.Client
	logger       logr.Logger
	parentObject *unstructured.Unstructured
	//TODO make this field private too? or everything public and no constructor func
	Pipelines []Pipeline
	source    string
}

type Pipeline struct {
	Name string
	Job  *batchv1.Job
	// ServiceAccount, Role, Rolebinding, ConfigMap etc (differs for delete vs configure)
	JobRequiredResources []client.Object
}

func NewOpts(ctx context.Context, client client.Client, logger logr.Logger, parentObj *unstructured.Unstructured, pipelines []Pipeline, source string) Opts {
	return Opts{
		ctx:          ctx,
		client:       client,
		logger:       logger,
		parentObject: parentObj,
		source:       source,
		Pipelines:    pipelines,
	}
}

// TODO refactor
func ReconcileDelete(opts Opts, pipelines []Pipeline) (bool, error) {
	opts.logger.Info("Reconciling Delete Pipeline")

	if len(pipelines) == 0 {
		return true, nil
	}

	if len(pipelines) > 1 {
		opts.logger.Info("Multiple delete pipeline found but only one delete pipeline is currently supported. Ignoring all but the first")
	}

	pipeline := pipelines[0]
	existingDeletePipeline, err := getDeletePipeline(opts, opts.parentObject.GetNamespace(), pipeline)
	if err != nil {
		return false, err
	}

	if existingDeletePipeline == nil {
		opts.logger.Info("Creating Delete Pipeline. The pipeline will now execute...")

		//TODO retrieve error information from applyResources to return to the caller
		applyResources(opts, append(pipeline.JobRequiredResources, pipeline.Job)...)

		return false, nil
	}

	opts.logger.Info("Checking status of Delete Pipeline")
	if existingDeletePipeline.Status.Succeeded > 0 {
		opts.logger.Info("Delete Pipeline Completed")
		return true, nil
	}

	opts.logger.Info("Delete Pipeline not finished", "status", existingDeletePipeline.Status)
	return false, nil
}

// Job:
// kratix.io/promise-name
// kratix.io/hash
// kratix.io/pipeline-name
// kratix.io/resource-name

// Old labels:
// kratix-promise-id
// kratix-workflow-kind
// kraitx-workflow-promise-version
// kratix-workflow-type
// kratix-workflow-action
// kratix-workflow-pipeline-name
func getLabelsForPipelineJob(pipeline Pipeline) map[string]string {
	labels := pipeline.Job.DeepCopy().GetLabels()
	return labels
}

func labelsForPipelineJob(pipeline Pipeline) map[string]string {
	return map[string]string{
		v1alpha1.KratixResourceHashLabel: pipeline.Job.GetLabels()[v1alpha1.KratixResourceHashLabel],
		v1alpha1.PipelineNameLabel:       pipeline.Job.GetLabels()[v1alpha1.PipelineNameLabel],
	}
}

func labelsForAllPipelineJobs(pipeline Pipeline) map[string]string {
	pipelineLabels := pipeline.Job.GetLabels()
	labels := map[string]string{
		v1alpha1.PromiseNameLabel: pipelineLabels[v1alpha1.PromiseNameLabel],
	}
	if pipelineLabels[v1alpha1.ResourceNameLabel] != "" {
		labels[v1alpha1.ResourceNameLabel] = pipelineLabels[v1alpha1.ResourceNameLabel]
	}
	return labels
}

func ReconcileConfigure(opts Opts) (bool, error) {
	originalLogger := opts.logger
	namespace := opts.parentObject.GetNamespace()
	if namespace == "" {
		namespace = v1alpha1.SystemNamespace
	}

	for _, pipeline := range opts.Pipelines {
		opts.logger = originalLogger.WithName(pipeline.Name)
		// TODO this will be very noisy - might want to slowRequeue?
		opts.logger.Info("Reconciling pipeline " + pipeline.Name)
		finished, err := reconcileConfigurePipeline(opts, namespace, pipeline)
		if err != nil || !finished {
			return false, err
		}
		opts.logger.Info("Pipeline reconciled, moving to next", "name", pipeline.Name)
	}

	if opts.source == "promise" {
		for _, pipeline := range opts.Pipelines {
			if err := deleteConfigMap(opts, pipeline); err != nil {
				return false, err
			}
		}
	}

	pipelineNames := map[string]bool{}
	for _, pipeline := range opts.Pipelines {
		l := labelsForAllPipelineJobs(pipeline)
		l[v1alpha1.PipelineNameLabel] = pipeline.Name
		pipelineNames[pipeline.Name] = true
		jobsForPipeline, _ := getJobsWithLabels(opts, l, namespace)
		// TODO: come back to this and reason about it
		if err := deleteAllButLastFiveJobs(opts, jobsForPipeline); err != nil {
			opts.logger.Error(err, "failed to delete old jobs")
			return false, err
		}
	}

	allPipelineWorks, err := resourceutil.GetWorksByType(opts.client, v1alpha1.Type(opts.source), opts.parentObject)
	if err != nil {
		opts.logger.Error(err, "failed to list works for Promise", "promise", opts.parentObject.GetName())
		return false, err
	}
	for _, work := range allPipelineWorks {
		workPipelineName := work.GetLabels()[v1alpha1.PipelineNameLabel]
		if !pipelineNames[workPipelineName] {
			opts.logger.Info("Deleting old work", "work", work.GetName(), "objectName", opts.parentObject.GetName(), "workType", work.Labels[v1alpha1.WorkTypeLabel])
			if err := opts.client.Delete(opts.ctx, &work); err != nil {
				opts.logger.Error(err, "failed to delete old work", "work", work.GetName())
				return false, err
			}

		}
	}

	return true, nil
}

func reconcileConfigurePipeline(opts Opts, namespace string, pipeline Pipeline) (bool, error) {
	opts.logger.Info("pipeline labels", "labels", pipeline.Job.GetLabels())
	pipelineJobsAtCurrentSpec, err := getJobsWithLabels(opts, labelsForPipelineJob(pipeline), namespace)
	if err != nil {
		return false, err
	}

	if len(pipelineJobsAtCurrentSpec) == 0 {
		allJobsWithParentObject, err := getJobsWithLabels(opts, labelsForAllPipelineJobs(pipeline), namespace)
		if err != nil {
			return false, err
		}

		if resourceutil.IsThereAPipelineRunning(opts.logger, allJobsWithParentObject) {
			for _, job := range resourceutil.SuspendablePipelines(opts.logger, allJobsWithParentObject) {
				trueBool := true
				patch := client.MergeFrom(job.DeepCopy())
				job.Spec.Suspend = &trueBool
				opts.logger.Info("Suspending inactive job", "job", job.GetName())
				err := opts.client.Patch(opts.ctx, &job, patch)
				if err != nil {
					opts.logger.Error(err, "failed to patch Job", "job", job.GetName())
				}
			}
			return false, nil
		}

		opts.logger.Info("No jobs found for resource at current spec, creating workflow Job")

		return false, createConfigurePipeline(opts, pipeline)
	}

	if resourceutil.IsThereAPipelineRunning(opts.logger, pipelineJobsAtCurrentSpec) {
		opts.logger.Info("Job already inflight for workflow, waiting for it to be inactive")
		return false, nil
	}

	if isManualReconciliation(opts.parentObject.GetLabels()) {
		opts.logger.Info("Creating job for workflow", "manualTrigger", isManualReconciliation(opts.parentObject.GetLabels()))
		return false, createConfigurePipeline(opts, pipeline)
	}

	allJobsWithParentObject, err := getJobsWithLabels(opts, labelsForAllPipelineJobs(pipeline), namespace)
	if err != nil {
		return false, err
	}

	resourceutil.SortJobsByCreationDateTime(allJobsWithParentObject, false)
	foundJob := false
	for _, job := range allJobsWithParentObject {
		if job.GetLabels()[v1alpha1.KratixResourceHashLabel] != pipeline.Job.GetLabels()[v1alpha1.KratixResourceHashLabel] {
			break
		}

		if job.GetLabels()[v1alpha1.PipelineNameLabel] == pipeline.Job.GetLabels()[v1alpha1.PipelineNameLabel] {
			foundJob = true
			break
		}
	}

	if !foundJob {
		return false, createConfigurePipeline(opts, pipeline)
	}

	return true, nil
}

const numberOfJobsToKeep = 5

func deleteAllButLastFiveJobs(opts Opts, pipelineJobsAtCurrentSpec []batchv1.Job) error {
	if len(pipelineJobsAtCurrentSpec) <= numberOfJobsToKeep {
		return nil
	}

	// Sort jobs by creation time
	pipelineJobsAtCurrentSpec = resourceutil.SortJobsByCreationDateTime(pipelineJobsAtCurrentSpec, true)

	// Delete all but the last 5 jobs
	for i := 0; i < len(pipelineJobsAtCurrentSpec)-numberOfJobsToKeep; i++ {
		job := pipelineJobsAtCurrentSpec[i]
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
	for _, resource := range pipeline.JobRequiredResources {
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

	applyResources(opts, append(pipeline.JobRequiredResources, pipeline.Job)...)

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

func getDeletePipeline(opts Opts, namespace string, pipeline Pipeline) (*batchv1.Job, error) {
	labels := getLabelsForPipelineJob(pipeline)
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
