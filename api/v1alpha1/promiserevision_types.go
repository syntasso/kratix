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

// PromiseRevisionSpec defines the desired state of PromiseRevision
type PromiseRevisionSpec struct {
	// PromiseRef is the reference to the Promise this revision is based on.
	// +required
	PromiseRef PromiseRef `json:"promiseRef"`

	// PromiseSpec is the Spec of the Promise this revision is based on.
	// +required
	PromiseSpec PromiseSpec `json:"promiseSpec"`
	// Version is the version of the Promise this revision is based on.
	// +required
	Version string `json:"version"`
}

// PromiseRevisionStatus defines the observed state of PromiseRevision.
type PromiseRevisionStatus struct {
	// Latest is true if this revision is the latest revision for the Promise. Only one revision can be the latest at a time.
	Latest bool `json:"latest,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,path=promiserevisions,categories=kratix
// +kubebuilder:subresource:status

// PromiseRevision is the Schema for the promiserevisions API
type PromiseRevision struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of PromiseRevision
	// +required
	Spec PromiseRevisionSpec `json:"spec"`

	// status defines the observed state of PromiseRevision
	// +optional
	Status PromiseRevisionStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// PromiseRevisionList contains a list of PromiseRevision
type PromiseRevisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromiseRevision `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PromiseRevision{}, &PromiseRevisionList{})
}

func (pr *PromiseRevision) GetPromiseName() string {
	return pr.Spec.PromiseRef.Name
}
