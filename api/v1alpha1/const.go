package v1alpha1

const (
	SystemNamespace = "kratix-platform-system"

	WorkflowActionConfigure Action = "configure"
	WorkflowActionDelete    Action = "delete"

	WorkflowTypeResource Type = "resource"
	WorkflowTypePromise  Type = "promise"
)

// So we can set a functions arguments to be of type Action instead of string
type Action string
type Type string
