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
	Resources          []v1alpha1.PipelineJobResources
	workflowType       string
	numberOfJobsToKeep int
	eventRecorder      record.EventRecorder
	namespace          string

	// Set by other controllers that use the Workflow engine
	SkipConditions bool
}

func (o *Opts) SetParentObject(parentObj *unstructured.Unstructured) {
	o.parentObject = parentObj
}

var minimumPeriodBetweenCreatingPipelineResources = 1100 * time.Millisecond
var ErrDeletePipelineFailed = fmt.Errorf("delete Pipeline Failed")

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

// ReconcileDelete deletes Workflows.
// The returned bool is passiveRequeue:
// true means reconcile should happen again, passively, when watched external
// resources are updated (for example a workflow Job changing state), rather
// than by issuing an explicit direct requeue from this function.
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

func createDeletePipeline(opts Opts, pipeline v1alpha1.PipelineJobResources) (passiveRequeue bool, err error) {
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

// workflowState holds the resolved state for a configure reconciliation.
type workflowState struct {
	mostRecentJob        *batchv1.Job
	pipelineIndex        int   // index of pipeline to act on (capped to len-1)
	completedCount       int64 // number of pipelines completed, for status sync
	manualReconcile      bool
	desiredFailedCount   *int64
	desiredPipelinePhase string
	desiredPipelineJob   *batchv1.Job
}

// ReconcileConfigure reconciles configure workflows.
// The returned bool is passiveRequeue:
// true means reconcile should happen again, passively, when watched external
// resources are updated (for example workflow Jobs or the parent object status),
// rather than by issuing an explicit direct requeue from this function.
func ReconcileConfigure(opts Opts) (passiveRequeue bool, err error) {
	state, err := determineWorkflowState(opts)
	if err != nil {
		return false, err
	}

	// TODO: do we need this check?
	if state.pipelineIndex < 0 {
		logging.Debug(opts.logger, "no pipeline to reconcile", "index", state.pipelineIndex)
		return false, nil
	}

	if !opts.SkipConditions {
		if requeue, err := reconcileWorkflowStatus(opts, state); err != nil || requeue {
			return requeue, err
		}
	}

	pipeline := opts.Resources[state.pipelineIndex]
	opts.logger = opts.logger.WithName(pipeline.Name).WithValues("isManualReconciliation", state.manualReconcile)

	return executeReconcileAction(opts, state, pipeline)
}

func determineWorkflowState(opts Opts) (*workflowState, error) {
	allJobs, err := getJobsWithLabels(opts, labelsForJobs(opts), opts.namespace)
	if err != nil {
		logging.Error(opts.logger, err, "failed to list jobs")
		return nil, err
	}
	allLegacyJobs, err := getJobsWithLabels(opts, legacyLabelsForJobs(opts), opts.namespace)
	if err != nil {
		logging.Error(opts.logger, err, "failed to list jobs")
		return nil, err
	}
	allJobs = append(allJobs, allLegacyJobs...)

	state := &workflowState{
		manualReconcile: isManualReconciliation(opts.parentObject.GetLabels()),
	}

	if len(allJobs) == 0 {
		state.pipelineIndex = 0
		state.completedCount = 0
		return state, nil
	}

	resourceutil.SortJobsByCreationDateTime(allJobs, false)
	state.mostRecentJob = &allJobs[0]
	logging.Debug(opts.logger, "found existing jobs; most recent job is",
		"name", state.mostRecentJob.GetName(),
		"labels", state.mostRecentJob.Labels,
		"createdTimestamp", state.mostRecentJob.GetCreationTimestamp().Time,
		"status", overAllJobStatus(state.mostRecentJob))

	pipelineIndex, jobIsForPipeline := jobToPipelineIndex(opts, state.mostRecentJob)

	state.completedCount = int64(pipelineIndex)

	if jobIsForPipeline && isCompleted(state.mostRecentJob) {
		state.completedCount++
		if pipelineIndex < len(opts.Resources)-1 {
			pipelineIndex++
		}
	}

	state.pipelineIndex = pipelineIndex
	return state, nil

}

func reconcileWorkflowStatus(opts Opts, state *workflowState) (passiveRequeue bool, err error) {
	currentSucceededCount := max(resourceutil.GetWorkflowsCounterStatus(opts.parentObject, "workflowsSucceeded"), 0)
	currentFailedCount := max(resourceutil.GetWorkflowsCounterStatus(opts.parentObject, "workflowsFailed"), 0)
	succeededCountDrifted := currentSucceededCount != state.completedCount

	shouldResetForManualRetry := state.manualReconcile && currentFailedCount != 0
	failedCountDrifted := state.desiredFailedCount != nil && currentFailedCount != *state.desiredFailedCount
	pipelinePhaseDrifted := state.desiredPipelineJob != nil && state.desiredPipelinePhase != ""

	if !succeededCountDrifted && !shouldResetForManualRetry && !failedCountDrifted && !pipelinePhaseDrifted {
		return false, nil
	}

	if succeededCountDrifted {
		resourceutil.SetStatus(opts.parentObject, opts.logger, "workflowsSucceeded", state.completedCount)
		if state.completedCount > 0 {
			if err = resourceutil.MarkCurrentPipelineAsSucceeded(opts.parentObject, opts.logger, state.mostRecentJob); err != nil {
				logging.Error(opts.logger, err, "failed to mark current pipeline as succeeded")
				return false, err
			}
		}
	}

	if shouldResetForManualRetry || (succeededCountDrifted && state.completedCount == 0) {
		resourceutil.SetStatus(opts.parentObject, opts.logger, "workflowsFailed", int64(0))
		if err = resourceutil.ResetPipelineStatusToPending(opts.parentObject, opts.logger, pipelineNames(opts.Resources)); err != nil {
			return false, err
		}
	}

	if failedCountDrifted {
		resourceutil.SetStatus(opts.parentObject, opts.logger, "workflowsFailed", *state.desiredFailedCount)
	}

	if pipelinePhaseDrifted {
		if err = resourceutil.MarkCurrentPipelineAs(state.desiredPipelinePhase, opts.parentObject, opts.logger, state.desiredPipelineJob); err != nil {
			logging.Error(opts.logger, err, "failed to mark current pipeline as "+state.desiredPipelinePhase)
			return false, err
		}
	}

	if err = opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
		logging.Error(opts.logger, err, "failed to update parent object status")
		return false, err
	}
	return true, nil
}

