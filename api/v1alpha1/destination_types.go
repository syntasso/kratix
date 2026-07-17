/*
Copyright 2021 Syntasso.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

distributed under the License is distributed on an "AS IS" BASIS,
Unless required by applicable law or agreed to in writing, software
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

type StateStoreCoreFields struct {
	// Path within the StateStore to write documents. This path should be allocated
	// to Kratix as it will create, update, and delete files within this path.
	// Path structure begins with provided path and ends with namespaced destination name:
	//   <StateStore.Spec.Path>/<Destination.Spec.Path>/<Destination.Metadata.Namespace>/<Destination.Metadata.Name>/
	//+kubebuilder:validation:Optional
	Path string `json:"path,omitempty"`
	// SecretRef specifies the Secret containing authentication credentials
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`
}

// TODO: revisit if we want all destination secrets on a single known namespaces
// (i.e. kratix-platform-system) or if we want to allow users to specify a
// namespace for each destination secret.

// DestinationSpec defines the desired state of Destination.
type DestinationSpec struct {
	// Path within StateStore to write documents, this will be appended to any
	// specficed Spec.Path provided in the referenced StateStore.
	// The Path structure will be:
	//   <StateStore.Spec.Path>/<Destination.Spec.Path>/
	// Kratix may create other subdirectories, depending on the Filepath.mode you select.
	// To write to the root of the StateStore.Spec.Path, set Path to "." or "/".
	// Defaults to the Destination name
	// +kubebuilder:validation:Required
	Path string `json:"path,omitempty"`

	// Reference to the StateStore (GitStateStore or BucketStateStore) used to persist resources for this Destination
	StateStoreRef *StateStoreReference `json:"stateStoreRef,omitempty"`

	// By default, Kratix will schedule works without labels to all destinations
	// (for promise dependencies) or to a random destination (for resource
	// requests). If StrictMatchLabels is true, Kratix will only schedule works
	// to this destination if it can be selected by the Promise's
	// destinationSelectors. An empty label set on the work won't be scheduled
	// to this destination, unless the destination label set is also empty
	// +kubebuilder:validation:Optional
	StrictMatchLabels bool `json:"strictMatchLabels,omitempty"`

	//The filepath mode to use when writing files to the destination.
	// +kubebuilder:default:={mode: "nestedByMetadata"}
	Filepath Filepath `json:"filepath,omitempty"`

	// cleanup can be set to either:
	// - none (default): no cleanup after removing the destination
	// - all: workplacements and statestore contents will be removed after removing the destination
	// +kubebuilder:validation:Enum:={none,all}
	// +kubebuilder:default:="none"
	Cleanup string `json:"cleanup,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:={}
	InitWorkloads InitWorkloads `json:"initWorkloads"`

	// SchedulingPolicy controls which Promises are authorized to schedule Works
	// to this Destination. It is evaluated by the scheduler after label matching:
	// a Work must both match the Destination's labels and be authorized by this
	// policy to be scheduled here. If omitted, all Promises are allowed
	// (preserving pre-existing behaviour).
	// +kubebuilder:validation:Optional
	SchedulingPolicy *SchedulingPolicy `json:"schedulingPolicy,omitempty"`
}

// SchedulingPolicy authorizes Promises to schedule Works to a Destination.
//
// A Promise's name authorizes the Promise for BOTH work kinds (promise-level
// dependency Works and resource-level Works). A rule's namespaceSelector further
// constrains only that Promise's RESOURCE Works to the matching requester namespaces;
// it never affects the dependency Works, so a shared operator is not torn down by a
// namespace restriction.
type SchedulingPolicy struct {
	// Type determines how the rules are interpreted:
	// - Allow: a Work is authorized only if it matches the rules.
	// - Deny: a Work is authorized unless it matches the rules.
	// +kubebuilder:validation:Enum:={Allow,Deny}
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// PromiseNames is exact-match shorthand for rules that authorize by Promise name
	// only (no namespace restriction). Each entry is equivalent to a Rule with just
	// promiseName set.
	// +kubebuilder:validation:Optional
	PromiseNames []string `json:"promiseNames,omitempty"`

	// Rules authorize by Promise name and, optionally, by the namespace of the
	// resource request. Rules and PromiseNames are combined.
	// +kubebuilder:validation:Optional
	Rules []SchedulingPolicyRule `json:"rules,omitempty"`
}

// SchedulingPolicyRule is one entry in a SchedulingPolicy.
type SchedulingPolicyRule struct {
	// PromiseName is the exact Promise name this rule applies to. Empty matches any Promise.
	// +kubebuilder:validation:Optional
	PromiseName string `json:"promiseName,omitempty"`

	// NamespaceSelector restricts which requester namespaces this rule applies to.
	// When set, the rule constrains only resource-level Works; the Promise's
	// dependency (promise-level) Works are unaffected. When empty, the rule applies
	// regardless of namespace.
	// +kubebuilder:validation:Optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
}

// effectiveRules returns Rules with the PromiseNames shorthand desugared into rules.
func (p *SchedulingPolicy) effectiveRules() []SchedulingPolicyRule {
	rules := make([]SchedulingPolicyRule, 0, len(p.Rules)+len(p.PromiseNames))
	rules = append(rules, p.Rules...)
	for _, name := range p.PromiseNames {
		rules = append(rules, SchedulingPolicyRule{PromiseName: name})
	}
	return rules
}

// InitWorkloads controls whether Kratix writes initial placeholder files to the StateStore for this Destination
type InitWorkloads struct {
	// When true, Kratix will write initial directory structure to the StateStore on Destination creation
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=true
	Enabled bool `json:"enabled"`
}

const (
	// if modifying these don't forget to edit below where they are written as a
	// kubebuilder comment for setting the default and Enum values.
	FilepathModeNone             = "none"
	FilepathModeNestedByMetadata = "nestedByMetadata"
	FilepathModeAggregatedYAML   = "aggregatedYAML"
	DestinationCleanupAll        = "all"
	DestinationCleanupNone       = "none"

	SchedulingPolicyTypeAllow = "Allow"
	SchedulingPolicyTypeDeny  = "Deny"
)

type Filepath struct {
	// +kubebuilder:validation:Enum:={nestedByMetadata,aggregatedYAML,none}
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="filepath.mode is immutable"
	// filepath.mode can be set to either:
	// - nestedByMetadata (default): files from the pipeline will be placed in a nested directory structure
	// - aggregatedYAML: all files from all pipelines will be aggregated into a single YAML file
	// - none: file from the pipeline will be placed in a flat directory structure
	// filepath.mode is immutable
	Mode string `json:"mode,omitempty"`

	// +kubebuilder:validation:Optional
	// If filepath.mode is set to aggregatedYAML, this field can be set to
	// specify the filename of the aggregated YAML file.  Defaults to
	// "aggregated.yaml"
	Filename string `json:"filename,omitempty"`
}

// it gets defaulted by the K8s API, but for unit testing it wont be defaulted
// since its not a real k8s api, so it may be empty when running unit tests.
func (d *Destination) GetFilepathMode() string {
	if d.Spec.Filepath.Mode == "" {
		return FilepathModeNestedByMetadata
	}
	return d.Spec.Filepath.Mode
}

func (d *Destination) GetCleanup() string {
	if d.Spec.Cleanup == "" {
		return DestinationCleanupNone
	}
	return d.Spec.Cleanup
}

// AuthorizesWork reports whether a Work for the given Promise is permitted to schedule
// to this Destination, per its SchedulingPolicy. A nil policy allows everything.
//
// isResourceWork distinguishes resource-level Works (which carry a requester namespace)
// from promise-level dependency Works. namespaceLabels are the labels of the requester
// namespace and are only consulted for resource Works.
//
// See SchedulingPolicy for the full Allow/Deny and namespace semantics.
func (d *Destination) AuthorizesWork(promiseName string, isResourceWork bool, namespaceLabels map[string]string) bool {
	policy := d.Spec.SchedulingPolicy
	if policy == nil {
		return true
	}

	// Rules that apply to this Promise (exact name match, or an empty name wildcard).
	var applicable []SchedulingPolicyRule
	for _, r := range policy.effectiveRules() {
		if r.PromiseName == "" || r.PromiseName == promiseName {
			applicable = append(applicable, r)
		}
	}

	if policy.Type == SchedulingPolicyTypeDeny {
		return !deniedByRules(applicable, isResourceWork, namespaceLabels)
	}

	// Allow semantics.
	if len(applicable) == 0 {
		return false // Promise not authorized on this Destination at all.
	}
	if !isResourceWork {
		return true // Dependency Works are authorized by name; namespace rules do not apply.
	}
	// Resource Work: if the Promise has any namespace-scoped rule, the requester
	// namespace must match one of them; an unrestricted rule authorizes any namespace.
	for _, r := range applicable {
		if r.NamespaceSelector == nil {
			return true
		}
		if namespaceSelectorMatches(r.NamespaceSelector, namespaceLabels) {
			return true
		}
	}
	return false
}

// deniedByRules reports whether the applicable Deny rules deny this Work. A
// selector-less rule denies the Promise entirely (both work kinds); a selector-bearing
// rule denies only resource Works whose requester namespace matches, leaving the
// dependency Works untouched.
func deniedByRules(applicable []SchedulingPolicyRule, isResourceWork bool, namespaceLabels map[string]string) bool {
	for _, r := range applicable {
		if r.NamespaceSelector == nil {
			return true
		}
		if isResourceWork && namespaceSelectorMatches(r.NamespaceSelector, namespaceLabels) {
			return true
		}
	}
	return false
}

func namespaceSelectorMatches(sel *metav1.LabelSelector, namespaceLabels map[string]string) bool {
	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return false
	}
	return selector.Matches(labels.Set(namespaceLabels))
}

// DestinationStatus defines the observed state of Destination
type DestinationStatus struct {
	// Current conditions of the Destination. Includes a Ready condition indicating whether the StateStore is accessible
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,path=destinations,categories=kratix,shortName=dest;dests
// +kubebuilder:printcolumn:name="StateStoreKind",type=string,JSONPath=`.spec.stateStoreRef.kind`,description="State store kind."
// +kubebuilder:printcolumn:name="StateStoreName",type=string,JSONPath=`.spec.stateStoreRef.name`,description="State store name."
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Indicates the destination is ready to use."

// Destination is the Schema for the Destinations API
type Destination struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DestinationSpec   `json:"spec,omitempty"`
	Status DestinationStatus `json:"status,omitempty"`
}

const (
	DestinationNotReadyReason = "DestinationNotReady"
	DestinationReadyReason    = "DestinationReady"
)

func (d *Destination) SetReadyCondition(status metav1.ConditionStatus, reason, message string) bool {
	return apimeta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}

func (d *Destination) SetNotReadyStatus(reason, message string) bool {
	return d.SetReadyCondition(
		metav1.ConditionFalse,
		reason,
		message,
	)
}

func (d *Destination) SetReadyStatus(reason, message string) bool {
	return d.SetReadyCondition(
		metav1.ConditionTrue,
		reason,
		message,
	)
}

//+kubebuilder:object:root=true

// DestinationList contains a list of Destination
type DestinationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Destination `json:"items"`
}

// StateStoreReference is a reference to a StateStore used by a Destination
type StateStoreReference struct {
	// Kind of the StateStore resource. Must be either BucketStateStore or GitStateStore
	// +kubebuilder:validation:Enum=BucketStateStore;GitStateStore
	Kind string `json:"kind"`
	// Name of the StateStore resource
	Name string `json:"name"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &Destination{}, &DestinationList{})
		return nil
	})
}
