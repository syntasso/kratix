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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ResourceMetadataSpec defines the desired state of ResourceMetadata
type ResourceMetadataSpec struct {
	Version     string           `json:"revision"`
	GVK         GroupVersionKind `json:"gvk"`
	PromiseRef  *PromiseRef      `json:"promiseRef"`
	ResourceRef *ResourceRef     `json:"resourceRef"`
}

type GroupVersionKind struct {
	Group   string `json:"group"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

// ResourceMetadataStatus defines the observed state of ResourceMetadata.
type ResourceMetadataStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the ResourceMetadata resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	History []ResourceMetadataHistory `json:"history,omitempty"`
}

type ResourceMetadataHistory struct {
	Version   string      `json:"version"`
	UpdatedAt metav1.Time `json:"updatedAt"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ResourceMetadata is the Schema for the resourcemetadata API
type ResourceMetadata struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of ResourceMetadata
	// +required
	Spec ResourceMetadataSpec `json:"spec"`

	// status defines the observed state of ResourceMetadata
	// +optional
	Status ResourceMetadataStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// ResourceMetadataList contains a list of ResourceMetadata
type ResourceMetadataList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceMetadata `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ResourceMetadata{}, &ResourceMetadataList{})
}
