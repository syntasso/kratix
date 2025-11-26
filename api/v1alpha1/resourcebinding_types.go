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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceBindingSpec defines the desired state of ResourceBinding
type ResourceBindingSpec struct {
	// Version is the version of the Promise that this ResourceRequest was last reconciled with.
	// +required
	Version string `json:"version"`
	// +required
	PromiseRef PromiseRef `json:"promiseRef"`
	// +required
	ResourceRef ResourceRef `json:"resourceRef"`
}

// ResourceBindingStatus defines the observed state of ResourceBinding.
type ResourceBindingStatus struct {
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:selectablefield:JSONPath=".spec.version"

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

// +kubebuilder:object:root=true

// ResourceBindingList contains a list of ResourceBinding
type ResourceBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ResourceBinding{}, &ResourceBindingList{})
}
