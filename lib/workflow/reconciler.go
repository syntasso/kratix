package workflow

import (
	"context"
	"fmt"
	"reflect"
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
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Opts struct {
	ctx                context.Context
	client             client.Client
	logger             logr.Logger
	parentObject       *unstructured.Unstructured
	Resources          []v1alpha1.PipelineJobResources
	workflowType       string
	numberOfJobsToKeep int
	eventRecorder      events.EventRecorder
	namespace          string

	// WorkflowKey isolates status for controllers that embed the workflow engine.
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

func (o *Opts) workflowKey() string {
	if o.WorkflowKey != "" {
		return o.WorkflowKey
	}
	return string(o.Resources[0].WorkflowAction)
}

func (o *Opts) coreConfigure() bool {
	return o.workflowKey() == "configure" &&
		(o.workflowType == "promise" || o.workflowType == "resource")
}

// ReconcileDelete returns true while the delete pipeline needs further reconciliation.
func ReconcileDelete(opts Opts) (bool, error) {
	if len(opts.Resources) == 0 {
		return false, nil
	}
	if len(opts.Resources) > 1 {
		logging.Warn(opts.logger, "multiple delete pipelines found; only the first will be used")
		opts.Resources = opts.Resources[:1]
	}
	return reconcileWorkflow(opts)
}

// ReconcileConfigure returns true while the configure workflow needs further reconciliation.
func ReconcileConfigure(opts Opts) (bool, error) {
	if len(opts.Resources) == 0 {
		return false, nil
	}
	return reconcileWorkflow(opts)
}

// Progress is everything read from the cluster before deciding what to do
// next. It holds plain values only, so DecideNextAction needs no client.
type Progress struct {
	Pipelines []v1alpha1.WorkflowPipelineStatus
	// MalformedStatus is true when a status entry could not be read, which means
	// status cannot be trusted and the workflow has to start again.
	MalformedStatus bool
	// Jobs are all Jobs for the parent object. They span every pipeline, both
	// workflow actions, and earlier runs.
	Jobs            []batchv1.Job
	ManualReconcile bool
	RunFromStart    bool
	Paused          bool
}

type NextAction string

const (
	WaitForRunningJob   NextAction = "waitForRunningJob"
	SuspendRunningJob   NextAction = "suspendRunningJob"
	StayPaused          NextAction = "stayPaused"
	FinishWorkflow      NextAction = "finishWorkflow"
	FailCurrentPipeline NextAction = "failCurrentPipeline"
	RecordJobOutcome    NextAction = "recordJobOutcome"
	StartPipeline       NextAction = "startPipeline"
)

// Decision is the one thing reconcileWorkflow should do this pass. Only the
// fields named against each action are set; the rest stay nil or false.
type Decision struct {
	Action NextAction
	// Pipeline is set for FailCurrentPipeline, RecordJobOutcome and StartPipeline.
	Pipeline *v1alpha1.PipelineJobResources
	// JobToSuspend is set for SuspendRunningJob only.
	JobToSuspend *batchv1.Job
	// JobToRecord is set for RecordJobOutcome only. It has always finished, but it
	// may have either succeeded or failed; the outcome is read off it later.
	JobToRecord *batchv1.Job
	// Restart is set for StartPipeline only, when every pipeline starts again.
	Restart bool
}

func reconcileWorkflow(opts Opts) (bool, error) {
	if migrated, err := MigrateStatus(opts); migrated || err != nil {
		return migrated, err
	}
	progress, err := readWorkflowProgress(opts)
	if err != nil {
		return false, err
	}

	decision := DecideNextAction(opts, progress)
	switch decision.Action {
	case WaitForRunningJob, StayPaused:
		return true, nil
	case SuspendRunningJob:
		return true, suspendJob(opts.ctx, opts.client, decision.JobToSuspend)
	case FinishWorkflow:
		if opts.Resources[0].WorkflowAction == v1alpha1.WorkflowActionDelete {
			return false, cleanupJobs(opts, opts.namespace)
		}
		return false, cleanup(opts, opts.namespace)
	case FailCurrentPipeline:
		if decision.Pipeline.WorkflowAction == v1alpha1.WorkflowActionDelete {
			return false, ErrDeletePipelineFailed
		}
		return true, cleanupJobs(opts, opts.namespace)
	case RecordJobOutcome:
		return recordPipelineJobOutcome(opts, decision)
	case StartPipeline:
		return createPipeline(opts, decision)
	}
	return false, fmt.Errorf("unhandled workflow decision %q", decision.Action)
}

// readWorkflowProgress does all of the reading. It makes no decisions.
func readWorkflowProgress(opts Opts) (Progress, error) {
	entries, _, err := unstructured.NestedSlice(opts.parentObject.Object,
		"status", "kratix", "workflows", opts.workflowKey(), "pipelines")
	if err != nil {
		return Progress{}, err
	}

	parentLabels := opts.parentObject.GetLabels()
	progress := Progress{
		Pipelines:       make([]v1alpha1.WorkflowPipelineStatus, 0, len(entries)),
		ManualReconcile: isManualReconciliation(parentLabels),
		RunFromStart:    isWorkflowRestart(parentLabels),
		Paused:          hasWorkflowSuspendedLabel(opts.parentObject),
	}
	for _, entry := range entries {
		pipeline, err := pipelineStatusFromUnstructured(entry)
		if err != nil {
			logging.Warn(opts.logger, "could not read workflow pipeline status; the workflow will start again", "error", err)
			progress.MalformedStatus = true
			continue
		}
		progress.Pipelines = append(progress.Pipelines, pipeline)
	}

	if progress.Jobs, err = getJobsWithLabels(opts, labelsForJobs(opts), opts.namespace); err != nil {
		return Progress{}, err
	}
	return progress, nil
}

// pipelineStatusFromUnstructured converts one raw status entry into the type the CRD
// already defines, so the field names are spelled out in exactly one place.
func pipelineStatusFromUnstructured(entry any) (v1alpha1.WorkflowPipelineStatus, error) {
	fields, ok := entry.(map[string]any)
	if !ok {
		return v1alpha1.WorkflowPipelineStatus{}, fmt.Errorf("workflow pipeline status was %T, not an object", entry)
	}
	var pipeline v1alpha1.WorkflowPipelineStatus
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(fields, &pipeline); err != nil {
		return v1alpha1.WorkflowPipelineStatus{}, err
	}
	return pipeline, nil
}

// DecideNextAction picks the single next action from what was read. It is pure, so
// the whole state machine can be read, and tested, in one place.
func DecideNextAction(opts Opts, progress Progress) Decision {
	// Never start anything while a Job is still going. That Job may belong to a
	// different pipeline, a different workflow action, or an earlier run.
	if runningJob := firstUnfinishedJob(progress.Jobs); runningJob != nil {
		if progress.ManualReconcile {
			return Decision{Action: SuspendRunningJob, JobToSuspend: runningJob}
		}
		return Decision{Action: WaitForRunningJob}
	}

	if progress.ManualReconcile || progress.RunFromStart ||
		pipelinesHaveChangedSinceStatusWasWritten(progress, opts.Resources) {
		return Decision{Action: StartPipeline, Pipeline: &opts.Resources[0], Restart: true}
	}

	index, found := firstUnfinishedPipeline(progress.Pipelines)
	if !found {
		return Decision{Action: FinishWorkflow}
	}
	if progress.Paused {
		return Decision{Action: StayPaused}
	}

	pipeline := &opts.Resources[index]
	status := progress.Pipelines[index]

	// The failure is read from status, not from the Job, because it was already
	// recorded on an earlier pass. That Job may since have been pruned, and asking
	// a Job that is no longer there would lose the failure and start the pipeline
	// again. Reading status keeps the pipeline failed until someone changes the
	// spec or asks for a reconciliation.
	if status.Phase == v1alpha1.WorkflowPhaseFailed {
		return Decision{Action: FailCurrentPipeline, Pipeline: pipeline}
	}
	if status.Phase == v1alpha1.WorkflowPhaseRunning {
		if job := findJobByName(progress.Jobs, status.Job); job != nil {
			return Decision{Action: RecordJobOutcome, Pipeline: pipeline, JobToRecord: job}
		}
	}
	return Decision{Action: StartPipeline, Pipeline: pipeline}
}

// pipelinesHaveChangedSinceStatusWasWritten reports that the pipelines we were given
// no longer match what status describes: one was added, removed or renamed, or the
// spec changed so a pipeline that has already started now carries a stale hash. A
// pipeline that has not started yet is allowed a stale hash, because it picks up the
// new one when it runs.
func pipelinesHaveChangedSinceStatusWasWritten(progress Progress, resources []v1alpha1.PipelineJobResources) bool {
	if progress.MalformedStatus || len(progress.Pipelines) != len(resources) {
		return true
	}
	for i, resource := range resources {
		pipeline := progress.Pipelines[i]
		if pipeline.Name != resource.Name {
			return true
		}
		if pipeline.Phase != v1alpha1.WorkflowPhasePending &&
			pipeline.Hash != resource.Job.Labels[v1alpha1.KratixResourceHashLabel] {
			return true
		}
	}
	return false
}

func firstUnfinishedPipeline(pipelines []v1alpha1.WorkflowPipelineStatus) (int, bool) {
	for i, pipeline := range pipelines {
		if pipeline.Phase != v1alpha1.WorkflowPhaseSucceeded {
			return i, true
		}
	}
	return 0, false
}

func firstUnfinishedJob(jobs []batchv1.Job) *batchv1.Job {
	for i := range jobs {
		if isRunning(&jobs[i]) {
			return &jobs[i]
		}
	}
	return nil
}

func findJobByName(jobs []batchv1.Job, name string) *batchv1.Job {
	if name == "" {
		return nil
	}
	for i := range jobs {
		if jobs[i].Name == name {
			return &jobs[i]
		}
	}
	return nil
}

func recordPipelineJobOutcome(opts Opts, decision Decision) (bool, error) {
	pipeline := *decision.Pipeline
	patch := client.MergeFromWithOptions(opts.parentObject.DeepCopy(), client.MergeFromWithOptimisticLock{})
	phase := v1alpha1.WorkflowPhaseSucceeded
	if isFailed(decision.JobToRecord) {
		phase = v1alpha1.WorkflowPhaseFailed
		if opts.coreConfigure() {
			resourceutil.MarkConfigureWorkflowAsFailed(opts.logger, opts.parentObject, pipeline.Name)
			resourceutil.MarkReconciledFailing(opts.parentObject, resourceutil.ConfigureWorkflowCompletedFailedReason)
		}
		opts.eventRecorder.Eventf(opts.parentObject, nil, v1.EventTypeWarning, resourceutil.ConfigureWorkflowCompletedFailedReason, resourceutil.ConfigureWorkflowCompletedFailedReason,
			"A %s/%s Pipeline has failed: %s", opts.workflowType, pipeline.WorkflowAction, pipeline.Name)
	}
	if err := resourceutil.MarkCurrentPipelineAs(phase, opts.parentObject, opts.logger, decision.JobToRecord, opts.workflowKey()); err != nil {
		return false, err
	}
	if err := opts.client.Status().Patch(opts.ctx, opts.parentObject, patch); err != nil {
		return false, err
	}
	if pipeline.WorkflowAction == v1alpha1.WorkflowActionDelete {
		if phase == v1alpha1.WorkflowPhaseFailed {
			return false, ErrDeletePipelineFailed
		}
		return false, cleanupJobs(opts, opts.namespace)
	}
	return true, cleanupJobs(opts, opts.namespace)
}

func suspendJob(ctx context.Context, c client.Client, job *batchv1.Job) error {
	trueBool := true
	patch := client.MergeFrom(job.DeepCopy())
	job.Spec.Suspend = &trueBool
	return c.Patch(ctx, job, patch)
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

func isFailed(job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	for _, condition := range job.Status.Conditions {
		if condition.Status == v1.ConditionTrue && (condition.Type == batchv1.JobFailed || condition.Type == batchv1.JobSuspended) {
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
		if condition.Status == v1.ConditionTrue && (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobSuspended || condition.Type == batchv1.JobFailed) {
			return false
		}
	}
	return true
}

func cleanup(opts Opts, namespace string) error {
	if err := cleanupJobs(opts, namespace); err != nil {
		return err
	}

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

// cleanupJobs prunes finished Jobs for every pipeline the workflow has run,
// including pipelines that have since been renamed or removed.
func cleanupJobs(opts Opts, namespace string) error {
	if len(opts.Resources) == 0 {
		return nil
	}

	jobsForWorkflow, err := getJobsWithLabels(opts, labelsForAllWorkflowJobs(opts.Resources[0]), namespace)
	if err != nil {
		logging.Error(opts.logger, err, "failed to list jobs for workflow")
		return err
	}

	for pipelineName, jobsForPipeline := range groupJobsByPipelineName(jobsForWorkflow) {
		if err := pruneJobs(opts, jobsForPipeline); err != nil {
			logging.Error(opts.logger, err, "failed to delete old jobs", "pipeline", pipelineName)
			return err
		}
	}

	return nil
}

func groupJobsByPipelineName(jobs []batchv1.Job) map[string][]batchv1.Job {
	jobsByPipelineName := map[string][]batchv1.Job{}
	for _, job := range jobs {
		pipelineName := job.GetLabels()[v1alpha1.PipelineNameLabel]
		jobsByPipelineName[pipelineName] = append(jobsByPipelineName[pipelineName], job)
	}
	return jobsByPipelineName
}

func pruneJobs(opts Opts, jobsForPipeline []batchv1.Job) error {
	if len(jobsForPipeline) <= opts.numberOfJobsToKeep {
		logging.Debug(opts.logger,
			"pipeline jobs do not exceed number of jobs to keep",
			"numberOfJobsToKeep", opts.numberOfJobsToKeep,
			"number of pipeline jobs", len(jobsForPipeline))
		return nil
	}

	// Sort jobs by creation time
	jobsForPipeline = resourceutil.SortJobsByCreationDateTime(jobsForPipeline, true)

	// Delete all but the last n jobs; n defaults to 5 and can be configured by env var for the operator
	for i := 0; i < len(jobsForPipeline)-opts.numberOfJobsToKeep; i++ {
		job := jobsForPipeline[i]
		if isRunning(&job) {
			continue
		}
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

func createPipeline(opts Opts, decision Decision) (passiveRequeue bool, err error) {
	resources := *decision.Pipeline
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
	if isWorkflowRestart(opts.parentObject.GetLabels()) {
		if err := removeWorkflowRestartLabel(opts); err != nil {
			return false, err
		}
	}

	if decision.Restart {
		if err := resourceutil.ResetPipelineStatusToPending(opts.parentObject, opts.Resources, opts.workflowKey()); err != nil {
			return false, err
		}
	}

	if err = setPipelineStartingStatus(opts, opts.parentObject, resources.Job); err != nil {
		return false, err
	}

	deleteResources(opts, objectToDelete...)
	applyResources(opts, append(resources.GetObjects(), resources.Job)...)

	action := "Configure"
	if resources.WorkflowAction == v1alpha1.WorkflowActionDelete {
		action = "Delete"
	}
	opts.eventRecorder.Eventf(opts.parentObject, nil, "Normal", "PipelineStarted", "PipelineStarted", "%s Pipeline started: %s", action, resources.Name)

	return true, cleanupJobs(opts, opts.namespace)
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

func setPipelineStartingStatus(opts Opts, obj *unstructured.Unstructured, job *batchv1.Job) error {
	if opts.coreConfigure() {
		resourceutil.SetStatus(obj, opts.logger, "message", "Pending")
		resourceutil.MarkReconciledPending(obj, "WorkflowPending")
		resourceutil.MarkConfigureWorkflowAsRunning(opts.logger, obj)
	}
	if err := resourceutil.MarkCurrentPipelineAsRunning(obj, opts.logger, job, opts.workflowKey()); err != nil {
		return err
	}
	return opts.client.Status().Update(opts.ctx, obj)
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

func hasWorkflowSuspendedLabel(parentObject *unstructured.Unstructured) bool {
	return parentObject.GetLabels()[v1alpha1.WorkflowSuspendedLabel] == "true"
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
