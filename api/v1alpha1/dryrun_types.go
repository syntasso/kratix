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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:categories=kratix

// DryRun previews the output of a Kratix pipeline without applying it to a real Destination.
type DryRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DryRunSpec   `json:"spec,omitempty"`
	Status DryRunStatus `json:"status,omitempty"`
}

// DryRunSpec defines the desired state of DryRun.
type DryRunSpec struct {
	// PromiseRef is the name of the Promise whose pipeline to dry-run.
	PromiseRef DryRunPromiseRef `json:"promiseRef"`
	// ResourceRequestRef identifies the live ResourceRequest to diff against.
	// When the referenced object is not found the diff treats the request as new (all files added).
	ResourceRequestRef DryRunResourceRequestRef `json:"resourceRequestRef"`
	// Resource is the spec to dry-run, in the shape expected by the Promise's resource API.
	Resource runtime.RawExtension `json:"resource"`
}

// DryRunPromiseRef names a Promise.
type DryRunPromiseRef struct {
	Name string `json:"name"`
}

// DryRunResourceRequestRef names the live ResourceRequest to diff against.
type DryRunResourceRequestRef struct {
	Name string `json:"name"`
	// Namespace of the live ResourceRequest. Defaults to the DryRun's own namespace when omitted.
	Namespace string `json:"namespace,omitempty"`
}

// DryRunStatus defines the observed state of DryRun.
type DryRunStatus struct {
	// Conditions holds status conditions. The "Completed" condition is True once the summary has been written.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true

// DryRunList contains a list of DryRun.
type DryRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DryRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DryRun{}, &DryRunList{})
}
