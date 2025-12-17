package workflow

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/tools/record"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
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
	Resources            []v1alpha1.PipelineJobResources
	workflowType         string
	numberOfJobsToKeep   int
	eventRecorder        record.EventRecorder
	namespace            string
	SkipConditions       bool
	retryAfterConfigured bool
	retryAfterRemaining  time.Duration
}

var minimumPeriodBetweenCreatingPipelineResources = 1100 * time.Millisecond
var ErrDeletePipelineFailed = fmt.Errorf("Delete Pipeline Failed")

func NewOpts(ctx context.Context, client client.Client, eventRecorder record.EventRecorder, logger logr.Logger, parentObj *unstructured.Unstructured, resources []v1alpha1.PipelineJobResources, workflowType string, numberOfJobsToKeep int, namespace string) Opts {
	return Opts{
		ctx:                ctx,
		client:             client,
		logger:             logger,
		parentObject:       parentObj,
		workflowType:       workflowType,
		numberOfJobsToKeep: numberOfJobsToKeep,
		Resources:          resources,
		eventRecorder:      eventRecorder,
		namespace:          namespace,
	}
}

func (o Opts) WithRetryAfter(configured bool, remaining time.Duration) Opts {
	o.retryAfterConfigured = configured
	o.retryAfterRemaining = remaining
	return o
}

// ReconcileDelete deletes Workflows.
func ReconcileDelete(opts Opts) (bool, error) {
	logging.Debug(opts.logger, "reconciling delete pipeline")

	if len(opts.Resources) == 0 {
		return false, nil
	}

	if len(opts.Resources) > 1 {
		logging.Warn(opts.logger, "multiple delete pipelines found; only the first will be used")
	}

	pipeline := opts.Resources[0]
	isManualReconciliation := isManualReconciliation(opts.parentObject.GetLabels())
	mostRecentJob, err := getMostRecentDeletePipelineJob(opts, opts.namespace, pipeline)
	if err != nil {
		return false, err
	}

	if isManualReconciliation {
		logging.Info(opts.logger, "manual reconciliation detected for delete pipeline", "pipeline", pipeline.Name)
	}

	if isRunning(mostRecentJob) {
		if isManualReconciliation {
			logging.Info(opts.logger, "suspending job for manual reconciliation", "job", mostRecentJob.Name, "pipeline", pipeline.Name)
			if err = suspendJob(opts.ctx, opts.client, mostRecentJob); err != nil {
				logging.Error(opts.logger, err, "failed to suspend job", "job", mostRecentJob.GetName())
			}
			opts.eventRecorder.Eventf(opts.parentObject, "Normal", "PipelineSuspended", "Delete Pipeline suspended: %s", opts.Resources[0].Name)
			return true, err
		}

		logging.Debug(opts.logger, "job already inflight for pipeline; waiting for completion", "job", mostRecentJob.Name, "pipeline", pipeline.Name)
		return true, nil
	}

	if mostRecentJob == nil || isManualReconciliation {
		return createDeletePipeline(opts, pipeline)
	}

	logging.Debug(opts.logger, "checking status of delete pipeline")
	if mostRecentJob.Status.Succeeded > 0 {
		logging.Info(opts.logger, "delete pipeline completed")
		return false, nil
	}
	if mostRecentJob.Status.Failed > 0 {
		return false, ErrDeletePipelineFailed
	}

	logging.Debug(opts.logger, "delete pipeline still running", "status", mostRecentJob.Status)
	return true, nil
}

func createDeletePipeline(opts Opts, pipeline v1alpha1.PipelineJobResources) (abort bool, err error) {
	logging.Debug(opts.logger, "creating delete pipeline; execution will commence")
	if isManualReconciliation(opts.parentObject.GetLabels()) {
		if err := removeManualReconciliationLabel(opts); err != nil {
			return false, err
		}
	}
	//TODO retrieve error information from applyResources to return to the caller
	applyResources(opts, append(pipeline.GetObjects(), pipeline.Job)...)
	opts.eventRecorder.Eventf(opts.parentObject, "Normal", "PipelineStarted", "Delete Pipeline started: %s", opts.Resources[0].Name)
	return true, nil
}

