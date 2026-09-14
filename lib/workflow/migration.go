package workflow

import (
	"fmt"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/lib/resourceutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The pre-keyed (flat) workflow status layout. These three fields sat directly
// under status.kratix.workflows, where the workflow keys live now, so each has
// to be read by name rather than by walking the node.
const (
	flatPipelinesField           = "pipelines"
	flatSuspendedGenerationField = "suspendedGeneration"
	flatLastSuccessfulTimeField  = "lastSuccessfulConfigureWorkflowTime"
)

// keyedLastSuccessfulTimeField is where flatLastSuccessfulTimeField lands, always
// under the configure key: it records when the configure workflow last
// succeeded, so a delete reconciliation must not file it as a delete one.
const keyedLastSuccessfulTimeField = "lastSuccessfulTime"

// legacyStatusCounters are top-level status fields Kratix stopped writing before
// the workflow status was keyed at all. They are cleared here so a single pass
// leaves nothing of the old layout behind.
var legacyStatusCounters = []string{"workflows", "workflowsSucceeded", "workflowsFailed"}

func flatWorkflowsPath(field string) []string {
	return []string{"status", "kratix", "workflows", field}
}

// migrateWorkflowStatus moves the pre-keyed (flat) workflow status of parentObject
// to the keyed layout under key, lifting each pipeline's last-run hash from the
// retained Jobs when they still exist. Returns true when it changed the object;
// the caller writes the status and requeues before doing anything else.
//
// Every move merges rather than overwrites, because a half-upgraded writer can
// recreate the flat layout after the migration has already run.
func migrateWorkflowStatus(opts Opts, key string, action v1alpha1.Action) (bool, error) {
	changed := false

	pipelinesMoved, err := migrateFlatPipelines(opts, key, action)
	if pipelinesMoved {
		changed = true
	}
	if err != nil {
		return changed, err
	}

	suspendedGenerationMoved, err := migrateFlatField(opts.parentObject, nestedInt64,
		flatSuspendedGenerationField, resourceutil.WorkflowsPath(key, flatSuspendedGenerationField))
	if suspendedGenerationMoved {
		changed = true
	}
	if err != nil {
		return changed, err
	}

	lastSuccessfulTimeMoved, err := migrateFlatField(opts.parentObject, nestedString,
		flatLastSuccessfulTimeField,
		resourceutil.WorkflowsPath(string(v1alpha1.WorkflowActionConfigure), keyedLastSuccessfulTimeField))
	if lastSuccessfulTimeMoved {
		changed = true
	}
	if err != nil {
		return changed, err
	}

	if removeLegacyStatusCounters(opts.parentObject) {
		changed = true
	}

	if changed {
		logging.Info(opts.logger, "migrated the pre-keyed workflow status to the keyed layout", "key", key)
	}

	return changed, nil
}

// migrateFlatPipelines moves the flat pipeline statuses under key, lifting each
// Succeeded entry's hash from its retained Job on the way. Entries move verbatim
// and in order: nothing is invented for a pipeline they did not already name.
func migrateFlatPipelines(opts Opts, key string, action v1alpha1.Action) (bool, error) {
	obj := opts.parentObject

	flatPipelines, found, err := unstructured.NestedSlice(obj.Object, flatWorkflowsPath(flatPipelinesField)...)
	if err != nil {
		return false, fmt.Errorf("reading the pre-keyed workflow pipeline status: %w", err)
	}
	if !found {
		return false, nil
	}

	keyedPath := resourceutil.WorkflowsPath(key, flatPipelinesField)
	_, keyedFound, err := unstructured.NestedFieldNoCopy(obj.Object, keyedPath...)
	if err != nil {
		return false, fmt.Errorf("reading the keyed workflow pipeline status: %w", err)
	}

	if !keyedFound {
		if err = liftPipelineHashes(opts, action, flatPipelines); err != nil {
			return false, err
		}
		if err = unstructured.SetNestedSlice(obj.Object, flatPipelines, keyedPath...); err != nil {
			return false, err
		}
	}

	unstructured.RemoveNestedField(obj.Object, flatWorkflowsPath(flatPipelinesField)...)
	return true, nil
}

// nestedReader reads one flat field at its declared type, so a status field of
// the wrong type returns an error instead of panicking in the deep copy.
type nestedReader func(obj map[string]any, fields ...string) (any, bool, error)

func nestedInt64(obj map[string]any, fields ...string) (any, bool, error) {
	value, found, err := unstructured.NestedInt64(obj, fields...)
	return value, found, err
}

func nestedString(obj map[string]any, fields ...string) (any, bool, error) {
	value, found, err := unstructured.NestedString(obj, fields...)
	return value, found, err
}

// migrateFlatField moves one scalar flat field to keyedPath, then removes it.
// The flat field is removed even when the keyed one already holds a value, so
// the stale copy cannot be read back as if it were current.
func migrateFlatField(obj *unstructured.Unstructured, read nestedReader, flatField string, keyedPath []string) (bool, error) {
	value, found, err := read(obj.Object, flatWorkflowsPath(flatField)...)
	if err != nil {
		return false, fmt.Errorf("reading the pre-keyed workflow status field %q: %w", flatField, err)
	}
	if !found {
		return false, nil
	}

	_, keyedFound, err := unstructured.NestedFieldNoCopy(obj.Object, keyedPath...)
	if err != nil {
		return false, fmt.Errorf("reading the keyed workflow status field %q: %w", flatField, err)
	}

	if !keyedFound {
		if err = unstructured.SetNestedField(obj.Object, value, keyedPath...); err != nil {
			return false, err
		}
	}

	unstructured.RemoveNestedField(obj.Object, flatWorkflowsPath(flatField)...)
	return true, nil
}

func removeLegacyStatusCounters(obj *unstructured.Unstructured) bool {
	removed := false
	for _, counter := range legacyStatusCounters {
		if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "status", counter); found {
			unstructured.RemoveNestedField(obj.Object, "status", counter)
			removed = true
		}
	}
	return removed
}