func pipelineNames(resources []v1alpha1.PipelineJobResources) []string {
	names := make([]string, len(resources))
	for i, r := range resources {
		names[i] = r.Name
	}
	return names
}

func executeReconcileAction(opts Opts, state *workflowState, pipeline v1alpha1.PipelineJobResources) (passiveRequeue bool, err error) {
	if jobIsForPipeline(pipeline, state.mostRecentJob) {
		return handleCurrentPipelineJob(opts, state, pipeline)
	}

	if isRunning(state.mostRecentJob) {
		logging.Info(opts.logger, "job already inflight for another workflow; suspending it", "job", state.mostRecentJob.Name)
		err = suspendJob(opts.ctx, opts.client, state.mostRecentJob)
		if err != nil {
			logging.Error(opts.logger, err, "failed to suspend job", "job", state.mostRecentJob.GetName())
		}
		return true, nil
	}

	return createConfigurePipeline(opts, state, pipeline)
}

func handleCurrentPipelineJob(opts Opts, state *workflowState, pipeline v1alpha1.PipelineJobResources) (passiveRequeue bool, err error) {
	logging.Debug(opts.logger, "job is for pipeline", "job", state.mostRecentJob.Name, "pipeline", pipeline.Name)

	if isRunning(state.mostRecentJob) {
		if state.manualReconcile {
			logging.Info(opts.logger, "suspending job for manual reconciliation", "job", state.mostRecentJob.Name, "pipeline", pipeline.Name)
			if err = suspendJob(opts.ctx, opts.client, state.mostRecentJob); err != nil {
				logging.Error(opts.logger, err, "failed to suspend job", "job", state.mostRecentJob.GetName())
			}
			return true, err
		}
		logging.Debug(opts.logger, "job already inflight for pipeline; waiting for completion", "job", state.mostRecentJob.Name, "pipeline", pipeline.Name)
		return true, nil
	}

	if state.manualReconcile {
		logging.Info(opts.logger, "pipeline running due to manual reconciliation", "pipeline", pipeline.Name, "parentLabels", opts.parentObject.GetLabels())
		return createConfigurePipeline(opts, state, pipeline)
	}

	if isFailed(state.mostRecentJob) {
		logging.Debug(opts.logger, "job failed", "job", state.mostRecentJob.Name, "pipeline", pipeline.Name)
		return setFailedConditionAndEvents(opts, state, pipeline)
	}

	return false, cleanup(opts, opts.namespace)
}