func ReconcileConfigure(opts Opts) (abort bool, err error) {
	var pipelineIndex = 0
	var mostRecentJob *batchv1.Job

	originalLogger := opts.logger
	allJobs, err := getJobsWithLabels(opts, labelsForJobs(opts), opts.namespace)
	if err != nil {
		logging.Error(opts.logger, err, "failed to list jobs")
		return false, err
	}

	// TODO: this part will be deprecated when we stop using the legacy labels
	allLegacyJobs, err := getJobsWithLabels(opts, legacyLabelsForJobs(opts), opts.namespace)
	if err != nil {
		logging.Error(opts.logger, err, "failed to list jobs")
		return false, err
	}
	allJobs = append(allJobs, allLegacyJobs...)

	if len(allJobs) != 0 {
		logging.Debug(opts.logger, "found existing jobs; checking pipeline match for most recent job")
		resourceutil.SortJobsByCreationDateTime(allJobs, false)
		mostRecentJob = &allJobs[0]
		pipelineIndex = nextPipelineIndex(opts, mostRecentJob)
	}

	retryAfterConfigured := opts.retryAfterConfigured
	retryAfterRemaining := opts.retryAfterRemaining

	if !opts.SkipConditions {
		var updateStatus bool
		if pipelineIndex == 0 {
			workflowsFailed := resourceutil.GetWorkflowsCounterStatus(opts.parentObject, "workflowsFailed")
			if workflowsFailed != 0 {
				resourceutil.SetStatus(opts.parentObject, opts.logger, "workflowsFailed", int64(0))
				updateStatus = true
			}
		}

		workflowsSucceededCount := resourceutil.GetWorkflowsCounterStatus(opts.parentObject, "workflowsSucceeded")
		if updateStatus || workflowsSucceededCount != int64(pipelineIndex) {
			resourceutil.SetStatus(opts.parentObject, opts.logger, "workflowsSucceeded", int64(pipelineIndex))
			if err = opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
				logging.Error(opts.logger, err, "failed to update parent object status")
				return false, err
			}
		}
	}

	if pipelineIndex >= len(opts.Resources) {
		pipelineIndex = len(opts.Resources) - 1
	}

	logging.Trace(opts.logger, "calculated pipeline index", "index", pipelineIndex)

	if pipelineIndex < 0 {
		logging.Debug(opts.logger, "no pipeline to reconcile")
		return false, nil
	}

	var mostRecentJobName = "n/a"
	if mostRecentJob != nil {
		mostRecentJobName = mostRecentJob.Name
	}

	logging.Info(opts.logger, "reconciling configure workflow", "pipelineIndex", pipelineIndex, "mostRecentJob", mostRecentJobName)

	pipeline := opts.Resources[pipelineIndex]
	isManualReconciliation := isManualReconciliation(opts.parentObject.GetLabels())
	opts.logger = originalLogger.WithName(pipeline.Name).WithValues("isManualReconciliation", isManualReconciliation)

	if jobIsForPipeline(pipeline, mostRecentJob) {
		logging.Trace(opts.logger, "checking if job is for pipeline", "job", mostRecentJob.Name, "pipeline", pipeline.Name)
		if isRunning(mostRecentJob) {
			if isManualReconciliation {
				logging.Info(opts.logger, "suspending job for manual reconciliation", "job", mostRecentJob.Name, "pipeline", pipeline.Name)
				if err = suspendJob(opts.ctx, opts.client, mostRecentJob); err != nil {
					logging.Error(opts.logger, err, "failed to suspend job", "job", mostRecentJob.GetName())
				}
				return true, err
			}

			logging.Debug(opts.logger, "job already inflight for pipeline; waiting for completion", "job", mostRecentJob.Name, "pipeline", pipeline.Name)
			return true, nil
		}

		if retryAfterConfigured {
			if retryAfterRemaining > 0 {
				logging.Info(opts.logger, "retryAfter set; requeuing pipeline execution", "remaining", retryAfterRemaining.String(), "pipeline", pipeline.Name)
				return true, nil
			}

			logging.Info(opts.logger, "retryAfter elapsed; rerunning pipeline", "pipeline", pipeline.Name)
			return createConfigurePipeline(opts, pipelineIndex, pipeline)
		}

		if isManualReconciliation {
			logging.Info(opts.logger, "pipeline running due to manual reconciliation", "pipeline", pipeline.Name, "parentLabels", opts.parentObject.GetLabels())
			return createConfigurePipeline(opts, pipelineIndex, pipeline)
		}

		if isFailed(mostRecentJob) {
			return setFailedConditionAndEvents(opts, mostRecentJob, pipeline)
		}

		return false, cleanup(opts, opts.namespace)
	}

	if isRunning(mostRecentJob) {
		logging.Info(opts.logger, "job already inflight for another workflow; suspending it", "job", mostRecentJob.Name)
		err = suspendJob(opts.ctx, opts.client, mostRecentJob)
		if err != nil {
			logging.Error(opts.logger, err, "failed to suspend job", "job", mostRecentJob.GetName())
		}
		return true, nil
	}

	return createConfigurePipeline(opts, pipelineIndex, pipeline)
}

