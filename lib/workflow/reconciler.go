package workflow

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/tools/events"

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
	"k8s.io/apimachinery/pkg/types"
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
	eventRecorder      events.EventRecorder
	namespace          string

	// WorkflowKey is set by controllers that run their own workflows with this
	// engine. It is where their pipeline progress is recorded in the object's
	// status, and it keeps them apart from the object's own configure and
	// delete workflows. It defaults to the action of the pipelines being run.
	WorkflowKey string
}

func (o *Opts) SetParentObject(parentObj *unstructured.Unstructured) {
	o.parentObject = parentObj
}

var minimumPeriodBetweenCreatingPipelineResources = 1100 * time.Millisecond
var ErrDeletePipelineFailed = fmt.Errorf("delete Pipeline Failed")

func NewOpts(ctx context.Context, client client.Client, eventRecorder events.EventRecorder, logger logr.Logger, parentObj *unstructured.Unstructured, resources []v1alpha1.PipelineJobResources, workflowType string, numberOfJobsToKeep int, namespace string) Opts {
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
func ReconcileDelete(opts Opts) (passiveRequeue bool, err error) {
	logging.Debug(opts.logger, "reconciling delete pipeline")

	if len(opts.Resources) == 0 {
		return false, nil
	}

	if len(opts.Resources) > 1 {
		logging.Warn(opts.logger, "multiple delete pipelines found; only the first will be used")
		opts.Resources = opts.Resources[:1]
	}

	pipeline := opts.Resources[0]
	manualReconcile := isManualReconciliation(opts.parentObject.GetLabels())

	recorded, err := resourceutil.GetPipelineStatuses(opts.parentObject, workflowKey(opts))
	if err != nil {
		return false, err
	}
	if !recordMatchesPipelines(recorded, opts.Resources) {
		recorded = pendingPipelines(opts.Resources)
	}

	job, err := mostRecentJobForPipeline(opts, pipeline)
	if err != nil {
		return false, err
	}

	if isRunning(job) {
		if manualReconcile {
			logging.Info(opts.logger, "suspending job for manual reconciliation", "job", job.Name, "pipeline", pipeline.Name)
			if err = suspendJob(opts.ctx, opts.client, job); err != nil {
				logging.Error(opts.logger, err, "failed to suspend job", "job", job.GetName())
			}
			opts.eventRecorder.Eventf(opts.parentObject, nil, "Normal", "PipelineSuspended", "PipelineSuspended", "Delete Pipeline suspended: %s", pipeline.Name)
			return true, err
		}

		logging.Debug(opts.logger, "job already running for pipeline; waiting for it to finish", "job", job.Name, "pipeline", pipeline.Name)
		return true, nil
	}

	if jobFailed(job) {
		return false, ErrDeletePipelineFailed
	}

	switch {
	case manualReconcile, recorded[0].Phase == v1alpha1.WorkflowPhaseSuspended:
		// A suspended pipeline asked to be run again later, and the wait is
		// over: keep the attempts and nextRetryAt it recorded.
		return startDeletePipeline(opts, pipeline, recorded, manualReconcile)

	case recorded[0].Phase == v1alpha1.WorkflowPhaseRunning && jobFinished(job):
		if opts.parentObject.GetLabels()[v1alpha1.WorkflowSuspendedLabel] == "true" {
			logging.Info(opts.logger, "delete pipeline completed but workflow is suspended; waiting")
			return true, nil
		}
		logging.Info(opts.logger, "delete pipeline completed")
		markPipelineAsSucceeded(&recorded[0])
		return false, writePipelineStatuses(opts, recorded)

	case recorded[0].Phase == v1alpha1.WorkflowPhaseSucceeded && recorded[0].Hash == runHash(pipeline):
		return false, nil

	default:
		return startDeletePipeline(opts, pipeline, pendingPipelines(opts.Resources), manualReconcile)
	}
}

// startDeletePipeline runs the delete pipeline, once no configure pipeline of
// the same object is still running.
func startDeletePipeline(opts Opts, pipeline v1alpha1.PipelineJobResources,
	recorded []v1alpha1.WorkflowPipelineStatus, manualReconcile bool,
) (passiveRequeue bool, err error) {
	configureLabels := labelsForJobs(opts)
	configureLabels[v1alpha1.WorkflowActionLabel] = string(v1alpha1.WorkflowActionConfigure)
	configureJobs, err := getJobsWithLabels(opts, configureLabels, opts.namespace)
	if err != nil {
		return false, err
	}
	if running := firstRunningJob(configureJobs); running != nil {
		logging.Info(opts.logger, "configure pipeline still running; "+
			"waiting for completion before starting delete pipeline", "runningJob", running.Name)
		return true, nil
	}

	if manualReconcile {
		logging.Info(opts.logger, "manual reconciliation detected for delete pipeline", "pipeline", pipeline.Name)
	}
	return createDeletePipeline(opts, pipeline, recorded)
}

func createDeletePipeline(opts Opts, pipeline v1alpha1.PipelineJobResources,
	recorded []v1alpha1.WorkflowPipelineStatus,
) (passiveRequeue bool, err error) {
	logging.Debug(opts.logger, "creating delete pipeline; execution will commence")
	if isManualReconciliation(opts.parentObject.GetLabels()) {
		if err := removeManualReconciliationLabel(opts); err != nil {
			return false, err
		}
	}

	markPipelineAsRunning(&recorded[0], pipeline)
	if err = writePipelineStatuses(opts, recorded); err != nil {
		return false, err
	}

	//TODO retrieve error information from applyResources to return to the caller
	applyResources(opts, append(pipeline.GetObjects(), pipeline.Job)...)
	opts.eventRecorder.Eventf(opts.parentObject, nil, "Normal", "PipelineStarted", "PipelineStarted", "Delete Pipeline started: %s", pipeline.Name)
	return true, nil
}

// ReconcileConfigure reconciles configure workflows.
// The returned bool is passiveRequeue:
// true means reconcile should happen again, passively, when watched external
// resources are updated (for example workflow Jobs or the parent object status),
// rather than by issuing an explicit direct requeue from this function.
func ReconcileConfigure(opts Opts) (passiveRequeue bool, err error) {
	if len(opts.Resources) == 0 {
		logging.Debug(opts.logger, "no pipeline resources to reconcile")
		return false, nil
	}

	manualReconcile := isManualReconciliation(opts.parentObject.GetLabels())
	runFromStart := manualReconcile || isWorkflowRestart(opts.parentObject.GetLabels())

	recorded, err := resourceutil.GetPipelineStatuses(opts.parentObject, workflowKey(opts))
	if err != nil {
		return false, err
	}
	updated := slices.Clone(recorded)
	if runFromStart || !recordMatchesPipelines(updated, opts.Resources) {
		updated = pendingPipelines(opts.Resources)
		resourceutil.ClearSuspendedGeneration(opts.parentObject, workflowKey(opts))
	}

	jobs, err := getJobsWithLabels(opts, labelsForJobs(opts), opts.namespace)
	if err != nil {
		logging.Error(opts.logger, err, "failed to list jobs")
		return false, err
	}
	resourceutil.SortJobsByCreationDateTime(jobs, false)
	if err := pruneJobs(opts, jobs); err != nil {
		return false, err
	}

	index := advanceToNextPipeline(opts.Resources, updated, jobs)
	if index == len(opts.Resources) {
		logging.Debug(opts.logger, "all pipelines have succeeded")
		if err := writePipelineStatusesIfChanged(opts, recorded, updated); err != nil {
			return false, err
		}
		return false, deleteWorksOfRemovedPipelines(opts)
	}

	pipeline := opts.Resources[index]
	opts.logger = opts.logger.WithName(pipeline.Name).WithValues("isManualReconciliation", manualReconcile)

	if running := firstRunningJob(jobs); running != nil {
		if manualReconcile {
			logging.Info(opts.logger, "suspending job for manual reconciliation", "job", running.Name)
			return true, suspendJob(opts.ctx, opts.client, running)
		}
		logging.Debug(opts.logger, "a job of this workflow is still running; waiting for it to finish", "job", running.Name)
		return true, writePipelineStatusesIfChanged(opts, recorded, updated)
	}

	// Only a run for the object as it is now can fail the workflow: an older
	// failed run is replaced rather than reported again.
	job := jobNamed(jobs, updated[index].Job)
	failedThisRun := updated[index].Hash == runHash(pipeline) &&
		updated[index].Phase != v1alpha1.WorkflowPhaseSuspended &&
		jobFailed(job)
	if !runFromStart && failedThisRun {
		return failWorkflow(opts, updated, index, pipeline, job)
	}

	return startPipeline(opts, updated, index, pipeline)
}

// workflowKey is where this workflow records the progress of its pipelines in
// the parent object's status.
func workflowKey(opts Opts) string {
	if opts.WorkflowKey != "" {
		return opts.WorkflowKey
	}
	return string(opts.Resources[0].WorkflowAction)
}

// ownsObjectConditions keeps a workflow run by another controller from setting
// the message and conditions of an object it does not own.
func ownsObjectConditions(opts Opts) bool {
	return workflowKey(opts) == string(v1alpha1.WorkflowActionConfigure)
}

// runHash combines the hash of the object's spec with the hash of the pipeline
// definition, when the pipeline carries one. A pipeline runs again when it no
// longer matches.
func runHash(pipeline v1alpha1.PipelineJobResources) string {
	jobLabels := pipeline.Job.GetLabels()
	hash := jobLabels[v1alpha1.KratixResourceHashLabel]
	if pipelineHash := jobLabels[v1alpha1.KratixPipelineHashLabel]; pipelineHash != "" {
		hash = fmt.Sprintf("%s-%s", hash, pipelineHash)
	}
	return hash
}

// advanceToNextPipeline records every pipeline whose Job has finished since the
// last reconciliation as succeeded, and returns the index of the first pipeline
// still to run, or len(pipelines) when they have all succeeded for the object
// as it is now.
func advanceToNextPipeline(pipelines []v1alpha1.PipelineJobResources, recorded []v1alpha1.WorkflowPipelineStatus, jobs []batchv1.Job) int {
	for i, pipeline := range pipelines {
		if recorded[i].Phase == v1alpha1.WorkflowPhaseRunning && jobFinished(jobNamed(jobs, recorded[i].Job)) {
			// Keeps the hash the run started with: crediting it with the hash the
			// object carries now would mark a change as done without running it.
			markPipelineAsSucceeded(&recorded[i])
		}

		if recorded[i].Phase == v1alpha1.WorkflowPhaseSucceeded && recorded[i].Hash == runHash(pipeline) {
			continue
		}
		return i
	}
	return len(pipelines)
}

func startPipeline(opts Opts, recorded []v1alpha1.WorkflowPipelineStatus, index int, pipeline v1alpha1.PipelineJobResources) (passiveRequeue bool, err error) {
	logging.Info(opts.logger, "triggering pipeline", "workflowAction", pipeline.WorkflowAction)

	objectsToDelete, err := getObjectsToDelete(opts, pipeline)
	if err != nil {
		return false, err
	}

	if isManualReconciliation(opts.parentObject.GetLabels()) {
		if err := removeManualReconciliationLabel(opts); err != nil {
			return false, err
		}
	}
	if isWorkflowRestart(opts.parentObject.GetLabels()) {
		if err := removeWorkflowRestartLabel(opts); err != nil {
			return false, err
		}
	}

	markPipelineAsRunning(&recorded[index], pipeline)
	if ownsObjectConditions(opts) {
		resourceutil.SetStatus(opts.parentObject, opts.logger, "message", "Pending")
		resourceutil.MarkReconciledPending(opts.parentObject, "WorkflowPending")
		if shouldMarkConfigureWorkflowAsRunning(opts.parentObject) {
			resourceutil.MarkConfigureWorkflowAsRunning(opts.logger, opts.parentObject)
		}
	}
	if err := writePipelineStatuses(opts, recorded); err != nil {
		return false, err
	}

	deleteResources(opts, objectsToDelete...)
	applyResources(opts, append(pipeline.GetObjects(), pipeline.Job)...)

	opts.eventRecorder.Eventf(opts.parentObject, nil, "Normal", "PipelineStarted", "PipelineStarted", "Configure Pipeline started: %s", pipeline.Name)

	return true, nil
}

func failWorkflow(opts Opts, statuses []v1alpha1.WorkflowPipelineStatus, index int,
	pipeline v1alpha1.PipelineJobResources, job *batchv1.Job,
) (passiveRequeue bool, err error) {
	logging.Warn(opts.logger, "pipeline job failed; exiting workflow", "failedJob", job.Name, "pipeline", pipeline.Name)

	if statuses[index].Phase == v1alpha1.WorkflowPhaseFailed && statuses[index].Hash == runHash(pipeline) {
		return true, nil
	}

	statuses[index].Phase = v1alpha1.WorkflowPhaseFailed
	statuses[index].Hash = runHash(pipeline)
	statuses[index].LastTransitionTime = metav1.Now()

	if ownsObjectConditions(opts) {
		resourceutil.MarkConfigureWorkflowAsFailed(opts.logger, opts.parentObject, pipeline.Name)
		resourceutil.MarkReconciledFailing(opts.parentObject, resourceutil.ConfigureWorkflowCompletedFailedReason)
	}
	if err := writePipelineStatuses(opts, statuses); err != nil {
		return false, err
	}

	opts.eventRecorder.Eventf(opts.parentObject, nil, v1.EventTypeWarning, resourceutil.ConfigureWorkflowCompletedFailedReason,
		resourceutil.ConfigureWorkflowCompletedFailedReason, "A %s/configure Pipeline has failed: %s", opts.workflowType, pipeline.Name)

	return true, nil
}

func markPipelineAsSucceeded(recorded *v1alpha1.WorkflowPipelineStatus) {
	recorded.Phase = v1alpha1.WorkflowPhaseSucceeded
	recorded.LastTransitionTime = metav1.Now()
}

func markPipelineAsRunning(recorded *v1alpha1.WorkflowPipelineStatus, pipeline v1alpha1.PipelineJobResources) {
	recorded.Phase = v1alpha1.WorkflowPhaseRunning
	recorded.Hash = runHash(pipeline)
	recorded.Job = pipeline.Job.GetName()
	recorded.Message = ""
	recorded.LastTransitionTime = metav1.Now()
}

func recordMatchesPipelines(recorded []v1alpha1.WorkflowPipelineStatus, pipelines []v1alpha1.PipelineJobResources) bool {
	if len(recorded) != len(pipelines) {
		return false
	}
	for i := range pipelines {
		if recorded[i].Name != pipelines[i].Name {
			return false
		}
	}
	return true
}

func pendingPipelines(pipelines []v1alpha1.PipelineJobResources) []v1alpha1.WorkflowPipelineStatus {
	pending := make([]v1alpha1.WorkflowPipelineStatus, 0, len(pipelines))
	for _, pipeline := range pipelines {
		pending = append(pending, v1alpha1.WorkflowPipelineStatus{
			Name:               pipeline.Name,
			Phase:              v1alpha1.WorkflowPhasePending,
			LastTransitionTime: metav1.Now(),
		})
	}
	return pending
}

// writePipelineStatusesIfChanged patches only this workflow's own part of the
// status. A pipeline writes the object's conditions from inside its Job, and
// writing the whole status back would put the earlier conditions in their
// place.
func writePipelineStatusesIfChanged(opts Opts, recorded, updated []v1alpha1.WorkflowPipelineStatus) error {
	if slices.Equal(recorded, updated) {
		return nil
	}

	patch, err := resourceutil.PipelineStatusesPatch(workflowKey(opts), updated)
	if err != nil {
		return err
	}
	if err := resourceutil.SetPipelineStatuses(opts.parentObject, workflowKey(opts), updated); err != nil {
		return err
	}
	if err := opts.client.Status().Patch(opts.ctx, opts.parentObject, client.RawPatch(types.MergePatchType, patch)); err != nil {
		logging.Error(opts.logger, err, "failed to record pipeline statuses")
		return err
	}
	return nil
}

func writePipelineStatuses(opts Opts, recorded []v1alpha1.WorkflowPipelineStatus) error {
	if err := resourceutil.SetPipelineStatuses(opts.parentObject, workflowKey(opts), recorded); err != nil {
		return err
	}
	if err := opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
		logging.Error(opts.logger, err, "failed to update parent object status")
		return err
	}
	return nil
}

