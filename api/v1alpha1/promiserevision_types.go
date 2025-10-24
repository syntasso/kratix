/*
Copyright 2021 Syntasso.

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

// PromiseRevisionSpec defines the desired state of PromiseRevision.
type PromiseRevisionSpec struct {
	Version     string      `json:"version,omitempty"`
	PromiseSpec PromiseSpec `json:"promise"`
}

// PromiseRevisionStatus defines the observed state of PromiseRevision.
type PromiseRevisionStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster,path=promiserevisions,categories=kratix
// +kubebuilder:subresource:status

// PromiseRevision is the Schema for the promiserevisions API.
type PromiseRevision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromiseRevisionSpec   `json:"spec,omitempty"`
	Status PromiseRevisionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PromiseRevisionList contains a list of PromiseRevision.
type PromiseRevisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []PromiseRevision `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PromiseRevision{}, &PromiseRevisionList{})
}