func setFailedConditionAndEvents(opts Opts, mostRecentJob *batchv1.Job, pipeline v1alpha1.PipelineJobResources) (bool, error) {
	if !opts.SkipConditions {
		resourceutil.MarkConfigureWorkflowAsFailed(opts.logger, opts.parentObject, pipeline.Name)
		resourceutil.MarkReconciledFailing(opts.parentObject, resourceutil.ConfigureWorkflowCompletedFailedReason)
		resourceutil.SetStatus(opts.parentObject, opts.logger, "workflowsFailed", int64(1))
		if err := opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
			logging.Error(opts.logger, err, "failed to update parent object status")
			return false, err
		}
	}
	opts.eventRecorder.Eventf(opts.parentObject, v1.EventTypeWarning,
		resourceutil.ConfigureWorkflowCompletedFailedReason, "A %s/configure Pipeline has failed: %s", opts.workflowType, pipeline.Name)
	logging.Warn(opts.logger, "pipeline job failed; exiting workflow", "failedJob", mostRecentJob.Name, "pipeline", pipeline.Name)
	return true, nil
}

func suspendJob(ctx context.Context, c client.Client, job *batchv1.Job) error {
	trueBool := true
	patch := client.MergeFrom(job.DeepCopy())
	job.Spec.Suspend = &trueBool
	return c.Patch(ctx, job, patch)
}

func getLabelsForPipelineJob(pipeline v1alpha1.PipelineJobResources) map[string]string {
	labels := pipeline.Job.DeepCopy().GetLabels()
	return labels
}

func labelsForJobs(opts Opts) map[string]string {
	l := map[string]string{
		v1alpha1.WorkflowTypeLabel: opts.workflowType,
	}
	promiseName := opts.parentObject.GetName()
	if strings.HasPrefix(opts.workflowType, string(v1alpha1.WorkflowTypeResource)) {
		promiseName = opts.parentObject.GetLabels()[v1alpha1.PromiseNameLabel]
		l[v1alpha1.ResourceNameLabel] = opts.parentObject.GetName()
		if opts.namespace != opts.parentObject.GetNamespace() {
			// only set resource request namespace label when workflow running in different namespace from the resource requests
			l[v1alpha1.ResourceNamespaceLabel] = opts.parentObject.GetNamespace()
		}
	}
	l[v1alpha1.PromiseNameLabel] = promiseName
	return l
}