func jobNamed(jobs []batchv1.Job, name string) *batchv1.Job {
	for i := range jobs {
		if jobs[i].GetName() == name {
			return &jobs[i]
		}
	}
	return nil
}

func firstRunningJob(jobs []batchv1.Job) *batchv1.Job {
	for i := range jobs {
		if isRunning(&jobs[i]) {
			return &jobs[i]
		}
	}
	return nil
}

func suspendJob(ctx context.Context, c client.Client, job *batchv1.Job) error {
	trueBool := true
	patch := client.MergeFrom(job.DeepCopy())
	job.Spec.Suspend = &trueBool
	return c.Patch(ctx, job, patch)
}

func getLabelsForPipelineJob(pipeline v1alpha1.PipelineJobResources) map[string]string {
	return pipeline.Job.DeepCopy().GetLabels()
}

// labelsForJobs selects this object's Jobs for the action being reconciled. A
// delete Job running alongside must not look like a configure pipeline still in
// progress.
func labelsForJobs(opts Opts) map[string]string {
	l := map[string]string{
		v1alpha1.WorkflowTypeLabel:   opts.workflowType,
		v1alpha1.WorkflowActionLabel: workflowKey(opts),
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

func jobFinished(job *batchv1.Job) bool {
	return job != nil && !isRunning(job) && !isFailed(job)
}

func jobFailed(job *batchv1.Job) bool {
	return job != nil && isFailed(job)
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

func deleteWorksOfRemovedPipelines(opts Opts) error {
	pipelineNames := map[string]bool{}
	for _, pipeline := range opts.Resources {
		pipelineNames[pipeline.Name] = true
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

// pruneJobs keeps the most recent numberOfJobsToKeep finished Jobs of each
// pipeline in the workflow. A Job that is still running is never deleted: the
// workflow is waiting on it.
func pruneJobs(opts Opts, jobs []batchv1.Job) error {
	for _, pipeline := range opts.Resources {
		if err := pruneJobsOfPipeline(opts, jobs, pipeline); err != nil {
			logging.Error(opts.logger, err, "failed to delete old jobs", "pipeline", pipeline.Name)
			return err
		}
	}
	return nil
}

func pruneJobsOfPipeline(opts Opts, jobs []batchv1.Job, pipeline v1alpha1.PipelineJobResources) error {
	var finished []batchv1.Job
	for _, job := range jobs {
		jobLabels := job.GetLabels()
		if jobLabels[v1alpha1.PipelineNameLabel] != pipeline.Name {
			continue
		}
		if jobLabels[v1alpha1.WorkflowActionLabel] != string(pipeline.WorkflowAction) {
			continue
		}
		if isRunning(&job) {
			continue
		}
		finished = append(finished, job)
	}

	if len(finished) <= opts.numberOfJobsToKeep {
		logging.Debug(opts.logger,
			"pipeline jobs do not exceed number of jobs to keep",
			"numberOfJobsToKeep", opts.numberOfJobsToKeep,
			"number of pipeline jobs", len(finished))
		return nil
	}

	finished = resourceutil.SortJobsByCreationDateTime(finished, true)

	for i := 0; i < len(finished)-opts.numberOfJobsToKeep; i++ {
		job := finished[i]
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

func removeManualReconciliationLabel(opts Opts) error {
	logging.Debug(opts.logger, "manual reconciliation label detected; removing it")
	return removeLabel(opts, resourceutil.ManualReconciliationLabel)
}

func removeWorkflowRestartLabel(opts Opts) error {
	logging.Debug(opts.logger, "workflow restart label detected; removing it")
	return removeLabel(opts, resourceutil.WorkflowRunFromStartLabel)
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

func shouldMarkConfigureWorkflowAsRunning(obj *unstructured.Unstructured) bool {
	condition := resourceutil.GetCondition(obj, resourceutil.ConfigureWorkflowCompletedCondition)
	if condition == nil {
		return true
	}
	if condition.Status != v1.ConditionFalse {
		return true
	}
	return condition.Reason != resourceutil.PipelinesInProgressReason
}

func mostRecentJobForPipeline(opts Opts, pipeline v1alpha1.PipelineJobResources) (*batchv1.Job, error) {
	jobs, err := getJobsWithLabels(opts, getLabelsForPipelineJob(pipeline), opts.namespace)
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

func isWorkflowRestart(labels map[string]string) bool {
	return isLabelSetToTrue(labels, resourceutil.WorkflowRunFromStartLabel)
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
		// Capture the GVK before calling Create: the client clears TypeMeta on
		// typed objects, so resource.GetObjectKind() is empty afterwards.
		gvk := resource.GetObjectKind().GroupVersionKind()
		logger := opts.logger.WithValues("type", reflect.TypeOf(resource), "gvk", gvk.String(), "name", resource.GetName(), "namespace", resource.GetNamespace(), "labels", resource.GetLabels())

		logging.Debug(logger, "reconciling resource")
		if err := opts.client.Create(opts.ctx, resource); err != nil {
			if errors.IsAlreadyExists(err) {
				if gvk.Kind == rbacv1.ServiceAccountKind {
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