// liftPipelineHashes fills in the hash of each Succeeded entry from the Job that
// produced it. Without it every pipeline of every existing object looks unrun
// after the upgrade and re-runs once.
//
// Only Succeeded entries are lifted: any other phase means the recorded run did
// not finish, and a hash on it would suppress the re-run it still needs.
func liftPipelineHashes(opts Opts, action v1alpha1.Action, pipelines []any) error {
	for _, entry := range pipelines {
		pipeline, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if phase, _ := pipeline["phase"].(string); phase != v1alpha1.WorkflowPhaseSucceeded {
			continue
		}
		if hash, _ := pipeline["hash"].(string); hash != "" {
			continue
		}
		name, _ := pipeline["name"].(string)
		if name == "" {
			continue
		}

		hash, err := hashOfMostRecentSucceededJob(opts, action, name)
		if err != nil {
			return err
		}
		if hash != "" {
			pipeline["hash"] = hash
		}
	}
	return nil
}

// hashOfMostRecentSucceededJob returns the kratix.io/hash of the newest retained
// Job that succeeded for pipelineName, or "" when there is none.
//
// The action has to be in the selector. Pipeline names are unique per workflow
// action, not across them, and both lanes' Jobs carry the same kratix.io/hash
// for the same object: without it a delete entry lifts the configure Job's hash,
// reads as complete, and the delete pipeline is skipped as the finalizer comes
// off. Jobs carrying only the retired kratix.io/work-* labels are not consulted
// either (#360); those entries migrate without a hash and re-run once.
func hashOfMostRecentSucceededJob(opts Opts, action v1alpha1.Action, pipelineName string) (string, error) {
	jobLabels := labelsForWorkflowJobs(opts, action)
	jobLabels[v1alpha1.PipelineNameLabel] = pipelineName

	jobs, err := getJobsWithLabels(opts, jobLabels, opts.namespace)
	if err != nil {
		return "", err
	}

	// Newest first, so the hash comes from the run the entry most likely records.
	resourceutil.SortJobsByCreationDateTime(jobs, false)
	for i := range jobs {
		if jobs[i].Status.Succeeded > 0 {
			return jobs[i].GetLabels()[v1alpha1.KratixResourceHashLabel], nil
		}
	}

	return "", nil
}
