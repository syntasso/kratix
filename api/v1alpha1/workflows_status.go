package v1alpha1

import "encoding/json"

// WorkflowsStatus holds workflow execution status keyed by workflow: Kratix's own
// workflows use the workflow action as the key ("configure", "delete"); controllers
// that embed the workflow engine use their own key.
//
//nolint:recvcheck // the generated deepcopy takes the value; the decoder and Set take the pointer.
type WorkflowsStatus map[string]WorkflowStatus

// UnmarshalJSON keeps every key that decodes and drops only the ones that do not.
// Failing the whole value instead empties the map: the pre-keyed flat layout
// ({pipelines: [...]}) would then fail the typed decode of every stored Promise on
// upgrade, and one unreadable foreign key would take the keys beside it with it —
// the next status write omits an empty `workflows` and deletes them.
func (w *WorkflowsStatus) UnmarshalJSON(data []byte) error {
	*w = nil

	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil //nolint:nilerr // a value that is not a map of workflow statuses carries no keyed status to read.
	}

	for key, value := range raw {
		var status WorkflowStatus
		if err := json.Unmarshal(value, &status); err != nil {
			continue
		}
		w.Set(key, status)
	}

	return nil
}

// Set stores status under key, allocating the map on first write.
func (w *WorkflowsStatus) Set(key string, status WorkflowStatus) {
	if *w == nil {
		*w = WorkflowsStatus{}
	}
	(*w)[key] = status
}
