/*
Copyright 2025 Syntasso.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"strings"
	"unicode"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	UpgradeSucceededCondition = "UpgradeSucceeded"
	UpgradeInProgressReason   = "UpgradeInProgress"
	UpgradeCompleteReason     = "UpgradeComplete"
	UpgradeFailedReason       = "UpgradeFailed"
)

// ResourceBindingSpec defines the desired state of ResourceBinding
type ResourceBindingSpec struct {
	// Version is the version of the Promise that this ResourceRequest was last reconciled with.
	// +required
	Version string `json:"version"`
	// Reference to the Promise that this binding tracks
	// +required
	PromiseRef PromiseRef `json:"promiseRef"`
	// Reference to the Resource Request that this binding tracks
	// +required
	ResourceRef ResourceRef `json:"resourceRef"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:selectablefield:JSONPath=".spec.version"
// +kubebuilder:resource:scope=Namespaced,path=resourcebindings,categories=kratix
// +kubebuilder:printcolumn:name="Resource",type=string,JSONPath=".spec.resourceRef.name",description="Resource using a Promise"
// +kubebuilder:printcolumn:name="Promise",type=string,JSONPath=".spec.promiseRef.name",description="Promise being used by the Resource"
// +kubebuilder:printcolumn:name="Desired",type=string,JSONPath=".spec.version",description="Promise version the resource should be reconciled with"
// +kubebuilder:printcolumn:name="Applied",type=string,JSONPath=".status.lastAppliedVersion",description="Promise version the resource was last reconciled with"

// ResourceBinding is the Schema for the resourcebindings API
type ResourceBinding struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of ResourceBinding
	// +required
	Spec ResourceBindingSpec `json:"spec"`

	// status defines the observed state of ResourceBinding
	// +optional
	Status ResourceBindingStatus `json:"status,omitempty,omitzero"`
}

// ResourceBindingStatus defines the observed state of ResourceBinding.
type ResourceBindingStatus struct {
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Reference to the Resource Request version
	// +optional
	LastAppliedVersion string `json:"lastAppliedVersion,omitempty"`
	// The promise version that was being attempted when the last upgrade failure occurred.
	// Cleared when an upgrade succeeds or a new upgrade attempt begins.
	// +optional
	FailedVersion string `json:"failedVersion,omitempty"`
}

// +kubebuilder:object:root=true

// ResourceBindingList contains a list of ResourceBinding
type ResourceBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &ResourceBinding{}, &ResourceBindingList{})
		return nil
	})
}

// InFlightVersion returns the promise version of the resource configure
// workflow that is currently being attempted for this binding, or an empty
// string if no upgrade is in flight.
//
// It is inferred from the UpgradeSucceeded condition: while an upgrade is in
// progress that condition is set to Unknown with reason UpgradeInProgress, and
// its message embeds the desired version after the literal token "version ".
// The controller is the sole writer of that condition, so coupling the parser
// to the message format is acceptable as long as both stay in step.
func (rb *ResourceBinding) InFlightVersion() string {
	cond := apiMeta.FindStatusCondition(rb.Status.Conditions, UpgradeSucceededCondition)
	if cond == nil {
		return ""
	}
	if cond.Status != metav1.ConditionUnknown || cond.Reason != UpgradeInProgressReason {
		return ""
	}

	const marker = "version "
	idx := strings.Index(cond.Message, marker)
	if idx == -1 {
		return ""
	}
	rest := cond.Message[idx+len(marker):]
	if i := strings.IndexFunc(rest, unicode.IsSpace); i != -1 {
		rest = rest[:i]
	}
	return rest
}
