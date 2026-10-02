package v1alpha1

const (
	SystemNamespace = "kratix-platform-system"

	WorkflowActionConfigure Action = "configure"
	WorkflowActionDelete    Action = "delete"

	WorkflowTypeResource Type = "resource"
	WorkflowTypePromise  Type = "promise"

	PlaceholderPromiseVersion = "not-set"

	// HealthDefinitionsAnnotation holds how many HealthDefinitions a Work carries; a
	// pipeline may write several. HealthDefinitionsVersionAnnotation holds the Promise
	// version set on all of them, since one Work comes from one run at one version.
	HealthDefinitionsAnnotation        = "kratix.io/health-definitions"
	HealthDefinitionsVersionAnnotation = "kratix.io/health-definitions-version"
)

// So we can set a functions arguments to be of type Action instead of string
type Action string
type Type string
