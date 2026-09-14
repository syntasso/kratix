package workflow

import (
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
// Only a client error stops it. A flat value that does not read at its declared
// type is left where it is and the entry migrates without it — the workflow then
// runs once more, where an error would leave the object on the old layout for
// ever.
func migrateWorkflowStatus(opts Opts, key string, action v1alpha1.Action) (bool, error) {
	changed, err := migrateFlatPipelines(opts, key, action)
	if err != nil {
		return false, err
	}

	if migrateFlatField[int64](opts.parentObject, flatSuspendedGenerationField,
		resourceutil.WorkflowsPath(key, flatSuspendedGenerationField)) {
		changed = true
	}

	if migrateFlatField[string](opts.parentObject, flatLastSuccessfulTimeField,
		resourceutil.WorkflowsPath(string(v1alpha1.WorkflowActionConfigure), keyedLastSuccessfulTimeField)) {
		changed = true
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
	if !found || err != nil {
		return false, nil //nolint:nilerr // a value that is not a list of entries is treated as absent, not as a reconcile failure.
	}

	if keyedPath := resourceutil.WorkflowsPath(key, flatPipelinesField); !keyedValueExists(obj, keyedPath) {
		if err := liftPipelineHashes(opts, action, flatPipelines); err != nil {
			return false, err
		}
		if err := unstructured.SetNestedSlice(obj.Object, flatPipelines, keyedPath...); err != nil {
			return false, err
		}
	}

	unstructured.RemoveNestedField(obj.Object, flatWorkflowsPath(flatPipelinesField)...)
	return true, nil
}

// migrateFlatField moves one scalar flat field to keyedPath. The flat copy goes
// even when the keyed one already holds a value, so the stale copy cannot be
// read back as if it were current.
func migrateFlatField[T int64 | string](obj *unstructured.Unstructured, flatField string, keyedPath []string) bool {
	raw, found, _ := unstructured.NestedFieldNoCopy(obj.Object, flatWorkflowsPath(flatField)...)
	if !found {
		return false
	}

	value, readsAtItsType := raw.(T)
	if !readsAtItsType {
		return false
	}

	if !keyedValueExists(obj, keyedPath) {
		if err := unstructured.SetNestedField(obj.Object, value, keyedPath...); err != nil {
			return false
		}
	}

	unstructured.RemoveNestedField(obj.Object, flatWorkflowsPath(flatField)...)
	return true
}

// keyedValueExists reports whether the keyed layout already holds this field. An
// unreadable keyed node counts as holding one: the flat copy is discarded rather
// than written over something the migration cannot see.
func keyedValueExists(obj *unstructured.Unstructured, keyedPath []string) bool {
	_, found, err := unstructured.NestedFieldNoCopy(obj.Object, keyedPath...)
	return found || err != nil
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
