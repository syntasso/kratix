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

// PromiseReleaseSpec defines the desired state of PromiseRelease
type PromiseReleaseSpec struct {
	Version   string    `json:"version,omitempty"`
	SourceRef SourceRef `json:"sourceRef,omitempty"`
}

type SourceRef struct {
	Type string `json:"type,omitempty"`
	URL  string `json:"url,omitempty"`
}

// PromiseReleaseStatus defines the observed state of PromiseRelease
type PromiseReleaseStatus struct {
	Installed bool `json:"installed,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster,path=promisereleases
//+kubebuilder:printcolumn:JSONPath=".status.installed",name="Installed",type=boolean
//+kubebuilder:printcolumn:JSONPath=".spec.version",name="Version",type=string

// PromiseRelease is the Schema for the promisereleases API
type PromiseRelease struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromiseReleaseSpec   `json:"spec,omitempty"`
	Status PromiseReleaseStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PromiseReleaseList contains a list of PromiseRelease
type PromiseReleaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromiseRelease `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PromiseRelease{}, &PromiseReleaseList{})
}