func setFailedConditionAndEvents(opts Opts, state *workflowState, pipeline v1alpha1.PipelineJobResources) (bool, error) {
	if !opts.SkipConditions {
		resourceutil.MarkConfigureWorkflowAsFailed(opts.logger, opts.parentObject, pipeline.Name)
		resourceutil.MarkReconciledFailing(opts.parentObject, resourceutil.ConfigureWorkflowCompletedFailedReason)

		failedCount := int64(1)
		state.desiredFailedCount = &failedCount
		state.desiredPipelinePhase = string(v1alpha1.WorkflowPhaseFailed)
		state.desiredPipelineJob = state.mostRecentJob

		if _, err := reconcileWorkflowStatus(opts, state); err != nil {
			return false, err
		}
	}
	opts.eventRecorder.Eventf(opts.parentObject, v1.EventTypeWarning,
		resourceutil.ConfigureWorkflowCompletedFailedReason, "A %s/configure Pipeline has failed: %s", opts.workflowType, pipeline.Name)
	logging.Warn(opts.logger, "pipeline job failed; exiting workflow", "failedJob", state.mostRecentJob.Name, "pipeline", pipeline.Name)
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

func labelsForAllWorkflowJobs(pipeline v1alpha1.PipelineJobResources) map[string]string {
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
	if pipelineLabels[v1alpha1.WorkflowActionLabel] != "" {
		labels[v1alpha1.WorkflowActionLabel] = pipelineLabels[v1alpha1.WorkflowActionLabel]
	}
	if pipelineLabels[v1alpha1.WorkflowTypeLabel] != "" {
		labels[v1alpha1.WorkflowTypeLabel] = pipelineLabels[v1alpha1.WorkflowTypeLabel]
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

// Bool indicates wether the job belongs to the pipeline, or is unrelated
func jobToPipelineIndex(opts Opts, mostRecentJob *batchv1.Job) (int, bool) {
	if mostRecentJob == nil || isManualReconciliation(opts.parentObject.GetLabels()) {
		return 0, false
	}

	for i := 0; i < len(opts.Resources); i++ {
		if jobIsForPipeline(opts.Resources[i], mostRecentJob) {
			return i, true
		}
	}

	return 0, false
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

func isCompleted(job *batchv1.Job) bool {
	return !isRunning(job) && !isFailed(job)
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
		l := labelsForAllWorkflowJobs(pipeline)
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
		logging.Debug(opts.logger,
			"pipeline jobs at current spec do not exceed number of jobs to keep",
			"numberOfJobsToKeep", opts.numberOfJobsToKeep,
			"number of pipeline jobs at current spec", len(pipelineJobsAtCurrentSpec))
		return nil
	}

	// Sort jobs by creation time
	pipelineJobsAtCurrentSpec = resourceutil.SortJobsByCreationDateTime(pipelineJobsAtCurrentSpec, true)

	// Delete all but the last n jobs; n defaults to 5 and can be configured by env var for the operator
	for i := 0; i < len(pipelineJobsAtCurrentSpec)-opts.numberOfJobsToKeep; i++ {
		job := pipelineJobsAtCurrentSpec[i]
		logging.Debug(opts.logger,
			"deleting old job",
			"name", job.GetName(),
			"labels", job.GetLabels(),
			"createdTimestamp", job.GetCreationTimestamp().Time,
			"status", overAllJobStatus(&job))
		if err := opts.client.Delete(opts.ctx, &job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
			if !errors.IsNotFound(err) {
				logging.Warn(opts.logger, "failed to delete job; will retry", "job", job.GetName(), "error", err)
				return nil
			}
		}
	}

	return nil
}

func createConfigurePipeline(opts Opts, state *workflowState, resources v1alpha1.PipelineJobResources) (passiveRequeue bool, err error) {
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

	updated, err := setConfigureWorkflowCompletedConditionStatus(opts, state.pipelineIndex, opts.parentObject)
	if err != nil || updated {
		return updated, err
	}

	state.desiredPipelinePhase = string(v1alpha1.WorkflowPhaseRunning)
	state.desiredPipelineJob = resources.Job
	if updated, err = reconcileWorkflowStatus(opts, state); err != nil || updated {
		return updated, err
	}

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

func setConfigureWorkflowCompletedConditionStatus(opts Opts, pipelineIndex int, obj *unstructured.Unstructured) (bool, error) {
	if opts.SkipConditions {
		return false, nil
	}
	var updated bool
	switch resourceutil.GetConfigureWorkflowCompletedConditionStatus(obj) {
	case v1.ConditionTrue:
		fallthrough
	case v1.ConditionUnknown:
		currentMessage := resourceutil.GetStatus(obj, "message")
		if pipelineIndex == 0 || currentMessage == "" || currentMessage == "Resource requested" {
			resourceutil.SetStatus(obj, opts.logger, "message", "Pending")
		}
		resourceutil.MarkConfigureWorkflowAsRunning(opts.logger, obj)
		resourceutil.MarkReconciledPending(obj, "WorkflowPending")
		updated = true
	default:
		updated = false
	}

	if updated {
		logging.Info(opts.logger, "setting pipeline execution status", "pipelineIndex", pipelineIndex, "phase", v1alpha1.WorkflowPhaseRunning)
		if err := opts.client.Status().Update(opts.ctx, obj); err != nil {
			logging.Error(opts.logger, err, "failed to update object status")
			return false, err
		}
	}
	return updated, nil
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

// overAllJobStatus returns job status as 'Running', 'Completed', 'Suspended', or 'Failed'
func overAllJobStatus(job *batchv1.Job) string {
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == v1.ConditionTrue {
			return string(condition.Type)
		}

		if condition.Type == batchv1.JobSuspended && condition.Status == v1.ConditionTrue {
			return string(condition.Type)
		}

		if condition.Type == batchv1.JobFailed && condition.Status == v1.ConditionTrue {
			return string(condition.Type)
		}
	}
	return "Running"
}
