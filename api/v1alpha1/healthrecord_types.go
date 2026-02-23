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
	"k8s.io/apimachinery/pkg/runtime"
)

// PromiseRef is a reference to a Promise by name
type PromiseRef struct {
	// Name of the referenced Promise
	Name string `json:"name"`
}

// ResourceRef is a reference to a specific Resource Request
type ResourceRef struct {
	// Name of the Resource Request
	Name string `json:"name"`
	// Namespace of the Resource Request
	Namespace string `json:"namespace"`
	// Generation of the Resource Request at the time of recording
	Generation int `json:"generation,omitempty"`
}

// HealthRecordData defines the desired state of HealthRecord
type HealthRecordData struct {
	// Reference to the Promise this health record belongs to
	PromiseRef PromiseRef `json:"promiseRef"`

	// Reference to the Resource Request this health record belongs to. Required when the HealthRecord is for a resource request
	ResourceRef ResourceRef `json:"resourceRef,omitempty"`

	// Health state of the resource; one of unknown, ready, unhealthy, healthy, or degraded
	// +kubebuilder:validation:Enum=unknown;ready;unhealthy;healthy;degraded
	// +kubebuilder:default=unknown
	State string `json:"state"`

	// Unix timestamp of the last healthcheck run
	LastRun int64 `json:"lastRun,omitempty"`

	// Arbitrary JSON details from the healthcheck pipeline
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Optional
	Details *runtime.RawExtension `json:"details,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.data.state`,description="Shows the HealthRecord state"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,description="When was the HealthRecord created"

// HealthRecord is the Schema for the healthrecords API
type HealthRecord struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Data HealthRecordData `json:"data,omitempty"`
}

//+kubebuilder:object:root=true

// HealthRecordList contains a list of HealthRecord
type HealthRecordList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HealthRecord `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HealthRecord{}, &HealthRecordList{})
}
