package v1alpha1

import (
	"bytes"
	"encoding/json"
)

// WorkflowsStatus holds workflow execution status keyed by workflow: Kratix's own
// workflows use the workflow action as the key ("configure", "delete"); controllers
// that embed the workflow engine use their own key.
// It decodes both the keyed layout and the pre-keyed flat layout ({pipelines: [...]});
// a decoded flat layout is carried verbatim and re-emitted on marshal until the
// workflow engine migrates it (with per-pipeline hash lift) on the next reconcile.
//
// json.Marshaler must sit on the value so a Promise marshalled by value still
// emits the workflow status, while json.Unmarshaler and the mutators must sit
// on the pointer; the receivers cannot be made uniform.
//
// +kubebuilder:object:generate=false
//
//nolint:recvcheck
type WorkflowsStatus struct {
	Actions map[string]WorkflowStatus `json:"-"`
	// LegacyRaw carries a stored flat-layout value byte-for-byte across typed
	// decode/encode round-trips so the engine's migration — not a lossy typed
	// write — performs the layout move. Never set by new code.
	LegacyRaw []byte `json:"-"`
}

// UnmarshalJSON decodes the keyed layout into Actions. Anything that is not a
// map of workflow statuses is the pre-keyed flat layout, whose "pipelines" key
// holds an array where a WorkflowStatus is expected; without this fallback the
// typed decode of every stored Promise fails and the Promise informer cache
// never populates on upgrade.
func (w *WorkflowsStatus) UnmarshalJSON(data []byte) error {
	w.Actions = nil
	w.LegacyRaw = nil

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}

	actions := map[string]WorkflowStatus{}
	if err := json.Unmarshal(trimmed, &actions); err != nil {
		w.LegacyRaw = bytes.Clone(trimmed)
		return nil //nolint:nilerr // not a decode failure: this is the pre-keyed flat layout, kept verbatim for the engine to migrate.
	}

	w.Actions = actions
	return nil
}

// MarshalJSON emits the keyed layout, or the stored flat layout verbatim when
// this object has not been migrated yet.
func (w WorkflowsStatus) MarshalJSON() ([]byte, error) {
	if w.Actions != nil {
		return json.Marshal(w.Actions)
	}
	if len(w.LegacyRaw) > 0 {
		return bytes.Clone(w.LegacyRaw), nil
	}
	return []byte("{}"), nil
}

// IsZero drives the `omitzero` json tag on the Workflows field, so an object
// with no workflow status writes no `workflows` key at all.
func (w WorkflowsStatus) IsZero() bool {
	return len(w.Actions) == 0 && len(w.LegacyRaw) == 0
}

// Legacy decodes a stored pre-keyed (flat) workflow status, or returns nil when
// there is none. The flat layout named the same fields this type does — its
// pipelines, suspendedGeneration and nothing else — one workflow's worth of
// status for the whole object.
//
// It is transitional, for the readers that run before the engine's migration
// can: both controllers decide whether a workflow is suspended, and when to
// retry it, before they call the engine, and the engine is the only thing that
// migrates. Reading the keyed layout alone left an object that was suspended at
// upgrade with no retry scheduled and no way to un-suspend it — the migration
// could never run, because the suspended branch returns before the engine.
// Delete it when the flat layout can no longer be encountered.
func (w WorkflowsStatus) Legacy() *WorkflowStatus {
	if len(w.LegacyRaw) == 0 {
		return nil
	}
	legacy := &WorkflowStatus{}
	if err := json.Unmarshal(w.LegacyRaw, legacy); err != nil {
		return nil
	}
	return legacy
}

// Get returns the status stored under key, or the zero WorkflowStatus when the
// key is absent.
func (w WorkflowsStatus) Get(key string) WorkflowStatus {
	return w.Actions[key]
}

// Set stores status under key, allocating the map on first write.
func (w *WorkflowsStatus) Set(key string, status WorkflowStatus) {
	if w.Actions == nil {
		w.Actions = map[string]WorkflowStatus{}
	}
	w.Actions[key] = status
}

// DeepCopyInto is hand-written: controller-gen cannot generate for this type
// because its wire shape is produced by MarshalJSON rather than by its fields.
func (w *WorkflowsStatus) DeepCopyInto(out *WorkflowsStatus) {
	*out = *w
	if w.Actions != nil {
		out.Actions = make(map[string]WorkflowStatus, len(w.Actions))
		for key, status := range w.Actions {
			out.Actions[key] = *status.DeepCopy()
		}
	}
	out.LegacyRaw = bytes.Clone(w.LegacyRaw)
}

// DeepCopy copies the receiver, creating a new WorkflowsStatus.
func (w *WorkflowsStatus) DeepCopy() *WorkflowsStatus {
	if w == nil {
		return nil
	}
	out := new(WorkflowsStatus)
	w.DeepCopyInto(out)
	return out
}
