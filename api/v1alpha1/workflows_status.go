package v1alpha1

import "encoding/json"

// WorkflowsStatus holds workflow execution status keyed by workflow: Kratix's own
// workflows use the workflow action as the key ("configure", "delete"); controllers
// that embed the workflow engine use their own key.
//
//nolint:recvcheck // the generated deepcopy takes the value; the decoder and Set take the pointer.
type WorkflowsStatus map[string]WorkflowStatus

// UnmarshalJSON leaves the map empty for a value that is not a map of workflow
// statuses. The pre-keyed flat layout ({pipelines: [...]}) is one — and without
// tolerating it the typed decode of every stored Promise fails and the Promise
// informer cache never populates on upgrade.
func (w *WorkflowsStatus) UnmarshalJSON(data []byte) error {
	statuses := map[string]WorkflowStatus{}
	if err := json.Unmarshal(data, &statuses); err != nil {
		*w = nil
		return nil //nolint:nilerr // the pre-keyed flat layout is not a decode failure; it carries no keyed status to read.
	}
	*w = statuses
	return nil
}

// Set stores status under key, allocating the map on first write.
func (w *WorkflowsStatus) Set(key string, status WorkflowStatus) {
	if *w == nil {
		*w = WorkflowsStatus{}
	}
	(*w)[key] = status
}
