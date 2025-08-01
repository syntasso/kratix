package contract

import "k8s.io/apimachinery/pkg/runtime/schema"

// The SDK interface implements the Kratix SDK core library function
type SDK interface {
	// ReadResourceInput reads the file in /kratix/input/object.yaml and returns a Resource
	ReadResourceInput() (Resource, error)
	// ReadPromiseInput reads the file in /kratix/input/object.yaml and returns a Resource
	ReadPromiseInput() (Promise, error)
	// ReadDestinationSelectors
	ReadDestinationSelectors() ([]DestinationSelector, error)
	// WriteOutput writes the content to the specifies file at the path /kratix/output/filepath
	WriteOutput(string, []byte) error
	// WriteStatus writes the specified status to the /kratix/output/status.yaml
	WriteStatus(Status) error
	// WriteDestinationSelectors writes the specified Destination Selectors to the /kratix/output/destination_selectors.yaml
	WriteDestinationSelectors([]DestinationSelector) error
	// WorkflowAction returns the value of KRATIX_WORKFLOW_ACTION environment variable
	WorkflowAction() string
	// WorkflowType returns the value of KRATIX_WORKFLOW_TYPE environment variable
	WorkflowType() string
	// PromiseName returns the value of the KRATIX_PROMISE_NAME environment variable
	PromiseName() string
	// PipelineName returns the value of the KRATIX_PIPELINE_NAME environment variable
	PipelineName() string
	// PublishStatus updates the status of the provided resource with the provided status
	PublishStatus(Resource, Status) error
	// ReadStatus reads the /kratix/output/status.yaml
	ReadStatus() (Status, error)
}

type Promise interface{}

type Resource interface {
	// GetValue queries the resource and returns the value at the specified path e.g. spec.dbConfig.size
	GetValue(string) (any, error)
	// GetStatus queries the resource and returns the resource.status
	GetStatus(string) (Status, error)
	// GetName queries the resource and returns the name
	GetName() string
	// GetStatus queries the resource and returns the namespace
	GetNamespace() string
	// GroupVersionKind queries the resource and returns the GroupVersionKind
	GetGroupVersionKind() schema.GroupVersionKind
	// GetLabels queries the resource and returns the labels
	GetLabels() map[string]string
	// GetAnnotations queries the resource and returns the annotations
	GetAnnotations() map[string]string
}

type Status interface {
	// Get queries the Status and retrieves the value at the specified path e.g. healthStatus.state
	Get(string) any
	// Set updates the value at the specified path e.g. healthStatus.state
	Set(string, any) error
	// Set removes the value at the specified path e.g. healthStatus.state
	Remove(string) bool
}

type DestinationSelector struct {
	Directory   string
	MatchLabels map[string]any
}
