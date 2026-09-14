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

	// Set by other controllers that use the Workflow engine
	SkipConditions bool

	// WorkflowKey is the key under status.kratix.workflows that this workflow's
	// pipeline ledger is stored at. Leave it empty and Kratix's own keys are
	// used, one per workflow action. Controllers that embed the workflow engine
	// set it so their ledger sits beside Kratix's rather than on top of it:
	// without it, their reset of "the" pipeline status wipes the configure or
	// delete entries Kratix is progressing through.
	//
	// Validated on entry to ReconcileConfigure/ReconcileDelete, so a bad key
	// fails the reconciliation instead of writing status at a path nothing can
	// read back.
	WorkflowKey string
}

func (o *Opts) SetParentObject(parentObj *unstructured.Unstructured) {
	o.parentObject = parentObj
}

// statusKey is the key under status.kratix.workflows that this reconciliation's
// pipeline status is stored at. Kratix's own workflows key by workflow action,
// so the configure and delete lanes keep separate ledgers instead of each reset
// wiping the other's entries; a controller that embeds the workflow engine
// overrides both with its own WorkflowKey.
func (o *Opts) statusKey(action v1alpha1.Action) string {
	if o.WorkflowKey != "" {
		return o.WorkflowKey
	}
	return string(action)
}

// workflowKeyPattern is deliberately narrower than "any map key": the key
// becomes a segment of a status path that people read with `kubectl get -o
// jsonpath` and that controllers walk with unstructured field paths, so a dot,
// a slash or a leading dash in it turns a working query into a silently empty
// one.
var workflowKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9]([-_a-zA-Z0-9]*[a-zA-Z0-9])?$`)

// validateWorkflowKey rejects a WorkflowKey that would collide with one of
// Kratix's own lanes or would not survive as a status path segment. The
// collision matters more than it looks: a controller keying its ledger at
// "configure" would have its entries pruned and rewritten by Kratix's configure
// workflow on the next reconcile, and vice versa.
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

	if !opts.SkipConditions {
		if changed, err := migrateWorkflowStatus(opts, deleteKey, v1alpha1.WorkflowActionDelete); err != nil || changed {
			if changed && err == nil {
				err = opts.client.Status().Update(opts.ctx, opts.parentObject)
			}
			return changed, err
		}
	}

	if len(opts.Resources) > 1 {
		logging.Warn(opts.logger, "multiple delete pipelines found; only the first will be used")
	}
	// The delete lane runs exactly one pipeline. Truncating here rather than
	// indexing everywhere keeps the ledger it writes the same shape as the
	// workflow it runs; the configure lane's entries live under their own key
	// and are never read or written from here.
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

	ledger, err := deletePipelineLedger(opts, deleteKey, evidence)
	if err != nil {
		return false, err
	}
	phase := ledger.phase(pipeline.Name)
	workflowSuspended := opts.parentObject.GetLabels()[v1alpha1.WorkflowSuspendedLabel] == "true"

	if phase == v1alpha1.WorkflowPhaseSuspended {
		if workflowSuspended {
			logging.Info(opts.logger, "delete pipeline suspended; waiting")
			return true, nil
		}
		// Resuming after a retry interval elapsed, not a genuine restart:
		// preserve the pipeline's existing status (attempts, nextRetryAt),
		// mirroring how configure's resume-from-suspended path never resets.
		return createDeletePipeline(opts, deleteKey, resetNone)
	}

	if phase == v1alpha1.WorkflowPhaseSucceeded && ledger.hash(pipeline.Name) == desiredPipelineHash(pipeline) {
		if workflowSuspended {
			logging.Info(opts.logger, "delete pipeline completed but workflow is suspended; waiting")
			return true, nil
		}
		// The ledger records the completion, so the delete workflow stays
		// complete once its Job has been pruned or garbage collected. Deriving
		// this from the Job instead is what made a controller that embeds the
		// workflow engine recreate the delete pipeline forever: no Job meant
		// "never ran", and the finalizer was never removed.
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
		if opts.SkipConditions {
			// Unreachable today: the ephemeral ledger this caller gets is built
			// from the same succeeded Job, so the Succeeded branch above has
			// already returned. Kept because what it guards against is a status
			// write on an object whose status the caller owns — that write goes
			// over the ledger the caller is progressing through, which is the
			// failure the per-workflow key exists to prevent.
			return false, nil
		}
		if err = resourceutil.MarkCurrentPipelineAsSucceeded(opts.parentObject, deleteKey, opts.logger, job); err != nil {
			return false, err
		}
		if err = opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
			return false, err
		}
		return true, nil
	}
}

// deletePipelineLedger reads the delete workflow's ledger, seeding the single
// entry in memory when the object has none yet. The seed is not written on its
// own: unlike the configure lane there is no order to normalise and nothing to
// prune, and every branch that needs the entry to exist writes the status
// anyway, so seeding here saves the delete workflow a whole reconcile.
func deletePipelineLedger(opts Opts, key string, evidence map[string]*batchv1.Job) (pipelineLedger, error) {
	if opts.SkipConditions {
		return ephemeralLedger(opts, evidence), nil
	}

	entries, _, err := resourceutil.GetPipelineStatuses(opts.parentObject, key)
	if err != nil {
		return pipelineLedger{}, err
	}

	ledger := newPipelineLedger(entries)
	if _, found := ledger.byName[opts.Resources[0].Name]; found {
		return ledger, nil
	}

	entries = []any{newPendingEntry(opts.Resources[0].Name)}
	if err := resourceutil.SetPipelineStatuses(opts.parentObject, key, entries); err != nil {
		return pipelineLedger{}, err
	}
	return newPipelineLedger(entries), nil
}

// createDeletePipelineWhenConfigureIdle holds the delete pipeline back until no
// configure Job is still running, so the two lanes never write the same Works
// at the same time.
func createDeletePipelineWhenConfigureIdle(opts Opts, key string, pipeline v1alpha1.PipelineJobResources, reset ledgerReset) (bool, error) {
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

func createDeletePipeline(opts Opts, key string, reset ledgerReset) (passiveRequeue bool, err error) {
	pipeline := opts.Resources[0]
	logging.Debug(opts.logger, "creating delete pipeline; execution will commence")
	if isManualReconciliation(opts.parentObject.GetLabels()) {
		if err := removeManualReconciliationLabel(opts); err != nil {
			return false, err
		}
	}
	// A caller that owns the parent object's status gets no status written for
	// it here either, the same as every other create path: its ledger is its
	// own, and the engine writing this lane's entries over it is what the
	// per-workflow key exists to prevent.
	if !opts.SkipConditions {
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
	}
	//TODO retrieve error information from applyResources to return to the caller
	applyResources(opts, append(pipeline.GetObjects(), pipeline.Job)...)
	opts.eventRecorder.Eventf(opts.parentObject, nil, "Normal", "PipelineStarted", "PipelineStarted", "Delete Pipeline started: %s", pipeline.Name)
	return true, nil
}

// pipelineLedger is the record of how far a workflow has got: one entry per
// pipeline, keyed by pipeline name. It is read from
// status.kratix.workflows.<key>.pipelines, or built in memory from Job evidence
// for callers that own the parent object's status.
//
// Entries are looked up by name, never by position. A workflow's pipelines can
// be added to, removed or reordered between reconciliations, and matching by
// index would then read one pipeline's history as another's — marking a
// pipeline that has never run as already succeeded.
type pipelineLedger struct {
	byName map[string]map[string]any
}

func newPipelineLedger(entries []any) pipelineLedger {
	ledger := pipelineLedger{byName: map[string]map[string]any{}}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := entry["name"].(string); name != "" {
			ledger.byName[name] = entry
		}
	}
	return ledger
}

func (l pipelineLedger) phase(name string) string {
	phase, _ := l.byName[name]["phase"].(string)
	return phase
}

func (l pipelineLedger) hash(name string) string {
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
// ran now. The pipeline's own hash is already folded into it by the Job factory,
// so this one value covers both a change to the parent object's spec and a
// change to the pipeline definition.
func desiredPipelineHash(pipeline v1alpha1.PipelineJobResources) string {
	return pipeline.Job.GetLabels()[v1alpha1.KratixResourceHashLabel]
}

// ledgerReset says what a pipeline creation does to the rest of the ledger.
type ledgerReset int

const (
	// resetNone leaves every other entry alone: used when resuming a suspended
	// pipeline, whose own entry carries the retry bookkeeping to preserve.
	resetNone ledgerReset = iota
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
	// failedHalt marks the stepWait that a Failed-at-the-current-definition
	// entry produces, as opposed to waiting on a Job that is still running.
	failedHalt bool
}

// ReconcileConfigure reconciles configure workflows.
//
// Progression is driven by the pipeline ledger, not by which Job happens to be
// the most recent. Jobs are evidence that moves an entry along; the ledger is
// the record of where the workflow got to. That is the whole point of this
// function: a workflow whose Jobs have all been pruned, or whose namespace was
// swept, is complete — not restarted from its first pipeline.
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

	if !opts.SkipConditions {
		if changed, err := migrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure); err != nil || changed {
			if changed && err == nil {
				err = opts.client.Status().Update(opts.ctx, opts.parentObject)
			}
			return changed, err
		}
	}

	evidence, err := jobEvidence(opts, v1alpha1.WorkflowActionConfigure)
	if err != nil {
		return false, err
	}

	// A caller that owns the parent object's status gets its ledger from the
	// Jobs; there is nothing to seed, prune or write. The label flows below run
	// for it all the same: they are driven by labels on the object and they
	// write labels back, and skipping them left manual reconciliation,
	// run-from-start and resume-from-suspended completely inert for such a
	// caller — the label was not even removed, so it stuck on the object.
	ledger := ephemeralLedger(opts, evidence)
	if !opts.SkipConditions {
		var changed bool
		if ledger, changed, err = seedAndPruneLedger(opts, configureKey); err != nil {
			return false, err
		}
		if changed {
			if err = opts.client.Status().Update(opts.ctx, opts.parentObject); err != nil {
				logging.Error(opts.logger, err, "failed to update parent object status")
				return false, err
			}
			return true, nil
		}
	}

	if requeue, handled, err := reconcileWorkflowLabels(opts, configureKey, evidence); handled {
		return requeue, err
	}

	return runWorkflowStep(opts, configureKey, ledger, evidence)
}

// reconcileWorkflowLabels handles the three label-driven flows — manual
// reconciliation, restart-from-start and resume-from-suspended — and reports
// whether it took the reconciliation.
//
// These run BEFORE any Job evidence is interpreted, and the order is
// load-bearing. isFailed() counts a suspended Job as failed, and a manual
// reconciliation deliberately suspends the Job that is running; reading the
// evidence first would therefore write Failed into the authoritative ledger
// every time somebody re-runs a workflow by hand, and the Failed entry would
// then halt the very run the label asked for.
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
	// Any Job of this workflow still in flight holds the resume back, not just
	// this pipeline's own: creating a Job here beside a live one gives the
	// workflow two pipelines writing the same parent's Works at once.
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
	// Cleanup happens here because a suspended workflow never reaches the
	// completion branch of the walk below.
	return requeue, true, cleanup(opts, opts.namespace)
}

// nextWorkflowStep walks the pipelines in order and stops at the first one that
// is not settled. "Settled" is the entry saying Succeeded at exactly the hash
// the pipeline would run with now: a Succeeded entry recorded against an older
// definition is a pipeline that still has to run.
func nextWorkflowStep(opts Opts, ledger pipelineLedger, evidence map[string]*batchv1.Job) workflowStep {
	for i, pipeline := range opts.Resources {
		desired := desiredPipelineHash(pipeline)
		phase := ledger.phase(pipeline.Name)

		if phase == v1alpha1.WorkflowPhaseSucceeded && ledger.hash(pipeline.Name) == desired {
			continue
		}

		if phase == v1alpha1.WorkflowPhaseSuspended {
			// Job evidence never overwrites a Suspended entry. Suspension is
			// written by whoever owns the retry policy, and the Job it left
			// behind reads as failed — letting the evidence win here would turn
			// every scheduled retry into a permanent failure.
			logging.Debug(opts.logger, "pipeline is suspended; waiting", "pipeline", pipeline.Name)
			return workflowStep{kind: stepWait, index: i}
		}

		if phase == v1alpha1.WorkflowPhaseFailed && ledger.hash(pipeline.Name) == desired {
			// This exact definition has already failed and the failure is
			// recorded. Re-running it on every reconcile would hammer the
			// cluster; the way back is a manual reconciliation, a restart, or a
			// change that produces a new hash.
			logging.Debug(opts.logger, "pipeline failed at the current definition; waiting for a re-run to be requested", "pipeline", pipeline.Name)
			return workflowStep{kind: stepWait, index: i, failedHalt: true}
		}

		job := evidence[pipeline.Name]
		switch {
		case isRunning(job):
			// Never start a second Job for a pipeline that already has one in
			// flight, whichever definition that one is running.
			logging.Debug(opts.logger, "job already inflight for pipeline; waiting for completion", "job", job.Name, "pipeline", pipeline.Name)
			return workflowStep{kind: stepWait, index: i, job: job}
		case phase == v1alpha1.WorkflowPhasePending || phase == "":
			// Pending means no run of this pipeline is outstanding: either
			// nothing has been started, or a manual reconciliation or restart
			// deliberately unwound what had been. Any terminal Job still lying
			// around therefore describes a run that is over, and adopting it
			// would make a re-run of the workflow skip every pipeline whose
			// definition had not changed — which is exactly what a manual
			// reconciliation is for.
			//
			// An entry with no phase at all is read the same way: nothing is
			// recorded, so nothing has run. Adopting a Job for it would settle a
			// pipeline against a run the ledger cannot account for.
			return runOrWaitForWorkflow(opts, evidence, i)
		case !jobIsForPipeline(pipeline, job):
			// Either there is no Job for this pipeline at all, or the only one
			// left ran a definition that is no longer wanted. Both mean run it.
			//
			// The no-Job case includes a pipeline whose entry says Running: the
			// Job completed and was pruned, or deleted, in the window between
			// two reconciliations, and no evidence of it survives. Re-running is
			// the safe answer — Kratix pipelines are expected to be idempotent —
			// where trusting the entry would wedge the workflow forever.
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
// The walk stops at the first unsettled pipeline, and after a second spec edit
// that is a pipeline *behind* the one whose Job is running: without this guard
// the engine starts a Job for it while the later pipeline's Job is still
// writing Works for the same parent, and resetFollowing has already put that
// running pipeline's entry back to Pending under its own feet. When the last
// pipeline's Job then completes, its in-Job status writer marks the whole
// workflow completed while the new Job is still running.
//
// It keys on a Job that is actually running, never on what an entry says: a
// Running entry whose Job is gone is still recreated (progression assertion 4).
// A manual reconciliation suspends the running Job first, in
// reconcileWorkflowLabels, so the label branches are never held up by it.
func runOrWaitForWorkflow(opts Opts, evidence map[string]*batchv1.Job, index int) workflowStep {
	if job := mostRecentRunningJob(opts, evidence); job != nil {
		logging.Debug(opts.logger, "job already inflight for another pipeline of this workflow; waiting for completion",
			"job", job.Name, "pipeline", opts.Resources[index].Name)
		return workflowStep{kind: stepWait, index: index, job: job}
	}
	return workflowStep{kind: stepRun, index: index}
}

func runWorkflowStep(opts Opts, key string, ledger pipelineLedger, evidence map[string]*batchv1.Job) (passiveRequeue bool, err error) {
	step := nextWorkflowStep(opts, ledger, evidence)

	if step.kind != stepComplete {
		pipeline := opts.Resources[step.index]
		opts.logger = opts.logger.WithName(pipeline.Name)
	}

	switch step.kind {
	case stepComplete:
		logging.Debug(opts.logger, "all pipelines in the workflow are complete")
		return false, cleanup(opts, opts.namespace)

	case stepWait:
		if step.failedHalt && opts.SkipConditions {
			// A caller that owns the status never reaches recordPipelineFailure
			// — its ledger is rebuilt from the Jobs on every reconcile, so the
			// failure is re-derived rather than recorded once — and pruning is
			// the only thing that stops a workflow that keeps failing growing
			// its Job history without bound. Jobs only, as on the recorded
			// failure path: Works are left alone while the workflow is unfinished.
			return true, cleanupJobs(opts, opts.namespace)
		}
		// The halt writes and emits nothing: the failure is already recorded in
		// the ledger, in the conditions and in one event, and rewriting any of
		// them here re-triggers the watch that brought us back — the hot loop.
		return true, nil

	case stepRecordSuccess:
		if opts.SkipConditions {
			// Unreachable today: the ephemeral ledger such a caller gets is
			// built from the same succeeded Job, so this pipeline is already
			// settled and the walk moved past it. Kept because what it guards
			// against is a status write on an object whose status the caller
			// owns, which would overwrite the ledger that caller is
			// progressing through.
			return true, nil
		}
		// The hash comes off the Job that actually ran, not off the pipeline
		// being reconciled: recording the desired hash instead would mark a
		// pipeline as having run a definition it never saw.
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

	// Unreachable for a caller that owns the parent object's status: its
	// ephemeral ledger records a failed Job as Failed at that Job's own hash,
	// so the walk halts at the Failed arm and never reaches this step. Kept
	// because what it guards against is Kratix's conditions and ledger being
	// written onto an object whose status belongs to that caller.
	if !opts.SkipConditions {
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
	}

	opts.eventRecorder.Eventf(opts.parentObject, nil, v1.EventTypeWarning, resourceutil.ConfigureWorkflowCompletedFailedReason, resourceutil.ConfigureWorkflowCompletedFailedReason, "A %s/configure Pipeline has failed: %s", opts.workflowType, pipeline.Name)
	logging.Warn(opts.logger, "pipeline job failed; exiting workflow", "failedJob", step.job.Name, "pipeline", pipeline.Name)

	// A workflow that keeps failing never reaches the completion branch of the
	// walk, so without pruning here its Jobs grow without bound once the
	// periodic reconcile retries failed runs. This reconciler is shared, so the
	// pruning applies to promise and resource workflows alike; only resources
	// retry on the interval today, so a failing promise workflow is pruned when
	// it is re-run manually or by a spec change. Jobs only, not the full
	// cleanup(): Works are left alone while the workflow has not completed.
	return true, cleanupJobs(opts, opts.namespace)
}

// seedAndPruneLedger reconciles the ledger under key with the workflow's current
// pipelines: entries whose pipeline no longer exists are dropped, pipelines with
// no entry are appended as Pending, and the entries are put back into the
// workflow's own order.
//
// This is what makes the ledger safe to read as the record of progress. A stale
// Succeeded entry left behind by a renamed or removed pipeline would otherwise
// count towards completion for a pipeline nobody runs any more, and a pipeline
// with no entry at all makes every later mark fail with "no pipeline found for
// job" — the ledger and the workflow have to describe the same set of pipelines
// before anything reads either.
func seedAndPruneLedger(opts Opts, key string) (pipelineLedger, bool, error) {
	stored, _, err := resourceutil.GetPipelineStatuses(opts.parentObject, key)
	if err != nil {
		return pipelineLedger{}, false, err
	}
	existing := newPipelineLedger(stored)

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
		return pipelineLedger{}, false, err
	}
	return newPipelineLedger(entries), true, nil
}

func sameName(raw any, name string) bool {
	entry, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	stored, _ := entry["name"].(string)
	return stored == name
}

// ephemeralLedger builds a ledger in memory from Job evidence alone, for callers
// that own the parent object's status lifecycle (SkipConditions). It records
// only what the evidence proves — a retained Job that succeeded at the hash the
// pipeline would run with now — so the walk reaches the same decisions it does
// from a stored ledger, without a single status read or write.
func ephemeralLedger(opts Opts, evidence map[string]*batchv1.Job) pipelineLedger {
	entries := make([]any, 0, len(opts.Resources))
	for _, pipeline := range opts.Resources {
		entry := map[string]any{"name": pipeline.Name, "phase": v1alpha1.WorkflowPhasePending}
		if job := evidence[pipeline.Name]; jobIsForPipeline(pipeline, job) && !isRunning(job) {
			// A Job that finished is proof either way. Recording only the
			// successes left a failed run reading as Pending — "nothing has run
			// yet" — and the walk's Pending arm then created a replacement Job
			// on every reconcile, for ever: the Failed arm in nextWorkflowStep
			// is the only thing between such a caller and an unbounded create
			// loop.
			entry["phase"] = v1alpha1.WorkflowPhaseSucceeded
			if isFailed(job) {
				entry["phase"] = v1alpha1.WorkflowPhaseFailed
			}
			entry["hash"] = job.GetLabels()[v1alpha1.KratixResourceHashLabel]
		}
		entries = append(entries, entry)
	}
	return newPipelineLedger(entries)
}

// jobEvidence lists this workflow's Jobs once and indexes the most recent one
// per pipeline name.
//
// It selects on the labels Kratix writes today. A Job carrying only the retired
// kratix.io/work-* labels is invisible to progression by design (#360): a
// cluster old enough to still have one is old enough that the run it describes
// is not the run the ledger records, and the pipeline it belongs to re-runs
// once. Re-adding a legacy selector here would resurrect the very ambiguity the
// keyed ledger exists to remove.
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

// mostRecentRunningJob returns the newest Job of this workflow that is still
// running, or nil when none is.
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

// resetPipelinesAfter returns the entries after index to Pending. Once a
// pipeline runs again, everything downstream of it ran against a workflow state
// that no longer holds, so leaving their Succeeded entries in place reports a
// completion the new run has not reached — and, because the hash goes with the
// entry, would let the workflow declare itself finished the moment the pipeline
// being re-run succeeds.
func resetPipelinesAfter(opts Opts, key string, index int) (bool, error) {
	entries, found, err := resourceutil.GetPipelineStatuses(opts.parentObject, key)
	if err != nil || !found {
		return false, err
	}

	changed := false
	for i := index + 1; i < len(entries) && i < len(opts.Resources); i++ {
		// Positions line up here because seedAndPruneLedger has already aligned
		// the ledger with the workflow's pipelines, so this skip is unreachable
		// from the engine's own path. It guards against the one thing that
		// makes an unwind actively harmful: writing Pending over the entry of a
		// pipeline that is not the one at this position — a pipeline nobody is
		// re-running, whose recorded run is then lost.
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
// single List a reconciliation makes returns only that lane's Jobs. Selecting on
// the action here rather than filtering afterwards is what lets jobIsForPipeline
// shrink to the two labels that actually vary per run.
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
// they are part of the label selector every Job list uses, so a Job that got
// this far already matches them. The pipeline's own hash is folded into
// kratix.io/hash by the Job factory, so that one label covers both a spec change
// and a pipeline change.
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
			// Never prune a Job that is still running, however old it is.
			// Progression now reads the Jobs as evidence of what the pipelines
			// are doing, so deleting a live one makes its pipeline look like it
			// has no Job at all and the workflow starts a duplicate run of it.
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

func createConfigurePipeline(opts Opts, key string, index int, reset ledgerReset) (passiveRequeue bool, err error) {
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

	ledgerChanged := false
	if !opts.SkipConditions {
		switch reset {
		case resetAll:
			if err = resourceutil.ResetPipelineStatusToPending(opts.parentObject, key, opts.Resources); err != nil {
				return false, err
			}
			ledgerChanged = true
		case resetFollowing:
			if ledgerChanged, err = resetPipelinesAfter(opts, key, index); err != nil {
				return false, err
			}
		case resetNone:
		}
	}

	if err = setPipelineStartingStatus(opts, key, index, resources.Job, ledgerChanged); err != nil {
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
	if opts.SkipConditions {
		return nil
	}

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