// TODO: this part will be deprecated when we stop using the legacy labels
func legacyLabelsForJobs(opts Opts) map[string]string {
	l := map[string]string{
		v1alpha1.WorkTypeLabel: opts.workflowType,
	}
	promiseName := opts.parentObject.GetName()
	if opts.workflowType == string(v1alpha1.WorkTypeResource) {
		promiseName = opts.parentObject.GetLabels()[v1alpha1.PromiseNameLabel]
		l[v1alpha1.ResourceNameLabel] = opts.parentObject.GetName()
	}
	l[v1alpha1.PromiseNameLabel] = promiseName
	return l
}

func labelsForAllPipelineJobs(pipeline v1alpha1.PipelineJobResources) map[string]string {
	pipelineLabels := pipeline.Job.GetLabels()
	labels := map[string]string{
		v1alpha1.PromiseNameLabel: pipelineLabels[v1alpha1.PromiseNameLabel],
	}
	if pipelineLabels[v1alpha1.ResourceNameLabel] != "" {
		labels[v1alpha1.ResourceNameLabel] = pipelineLabels[v1alpha1.ResourceNameLabel]
	}
	if pipelineLabels[v1alpha1.ResourceNamespaceLabel] != "" {
		labels[v1alpha1.ResourceNamespaceLabel] = pipelineLabels[v1alpha1.ResourceNamespaceLabel]
	}
	return labels
}

func jobIsForPipeline(pipeline v1alpha1.PipelineJobResources, job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	jobLabels := job.GetLabels()
	pipelineLabels := pipeline.Job.GetLabels()

	if jobLabels[v1alpha1.KratixResourceHashLabel] != pipelineLabels[v1alpha1.KratixResourceHashLabel] {
		return false
	}

	if jobLabels[v1alpha1.WorkflowTypeLabel] != pipelineLabels[v1alpha1.WorkflowTypeLabel] {
		return false
	}

	if jobLabels[v1alpha1.WorkflowActionLabel] != pipelineLabels[v1alpha1.WorkflowActionLabel] {
		return false
	}

	if jobLabels[v1alpha1.KratixPipelineHashLabel] != pipelineLabels[v1alpha1.KratixPipelineHashLabel] {
		return false
	}

	return jobLabels[v1alpha1.PipelineNameLabel] == pipelineLabels[v1alpha1.PipelineNameLabel]
}

func nextPipelineIndex(opts Opts, mostRecentJob *batchv1.Job) int {
	if mostRecentJob == nil || isManualReconciliation(opts.parentObject.GetLabels()) {
		return 0
	}

	// in reverse order loop through the pipeline, see if the latest job is for
	// the pipeline if it is and its finished then we know the pipeline at the
	// index is done, and we need to start the next one
	i := len(opts.Resources) - 1
	for i >= 0 {
		if jobIsForPipeline(opts.Resources[i], mostRecentJob) {
			logging.Trace(opts.logger, "found job for pipeline", "pipeline", opts.Resources[i].Name, "job", mostRecentJob.Name, "status", mostRecentJob.Status, "index", i)
			if isFailed(mostRecentJob) || isRunning(mostRecentJob) {
				return i
			}
			break
		}
		i -= 1
	}

	return i + 1
}

func isFailed(job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobFailed || condition.Type == batchv1.JobSuspended {
			return true
		}
	}
	return false
}

func isRunning(job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	if job.Status.Active > 0 {
		return true
	}

	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobSuspended || condition.Type == batchv1.JobFailed {
			return false
		}
	}
	return true
}

