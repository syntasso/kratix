package workflow

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
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

	// WorkflowKey is the key under status.kratix.workflows that this workflow's
	// pipeline statuses are stored at. Leave it empty and Kratix's own keys are
	// used, one per workflow action. A controller that embeds the workflow
	// engine has to set it: without a key of its own, its reset of "the"
	// pipeline status wipes the entries Kratix's configure and delete workflows
	// are progressing through.
	WorkflowKey string
}

func (o *Opts) SetParentObject(parentObj *unstructured.Unstructured) {
	o.parentObject = parentObj
}

func (o *Opts) statusKey(action v1alpha1.Action) string {
	if o.WorkflowKey != "" {
		return o.WorkflowKey
	}
	return string(action)
}

// workflowKeyPattern is narrower than "any map key" because the key becomes a
// segment of a status path: a dot, a slash or a leading dash in it turns a
// working jsonpath query into a silently empty one.
var workflowKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9]([-_a-zA-Z0-9]*[a-zA-Z0-9])?$`)

func (o *Opts) validateWorkflowKey() error {
	if o.WorkflowKey == "" {
		return nil
	}
	for _, reserved := range []v1alpha1.Action{v1alpha1.WorkflowActionConfigure, v1alpha1.WorkflowActionDelete} {
		if o.WorkflowKey == string(reserved) {
			return fmt.Errorf("workflow key %q is reserved for Kratix's own %s workflow", o.WorkflowKey, reserved)
		}
	}
	if len(o.WorkflowKey) > 63 || !workflowKeyPattern.MatchString(o.WorkflowKey) {
		return fmt.Errorf("workflow key %q is not a valid status path segment: "+
			"use up to 63 alphanumerics, dashes or underscores, starting and ending with an alphanumeric", o.WorkflowKey)
	}
	return nil
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
func ReconcileDelete(opts Opts) (bool, error) {
	logging.Debug(opts.logger, "reconciling delete pipeline")

	if len(opts.Resources) == 0 {
		return false, nil
	}

	if err := opts.validateWorkflowKey(); err != nil {
		return false, err
	}
	deleteKey := opts.statusKey(v1alpha1.WorkflowActionDelete)

	if changed, err := migrateWorkflowStatus(opts, deleteKey, v1alpha1.WorkflowActionDelete); err != nil || changed {
		if changed && err == nil {
			err = opts.client.Status().Update(opts.ctx, opts.parentObject)
		}
		return changed, err
	}

	if len(opts.Resources) > 1 {
		logging.Warn(opts.logger, "multiple delete pipelines found; only the first will be used")
	}
	opts.Resources = opts.Resources[:1]
	pipeline := opts.Resources[0]

	evidence, err := jobEvidence(opts, v1alpha1.WorkflowActionDelete)
	if err != nil {
		return false, err
	}
	job := evidence[pipeline.Name]

	manualReconcile := isManualReconciliation(opts.parentObject.GetLabels())
	if manualReconcile {
		logging.Info(opts.logger, "manual reconciliation detected for delete pipeline", "pipeline", pipeline.Name)
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

		logging.Debug(opts.logger, "job already inflight for pipeline; waiting for completion", "job", job.Name, "pipeline", pipeline.Name)
		return true, nil
	}

	if manualReconcile {
		return createDeletePipelineWhenConfigureIdle(opts, deleteKey, pipeline, resetAll)
	}

	statuses, err := deletePipelineStatuses(opts, deleteKey)
	if err != nil {
		return false, err
	}
	phase := statuses.phase(pipeline.Name)
	workflowSuspended := opts.parentObject.GetLabels()[v1alpha1.WorkflowSuspendedLabel] == "true"

	if phase == v1alpha1.WorkflowPhaseSuspended {
		if workflowSuspended {
			logging.Info(opts.logger, "delete pipeline suspended; waiting")
			return true, nil
		}
		return createDeletePipeline(opts, deleteKey, resetNone)
	}

	if phase == v1alpha1.WorkflowPhaseSucceeded && statuses.hash(pipeline.Name) == desiredPipelineHash(pipeline) {
		if workflowSuspended {
			logging.Info(opts.logger, "delete pipeline completed but workflow is suspended; waiting")
			return true, nil
		}
		logging.Info(opts.logger, "delete pipeline completed")
		return false, nil
	}

	logging.Debug(opts.logger, "checking status of delete pipeline")
	switch {
	case !jobIsForPipeline(pipeline, job):
		return createDeletePipelineWhenConfigureIdle(opts, deleteKey, pipeline, resetAll)
	case isFailed(job):
		return false, ErrDeletePipelineFailed
	default:
		if err = resourceutil.MarkCurrentPipelineAsSucceeded(opts.parentObject, deleteKey, opts.logger, job); err != nil {
			return false, err
		}
		if err = opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
			return false, err
		}
		return true, nil
	}
}

// deletePipelineStatuses reads the delete workflow's pipeline statuses, seeding
// the single entry on the object — but not persisting it — when there is none.
func deletePipelineStatuses(opts Opts, key string) (pipelineStatuses, error) {
	entries, _, err := resourceutil.GetPipelineStatuses(opts.parentObject, key)
	if err != nil {
		return pipelineStatuses{}, err
	}

	statuses := newPipelineStatuses(entries)
	if _, found := statuses.byName[opts.Resources[0].Name]; found {
		return statuses, nil
	}

	entries = []any{newPendingEntry(opts.Resources[0].Name)}
	if err := resourceutil.SetPipelineStatuses(opts.parentObject, key, entries); err != nil {
		return pipelineStatuses{}, err
	}
	return newPipelineStatuses(entries), nil
}

// createDeletePipelineWhenConfigureIdle holds the delete pipeline back until no
// configure Job is running, so the two lanes never write the same Works at once.
func createDeletePipelineWhenConfigureIdle(opts Opts, key string, pipeline v1alpha1.PipelineJobResources, reset statusReset) (bool, error) {
	configureJobs, err := getJobsWithLabels(opts, labelsForWorkflowJobs(opts, v1alpha1.WorkflowActionConfigure), opts.namespace)
	if err != nil {
		return false, err
	}
	for i := range configureJobs {
		if isRunning(&configureJobs[i]) {
			logging.Info(opts.logger, "configure pipeline still running; "+
				"waiting for completion before starting delete pipeline",
				"runningJob", configureJobs[i].Name, "pipeline", pipeline.Name)
			return true, nil
		}
	}
	return createDeletePipeline(opts, key, reset)
}

func createDeletePipeline(opts Opts, key string, reset statusReset) (passiveRequeue bool, err error) {
	pipeline := opts.Resources[0]
	logging.Debug(opts.logger, "creating delete pipeline; execution will commence")
	if isManualReconciliation(opts.parentObject.GetLabels()) {
		if err := removeManualReconciliationLabel(opts); err != nil {
			return false, err
		}
	}
	if reset == resetAll {
		if err = resourceutil.ResetPipelineStatusToPending(opts.parentObject, key, opts.Resources); err != nil {
			return false, err
		}
	}
	if err = resourceutil.MarkCurrentPipelineAsRunning(opts.parentObject, key, opts.logger, pipeline.Job); err != nil {
		return false, err
	}
	if err = opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
		return false, err
	}
	//TODO retrieve error information from applyResources to return to the caller
	applyResources(opts, append(pipeline.GetObjects(), pipeline.Job)...)
	opts.eventRecorder.Eventf(opts.parentObject, nil, "Normal", "PipelineStarted", "PipelineStarted", "Delete Pipeline started: %s", pipeline.Name)
	return true, nil
}

// pipelineStatuses is the record of how far a workflow has got: one entry per
// pipeline, read from status.kratix.workflows.<key>.pipelines, or built in
// memory from Job evidence for callers that own the parent object's status.
//
// Entries are looked up by name, never by position: pipelines can be added,
// removed or reordered between reconciliations, and matching by index then
// reads one pipeline's history as another's.
type pipelineStatuses struct {
	byName map[string]map[string]any
}

func newPipelineStatuses(entries []any) pipelineStatuses {
	statuses := pipelineStatuses{byName: map[string]map[string]any{}}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := entry["name"].(string); name != "" {
			statuses.byName[name] = entry
		}
	}
	return statuses
}

func (l pipelineStatuses) phase(name string) string {
	phase, _ := l.byName[name]["phase"].(string)
	return phase
}

func (l pipelineStatuses) hash(name string) string {
	hash, _ := l.byName[name]["hash"].(string)
	return hash
}

func newPendingEntry(name string) map[string]any {
	return map[string]any{
		"name":               name,
		"phase":              v1alpha1.WorkflowPhasePending,
		"lastTransitionTime": metav1.Now().Format(time.RFC3339),
	}
}

// desiredPipelineHash is the kratix.io/hash the pipeline's Job would carry if it
// ran now. The Job factory folds the pipeline's own hash into it, so this one
// value covers a spec change and a pipeline-definition change alike.
func desiredPipelineHash(pipeline v1alpha1.PipelineJobResources) string {
	return pipeline.Job.GetLabels()[v1alpha1.KratixResourceHashLabel]
}

// statusReset says what a pipeline creation does to the other pipelines' entries.
type statusReset int

const (
	// resetNone leaves every other entry alone: used when resuming a suspended
	// pipeline, whose own entry carries the retry bookkeeping to preserve.
	resetNone statusReset = iota
	// resetFollowing returns the entries after the pipeline being run to Pending.
	resetFollowing
	// resetAll returns every entry to Pending: a manual reconciliation or a
	// restart-from-start runs the workflow again from the top.
	resetAll
)

// stepKind is what this reconciliation of the workflow should do.
type stepKind int

const (
	stepComplete      stepKind = iota // every pipeline is settled
	stepWait                          // nothing to do until something else changes
	stepRun                           // create the pipeline at index
	stepRecordSuccess                 // job proves the pipeline at index succeeded
	stepRecordFailure                 // job proves the pipeline at index failed
)

type workflowStep struct {
	kind  stepKind
	index int
	job   *batchv1.Job
}

// ReconcileConfigure reconciles configure workflows.
//
// The returned bool is passiveRequeue:
// true means reconcile should happen again, passively, when watched external
// resources are updated (for example workflow Jobs or the parent object status),
// rather than by issuing an explicit direct requeue from this function.
//
// Invariant: every path here that writes the parent object's status returns
// passiveRequeue=true. The promise controller Status().Updates its own typed
// copy of the parent once this returns false, and that copy predates anything
// written here — so a status write followed by false makes the controller
// conflict with the write this function just made.
func ReconcileConfigure(opts Opts) (passiveRequeue bool, err error) {
	if len(opts.Resources) == 0 {
		logging.Debug(opts.logger, "no pipeline resources to reconcile")
		return false, nil
	}

	if err := opts.validateWorkflowKey(); err != nil {
		return false, err
	}
	configureKey := opts.statusKey(v1alpha1.WorkflowActionConfigure)

	if changed, err := migrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure); err != nil || changed {
		if changed && err == nil {
			err = opts.client.Status().Update(opts.ctx, opts.parentObject)
		}
		return changed, err
	}

	evidence, err := jobEvidence(opts, v1alpha1.WorkflowActionConfigure)
	if err != nil {
		return false, err
	}

	statuses, changed, err := seedAndPrunePipelineStatuses(opts, configureKey)
	if err != nil {
		return false, err
	}
	if changed {
		if err = opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
			logging.Error(opts.logger, err, "failed to update parent object status")
			return false, err
		}
		return true, nil
	}

	if requeue, handled, err := reconcileWorkflowLabels(opts, configureKey, evidence); handled {
		return requeue, err
	}

	return runWorkflowStep(opts, configureKey, statuses, evidence)
}

// reconcileWorkflowLabels handles the three label-driven flows — manual
// reconciliation, restart-from-start and resume-from-suspended — and reports
// whether it took the reconciliation.
//
// It has to run before any Job evidence is interpreted: isFailed() counts a
// suspended Job as failed and a manual reconciliation suspends the running Job,
// so reading the evidence first records Failed on every re-run by hand — and
// that entry then halts the run the label asked for.
func reconcileWorkflowLabels(opts Opts, key string, evidence map[string]*batchv1.Job) (passiveRequeue, handled bool, err error) {
	manualReconcile := isManualReconciliation(opts.parentObject.GetLabels())
	restartFromStart := isWorkflowRestart(opts.parentObject.GetLabels())

	if manualReconcile || restartFromStart {
		if job := mostRecentRunningJob(opts, evidence); job != nil {
			if manualReconcile {
				logging.Info(opts.logger, "suspending job for manual reconciliation", "job", job.Name)
				if err = suspendJob(opts.ctx, opts.client, job); err != nil {
					logging.Error(opts.logger, err, "failed to suspend job", "job", job.GetName())
				}
				return true, true, err
			}
			logging.Info(opts.logger, "job already inflight; waiting for completion before restarting the workflow", "job", job.Name)
			return true, true, nil
		}
		logging.Info(opts.logger, "running the workflow from its first pipeline",
			"isManualReconciliation", manualReconcile, "restartFromStart", restartFromStart,
			"parentLabels", opts.parentObject.GetLabels())
		requeue, err := createConfigurePipeline(opts, key, 0, resetAll)
		return requeue, true, err
	}

	suspendedIdx, err := resourceutil.GetSuspendedPipelineIndex(opts.parentObject, key)
	if err != nil {
		return false, true, err
	}
	workflowSuspended := opts.parentObject.GetLabels()[v1alpha1.WorkflowSuspendedLabel] == "true"
	if workflowSuspended || suspendedIdx < 0 || suspendedIdx >= len(opts.Resources) {
		return false, false, nil
	}

	pipeline := opts.Resources[suspendedIdx]
	// Any Job of this workflow holds the resume back, not just this pipeline's
	// own: a second live Job has two pipelines writing the same Works at once.
	if job := mostRecentRunningJob(opts, evidence); job != nil {
		logging.Debug(opts.logger, "job already inflight for this workflow; waiting for completion before resuming the suspended pipeline",
			"job", job.Name, "pipeline", pipeline.Name)
		return true, true, nil
	}

	logging.Info(opts.logger, fmt.Sprintf("rerunning suspended pipeline after %q is removed",
		v1alpha1.WorkflowSuspendedLabel), "pipeline", pipeline.Name)
	requeue, err := createConfigurePipeline(opts, key, suspendedIdx, resetNone)
	if err != nil {
		return false, true, err
	}
	return requeue, true, cleanup(opts, opts.namespace)
}

// nextWorkflowStep walks the pipelines in order and stops at the first one that
// is not settled. "Settled" is the entry saying Succeeded at exactly the hash
// the pipeline would run with now: a Succeeded entry recorded against an older
// definition is a pipeline that still has to run.
func nextWorkflowStep(opts Opts, statuses pipelineStatuses, evidence map[string]*batchv1.Job) workflowStep {
	for i, pipeline := range opts.Resources {
		desired := desiredPipelineHash(pipeline)
		phase := statuses.phase(pipeline.Name)

		if phase == v1alpha1.WorkflowPhaseSucceeded && statuses.hash(pipeline.Name) == desired {
			continue
		}

		if phase == v1alpha1.WorkflowPhaseSuspended {
			logging.Debug(opts.logger, "pipeline is suspended; waiting", "pipeline", pipeline.Name)
			return workflowStep{kind: stepWait, index: i}
		}

		if phase == v1alpha1.WorkflowPhaseFailed && statuses.hash(pipeline.Name) == desired {
			logging.Debug(opts.logger, "pipeline failed at the current definition; waiting for a re-run to be requested", "pipeline", pipeline.Name)
			return workflowStep{kind: stepWait, index: i}
		}

		job := evidence[pipeline.Name]
		switch {
		case isRunning(job):
			logging.Debug(opts.logger, "job already inflight for pipeline; waiting for completion", "job", job.Name, "pipeline", pipeline.Name)
			return workflowStep{kind: stepWait, index: i, job: job}
		case phase == v1alpha1.WorkflowPhasePending || phase == "":
			return runOrWaitForWorkflow(opts, evidence, i)
		case !jobIsForPipeline(pipeline, job):
			return runOrWaitForWorkflow(opts, evidence, i)
		case isFailed(job):
			return workflowStep{kind: stepRecordFailure, index: i, job: job}
		default:
			return workflowStep{kind: stepRecordSuccess, index: i, job: job}
		}
	}
	return workflowStep{kind: stepComplete}
}

// runOrWaitForWorkflow runs the pipeline at index unless any pipeline of this
// workflow still has a Job in flight.
//
// After a second spec edit the first unsettled pipeline is one *behind* the
// pipeline whose Job is running. Starting it there gives the parent two Jobs
// writing its Works at once, and the later Job's in-Job status writer then
// marks the whole workflow completed while the new one is still going.
func runOrWaitForWorkflow(opts Opts, evidence map[string]*batchv1.Job, index int) workflowStep {
	if job := mostRecentRunningJob(opts, evidence); job != nil {
		logging.Debug(opts.logger, "job already inflight for another pipeline of this workflow; waiting for completion",
			"job", job.Name, "pipeline", opts.Resources[index].Name)
		return workflowStep{kind: stepWait, index: index, job: job}
	}
	return workflowStep{kind: stepRun, index: index}
}

func runWorkflowStep(opts Opts, key string, statuses pipelineStatuses, evidence map[string]*batchv1.Job) (passiveRequeue bool, err error) {
	step := nextWorkflowStep(opts, statuses, evidence)

	if step.kind != stepComplete {
		pipeline := opts.Resources[step.index]
		opts.logger = opts.logger.WithName(pipeline.Name)
	}

	switch step.kind {
	case stepComplete:
		logging.Debug(opts.logger, "all pipelines in the workflow are complete")
		return false, cleanup(opts, opts.namespace)

	case stepWait:
		return true, nil

	case stepRecordSuccess:
		if err = resourceutil.MarkCurrentPipelineAsSucceeded(opts.parentObject, key, opts.logger, step.job); err != nil {
			logging.Error(opts.logger, err, "failed to mark pipeline as succeeded")
			return false, err
		}
		if err = opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
			logging.Error(opts.logger, err, "failed to update parent object status")
			return false, err
		}
		return true, nil

	case stepRecordFailure:
		return recordPipelineFailure(opts, key, step)

	default: // stepRun
		return createConfigurePipeline(opts, key, step.index, resetFollowing)
	}
}

func recordPipelineFailure(opts Opts, key string, step workflowStep) (bool, error) {
	pipeline := opts.Resources[step.index]
	logging.Debug(opts.logger, "job failed", "job", step.job.Name, "pipeline", pipeline.Name)

	resourceutil.MarkConfigureWorkflowAsFailed(opts.logger, opts.parentObject, pipeline.Name)
	resourceutil.MarkReconciledFailing(opts.parentObject, resourceutil.ConfigureWorkflowCompletedFailedReason)
	if err := resourceutil.MarkCurrentPipelineAsFailed(opts.parentObject, key, opts.logger, step.job); err != nil {
		logging.Error(opts.logger, err, "failed to mark pipeline as failed")
		return false, err
	}
	if err := opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
		logging.Error(opts.logger, err, "failed to update parent object status")
		return false, err
	}

	opts.eventRecorder.Eventf(opts.parentObject, nil, v1.EventTypeWarning, resourceutil.ConfigureWorkflowCompletedFailedReason, resourceutil.ConfigureWorkflowCompletedFailedReason, "A %s/configure Pipeline has failed: %s", opts.workflowType, pipeline.Name)
	logging.Warn(opts.logger, "pipeline job failed; exiting workflow", "failedJob", step.job.Name, "pipeline", pipeline.Name)

	// Jobs only, not the full cleanup(): the Works of a workflow that has not
	// completed are still wanted.
	return true, cleanupJobs(opts, opts.namespace)
}

// seedAndPrunePipelineStatuses reconciles the entries under key with the
// workflow's current pipelines: entries whose pipeline no longer exists are
// dropped, pipelines with no entry are appended as Pending, and the entries are
// put back into the workflow's own order.
//
// The stored entries and the workflow have to describe the same pipelines before
// anything reads either: a stale Succeeded entry counts towards completion for a
// pipeline nobody runs, and a pipeline with no entry makes every later mark fail
// with "no pipeline found for job".
func seedAndPrunePipelineStatuses(opts Opts, key string) (pipelineStatuses, bool, error) {
	stored, _, err := resourceutil.GetPipelineStatuses(opts.parentObject, key)
	if err != nil {
		return pipelineStatuses{}, false, err
	}
	existing := newPipelineStatuses(stored)

	changed := len(stored) != len(opts.Resources)
	entries := make([]any, 0, len(opts.Resources))
	for i, pipeline := range opts.Resources {
		entry, found := existing.byName[pipeline.Name]
		if !found {
			entry = newPendingEntry(pipeline.Name)
			changed = true
		} else if i >= len(stored) {
			changed = true
		} else if stored[i] == nil || !sameName(stored[i], pipeline.Name) {
			changed = true
		}
		entries = append(entries, entry)
	}

	if !changed {
		return existing, false, nil
	}

	logging.Info(opts.logger, "reconciling the workflow pipeline status with the workflow's pipelines", "key", key)
	if err := resourceutil.SetPipelineStatuses(opts.parentObject, key, entries); err != nil {
		return pipelineStatuses{}, false, err
	}
	return newPipelineStatuses(entries), true, nil
}

func sameName(raw any, name string) bool {
	entry, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	stored, _ := entry["name"].(string)
	return stored == name
}

// jobEvidence lists this workflow's Jobs once and indexes the most recent one
// per pipeline name.
//
// It selects on the labels Kratix writes today, so a Job carrying only the
// retired kratix.io/work-* labels is no evidence at all (#360). Re-adding a
// legacy selector resurrects the ambiguity keying removes: such a Job describes
// a run the pipeline statuses cannot account for.
func jobEvidence(opts Opts, action v1alpha1.Action) (map[string]*batchv1.Job, error) {
	jobs, err := getJobsWithLabels(opts, labelsForWorkflowJobs(opts, action), opts.namespace)
	if err != nil {
		logging.Error(opts.logger, err, "failed to list jobs")
		return nil, err
	}

	// Newest first, so the first Job seen for a pipeline is the one that
	// describes its latest run.
	resourceutil.SortJobsByCreationDateTime(jobs, false)

	evidence := make(map[string]*batchv1.Job, len(jobs))
	for i := range jobs {
		name := jobs[i].GetLabels()[v1alpha1.PipelineNameLabel]
		if name == "" {
			continue
		}
		if _, seen := evidence[name]; !seen {
			evidence[name] = &jobs[i]
		}
	}
	return evidence, nil
}

func mostRecentRunningJob(opts Opts, evidence map[string]*batchv1.Job) *batchv1.Job {
	var newest *batchv1.Job
	for _, pipeline := range opts.Resources {
		job := evidence[pipeline.Name]
		if !isRunning(job) {
			continue
		}
		if newest == nil || job.GetCreationTimestamp().After(newest.GetCreationTimestamp().Time) {
			newest = job
		}
	}
	return newest
}

// resetPipelinesAfter returns the entries after index to Pending. Their hashes
// go with them: left in place, the workflow declares itself finished the moment
// the pipeline being re-run succeeds, without the later ones running at all.
func resetPipelinesAfter(opts Opts, key string, index int) (bool, error) {
	entries, found, err := resourceutil.GetPipelineStatuses(opts.parentObject, key)
	if err != nil || !found {
		return false, err
	}

	changed := false
	for i := index + 1; i < len(entries) && i < len(opts.Resources); i++ {
		// Writing Pending over an entry that is not this position's pipeline
		// loses the recorded run of a pipeline nobody is re-running.
		entry, ok := entries[i].(map[string]any)
		if !ok || !sameName(entries[i], opts.Resources[i].Name) {
			continue
		}
		if entry["phase"] == v1alpha1.WorkflowPhasePending && entry["hash"] == nil {
			continue
		}
		delete(entry, "hash")
		delete(entry, "message")
		entry["phase"] = v1alpha1.WorkflowPhasePending
		entry["lastTransitionTime"] = metav1.Now().Format(time.RFC3339)
		entries[i] = entry
		changed = true
	}

	if !changed {
		return false, nil
	}
	return true, resourceutil.SetPipelineStatuses(opts.parentObject, key, entries)
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

// labelsForWorkflowJobs narrows labelsForJobs to one workflow action, so the
// single List a reconciliation makes returns only that lane's Jobs.
func labelsForWorkflowJobs(opts Opts, action v1alpha1.Action) map[string]string {
	l := labelsForJobs(opts)
	l[v1alpha1.WorkflowActionLabel] = string(action)
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

// jobIsForPipeline reports whether job is a run of exactly this pipeline at
// exactly its current definition. Workflow type and action are not compared:
// they are in the label selector every Job list uses, so a Job that got this far
// already matches them.
func jobIsForPipeline(pipeline v1alpha1.PipelineJobResources, job *batchv1.Job) bool {
	if job == nil {
		return false
	}

	jobLabels := job.GetLabels()
	pipelineLabels := pipeline.Job.GetLabels()

	return jobLabels[v1alpha1.PipelineNameLabel] == pipelineLabels[v1alpha1.PipelineNameLabel] &&
		jobLabels[v1alpha1.KratixResourceHashLabel] == pipelineLabels[v1alpha1.KratixResourceHashLabel]
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

// cleanupJobs prunes the job history of every pipeline in the workflow. It runs
// both when the workflow completes and when a pipeline fails, so that a workflow
// that never succeeds still respects numberOfJobsToKeep.
func cleanupJobs(opts Opts, namespace string) error {
	for _, pipeline := range opts.Resources {
		l := labelsForAllWorkflowJobs(pipeline)
		l[v1alpha1.PipelineNameLabel] = pipeline.Name
		jobsForPipeline, err := getJobsWithLabels(opts, l, namespace)
		if err != nil {
			logging.Error(opts.logger, err, "failed to list jobs for pipeline", "pipeline", pipeline.Name)
			return err
		}
		if err := pruneJobs(opts, jobsForPipeline); err != nil {
			logging.Error(opts.logger, err, "failed to delete old jobs")
			return err
		}
	}

	return nil
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
			logging.Debug(opts.logger, "not deleting a running job", "name", job.GetName())
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

func createConfigurePipeline(opts Opts, key string, index int, reset statusReset) (passiveRequeue bool, err error) {
	resources := opts.Resources[index]
	logging.Info(opts.logger, "triggering pipeline", "workflowAction", resources.WorkflowAction, "pipeline", resources.Name)
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

	statusesChanged := false
	switch reset {
	case resetAll:
		if err = resourceutil.ResetPipelineStatusToPending(opts.parentObject, key, opts.Resources); err != nil {
			return false, err
		}
		statusesChanged = true
	case resetFollowing:
		if statusesChanged, err = resetPipelinesAfter(opts, key, index); err != nil {
			return false, err
		}
	case resetNone:
	}

	if err = setPipelineStartingStatus(opts, key, index, resources.Job, statusesChanged); err != nil {
		return false, err
	}

	deleteResources(opts, objectToDelete...)
	applyResources(opts, append(resources.GetObjects(), resources.Job)...)

	opts.eventRecorder.Eventf(opts.parentObject, nil, "Normal", "PipelineStarted", "PipelineStarted", "Configure Pipeline started: %s", resources.Name)

	return true, nil
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

func setPipelineStartingStatus(opts Opts, key string, pipelineIndex int, job *batchv1.Job, updated bool) error {
	obj := opts.parentObject

	currentMessage := resourceutil.GetStatus(obj, "message")
	if currentMessage != "Pending" {
		logging.Debug(opts.logger, "updating status message to Pending", "previousMessage", currentMessage)
		resourceutil.SetStatus(obj, opts.logger, "message", "Pending")
		updated = true
	}

	reconciled := resourceutil.GetCondition(obj, resourceutil.ReconciledCondition)
	if reconciled == nil || reconciled.Status != v1.ConditionUnknown || reconciled.Reason != "WorkflowPending" {
		logging.Debug(opts.logger, "updating Reconciled condition to WorkflowPending")
		resourceutil.MarkReconciledPending(obj, "WorkflowPending")
		updated = true
	}

	if shouldMarkConfigureWorkflowAsRunning(obj) {
		logging.Debug(opts.logger, "marking ConfigureWorkflowCompleted as running")
		resourceutil.MarkConfigureWorkflowAsRunning(opts.logger, obj)
		updated = true
	}

	if resourceutil.GetCurrentPipelinePhase(obj, key, job) != v1alpha1.WorkflowPhaseRunning {
		logging.Debug(opts.logger, "marking pipeline phase as Running", "pipelineIndex", pipelineIndex)
		if err := resourceutil.MarkCurrentPipelineAs(v1alpha1.WorkflowPhaseRunning, obj, key, opts.logger, job); err != nil {
			return err
		}
		updated = true
	}

	if updated {
		if err := opts.client.Status().Update(opts.ctx, obj); err != nil {
			logging.Error(opts.logger, err, "failed to update object status")
			return err
		}
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
