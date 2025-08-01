# Kratix SDK Contract

This defines the contract that must be honoured when introducing any new Kratix SDKs as well as the core features to ensure consistency across all SDKs.

## SDK Interface

The SDK interface implements the Kratix SDK core library functions

**`ReadResourceInput() (Resource, error)`**

ReadResourceInput reads the file in `/kratix/input/object.yaml` and returns a Resource

**`ReadPromiseInput() (Promise, error)`**

ReadPromiseInput reads the file in `/kratix/input/object.yaml` and returns a Resource

**`ReadDestinationSelectors() ([]DestinationSelector, error)`**

ReadDestinationSelectors

**`WriteOutput(string, []byte) error`**

WriteOutput writes the content to the specifies file at the path /kratix/output/filepath

**`WriteStatus(Status) error`**

WriteStatus writes the specified status to the `/kratix/output/status.yaml`

**`WriteDestinationSelectors([]DestinationSelector) error`**

WriteDestinationSelectors writes the specified Destination Selectors to the `/kratix/output/destination_selectors.yaml`

**`WorkflowAction() string`**

WorkflowAction returns the value of KRATIX_WORKFLOW_ACTION environment variable

**`WorkflowType() string`**

WorkflowType returns the value of KRATIX_WORKFLOW_TYPE environment variable

**`PromiseName() string`**

PromiseName returns the value of the KRATIX_PROMISE_NAME environment variable

**`PipelineName() string`**

PipelineName returns the value of the KRATIX_PIPELINE_NAME environment variable

**`PublishStatus(Resource, Status) error`**

PublishStatus updates the status of the provided resource with the provided status

**`ReadStatus() (Status, error)`**

ReadStatus reads the /kratix/output/status.yaml

## Promise Interface

The SDK interface implements the core functions for interacting with a Promise

## Resource Interface

The Resource interface implements the core functions for getting attributes of a Resource

**`GetValue(string) (any, error)`**

GetValue queries the resource and returns the value at the specified path e.g. spec.dbConfig.size

**`GetStatus(string) (Status, error)`**

GetStatus queries the resource and returns the resource.status

**`GetName() string`**

GetName queries the resource and returns the name

**`GetNamespace() string`**

GetStatus queries the resource and returns the namespace

**`GetGroupVersionKind() schema.GroupVersionKind`**

GroupVersionKind queries the resource and returns the GroupVersionKind

**`GetLabels() map[string]string`**

GetLabels queries the resource and returns the labels

**`GetAnnotations() map[string]string`**

GetAnnotations queries the resource and returns the annotations

## Status Interface

The Status interface provides helpers for manipulating a structured status

**`Get(string) any`**

Get queries the Status and retrieves the value at the specified path e.g. healthStatus.state

**`Set(string, any) error`**

Set updates the value at the specified path e.g. healthStatus.state

**`Remove(string) bool`**

Set removes the value at the specified path e.g. healthStatus.state

## DestinationSelector Object

The `DestinationSelector` is a concrete type with the following attributes:

* `Directory` string

* `MatchLabels` map[string]any