func cleanup(opts Opts, namespace string) error {
	pipelineNames := map[string]bool{}
	for _, pipeline := range opts.Resources {
		l := labelsForAllPipelineJobs(pipeline)
		l[v1alpha1.PipelineNameLabel] = pipeline.Name
		pipelineNames[pipeline.Name] = true
		jobsForPipeline, _ := getJobsWithLabels(opts, l, namespace)
		if err := cleanupJobs(opts, jobsForPipeline); err != nil {
			logging.Error(opts.logger, err, "failed to delete old jobs")
			return err
		}
	}

	allPipelineWorks, err := resourceutil.GetWorksByType(opts.client, v1alpha1.Type(opts.workflowType), opts.parentObject)
	if err != nil {
		logging.Error(opts.logger, err, "failed to list works for Promise", "promise", opts.parentObject.GetName())
		return err
	}
	for _, work := range allPipelineWorks {
		workPipelineName := work.GetLabels()[v1alpha1.PipelineNameLabel]
		if !pipelineNames[workPipelineName] {
			logging.Debug(opts.logger, "deleting old work", "work", work.GetName(), "objectName", opts.parentObject.GetName(), "workType", work.Labels[v1alpha1.WorkTypeLabel])
			if err := opts.client.Delete(opts.ctx, &work); err != nil {
				logging.Error(opts.logger, err, "failed to delete old work", "work", work.GetName())
				return err
			}

		}
	}

	return nil
}

func cleanupJobs(opts Opts, pipelineJobsAtCurrentSpec []batchv1.Job) error {
	if len(pipelineJobsAtCurrentSpec) <= opts.numberOfJobsToKeep {
		return nil
	}

	// Sort jobs by creation time
	pipelineJobsAtCurrentSpec = resourceutil.SortJobsByCreationDateTime(pipelineJobsAtCurrentSpec, true)

	// Delete all but the last n jobs; n defaults to 5 and can be configured by env var for the operator
	for i := 0; i < len(pipelineJobsAtCurrentSpec)-opts.numberOfJobsToKeep; i++ {
		job := pipelineJobsAtCurrentSpec[i]
		logging.Debug(opts.logger, "deleting old job", "job", job.GetName())
		if err := opts.client.Delete(opts.ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			if !errors.IsNotFound(err) {
				logging.Warn(opts.logger, "failed to delete job; will retry", "job", job.GetName(), "error", err)
				return nil
			}
		}
	}

	return nil
}

func createConfigurePipeline(opts Opts, pipelineIndex int, resources v1alpha1.PipelineJobResources) (abort bool, err error) {
	updated, err := setConfigureWorkflowCompletedConditionStatus(opts, pipelineIndex == 0, opts.parentObject)
	if err != nil || updated {
		return updated, err
	}

	logging.Info(opts.logger, "triggering pipeline", "workflowAction", resources.WorkflowAction)

	var objectToDelete []client.Object
	if objectToDelete, err = getObjectsToDelete(opts, resources); err != nil {
		return false, err
	}

	logging.Trace(opts.logger, "reconciling for parent object", "parent", opts.parentObject.GetName())
	if isManualReconciliation(opts.parentObject.GetLabels()) {
		if err := removeManualReconciliationLabel(opts); err != nil {
			return false, err
		}
	}

	deleteResources(opts, objectToDelete...)
	applyResources(opts, append(resources.GetObjects(), resources.Job)...)

	opts.eventRecorder.Eventf(opts.parentObject, "Normal", "PipelineStarted", "Configure Pipeline started: %s", resources.Name)

	return true, nil
}

func removeManualReconciliationLabel(opts Opts) error {
	logging.Debug(opts.logger, "manual reconciliation label detected; removing it")
	return removeLabel(opts, resourceutil.ManualReconciliationLabel)
}

func removeLabel(opts Opts, labelKey string) error {
	newLabels := opts.parentObject.GetLabels()
	delete(newLabels, labelKey)
	opts.parentObject.SetLabels(newLabels)
	if err := opts.client.Update(opts.ctx, opts.parentObject); err != nil {
		logging.Error(opts.logger, err, "failed to remove manual reconciliation label")
		return err
	}
	return nil
}

