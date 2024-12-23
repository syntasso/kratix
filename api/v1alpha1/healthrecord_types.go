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

type PromiseRef struct {
	Name string `json:"name"`
}

type ResourceRef struct {
	// Name the resource name
	Name string `json:"name"`
	// Namespace the resource namespace
	Namespace string `json:"namespace"`
	// Generation the generation of the resource
	Generation int `json:"generation,omitempty"`
}

// HealthRecordData defines the desired state of HealthRecord
type HealthRecordData struct {
	PromiseRef PromiseRef `json:"promiseRef"`

	// ResourceRef represents the resource request; required value if HealthRecord is for a resource request
	ResourceRef ResourceRef `json:"resourceRef,omitempty"`

	// +kubebuilder:validation:Enum=unknown;ready;unhealthy;healthy;degraded
	// +kubebuilder:default=unknown
	State string `json:"state"`

	// Timestamp of the last healthcheck run
	LastRun string `json:"lastRun,omitempty"`

	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Optional
	Details *runtime.RawExtension `json:"details,omitempty"`
}

//+kubebuilder:object:root=true

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