func setConfigureWorkflowCompletedConditionStatus(opts Opts, isTheFirstPipeline bool, obj *unstructured.Unstructured) (bool, error) {
	if opts.SkipConditions {
		return false, nil
	}
	switch resourceutil.GetConfigureWorkflowCompletedConditionStatus(obj) {
	case v1.ConditionTrue:
		fallthrough
	case v1.ConditionUnknown:
		currentMessage := resourceutil.GetStatus(obj, "message")
		if isTheFirstPipeline || currentMessage == "" || currentMessage == "Resource requested" {
			resourceutil.SetStatus(obj, opts.logger, "message", "Pending")
		}
		resourceutil.MarkConfigureWorkflowAsRunning(opts.logger, obj)
		resourceutil.MarkReconciledPending(obj, "WorkflowPending")
		err := opts.client.Status().Update(opts.ctx, obj)
		if err != nil {
			logging.Error(opts.logger, err, "failed to update object status")
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}

func getMostRecentDeletePipelineJob(opts Opts, namespace string, pipeline v1alpha1.PipelineJobResources) (*batchv1.Job, error) {
	labels := getLabelsForPipelineJob(pipeline)
	jobs, err := getJobsWithLabels(opts, labels, namespace)
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	resourceutil.SortJobsByCreationDateTime(jobs, false)
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
		logging.Error(opts.logger, err, "error listing jobs", "selectors", selector.String())
		return nil, err
	}
	return jobs.Items, nil
}

func isManualReconciliation(labels map[string]string) bool {
	return isLabelSetToTrue(labels, resourceutil.ManualReconciliationLabel)
}

func isLabelSetToTrue(labels map[string]string, labelKey string) bool {
	if labels == nil {
		return false
	}
	val, exists := labels[labelKey]
	return exists && val == "true"
}

// TODO return error info (summary of errors from resources?) to the caller, instead of just logging
func applyResources(opts Opts, resources ...client.Object) {
	logging.Debug(opts.logger, "reconciling pipeline resources")

	for _, resource := range resources {
		logger := opts.logger.WithValues("type", reflect.TypeOf(resource), "gvk", resource.GetObjectKind().GroupVersionKind().String(), "name", resource.GetName(), "namespace", resource.GetNamespace(), "labels", resource.GetLabels())

		logging.Debug(logger, "reconciling resource")
		if err := opts.client.Create(opts.ctx, resource); err != nil {
			if errors.IsAlreadyExists(err) {
				if resource.GetObjectKind().GroupVersionKind().Kind == rbacv1.ServiceAccountKind {
					serviceAccount := &v1.ServiceAccount{}
					if err := opts.client.Get(opts.ctx, client.ObjectKey{Namespace: resource.GetNamespace(), Name: resource.GetName()}, serviceAccount); err != nil {
						logging.Error(logger, err, "error getting service account")
						continue
					}

					if _, ok := serviceAccount.Labels[v1alpha1.PromiseNameLabel]; !ok {
						logging.Debug(opts.logger, "service account exists but was not created by kratix; skipping update", "name", serviceAccount.GetName(), "namespace", serviceAccount.GetNamespace(), "labels", serviceAccount.GetLabels())
						continue
					}

				}
				logging.Debug(logger, "resource already exists; updating")
				if err = opts.client.Update(opts.ctx, resource); err == nil {
					continue
				}
			}

			logging.Error(logger, err, "error reconciling resource")
			y, _ := yaml.Marshal(&resource)
			logging.Error(logger, err, string(y))
		} else {
			logging.Debug(logger, "resource created")
		}
	}

	time.Sleep(minimumPeriodBetweenCreatingPipelineResources)
}

func deleteResources(opts Opts, resources ...client.Object) {
	for _, resource := range resources {
		logger := opts.logger.WithValues("type", reflect.TypeOf(resource), "gvk", resource.GetObjectKind().GroupVersionKind().String(), "name", resource.GetName(), "namespace", resource.GetNamespace(), "labels", resource.GetLabels())
		logging.Debug(logger, "deleting resource")
		if err := opts.client.Delete(opts.ctx, resource); err != nil {
			if errors.IsNotFound(err) {
				logging.Debug(logger, "resource already deleted")
				continue
			}
			logging.Error(logger, err, "error deleting resource")
			y, _ := yaml.Marshal(&resource)
			logging.Error(logger, err, string(y))
		} else {
			logging.Debug(logger, "resource deleted")
		}
	}
}
