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

// Phases reported in DryRunComponentStatus.
const (
	DryRunComponentPending   = "Pending"
	DryRunComponentSucceeded = "Succeeded"
	DryRunComponentFailed    = "Failed"
)

// DryRunStatus defines the observed state of DryRun.
type DryRunStatus struct {
	// Conditions holds status conditions.
	//
	// "Completed" is True once the summary has been written. For a compound request it
	// stays True even when a component failed, because a partial summary is still
	// useful -- so do not gate on it alone.
	//
	// "ComponentsSucceeded" is set only for compound requests, and is False when any
	// component failed or did not finish. That is the condition to gate on: it
	// distinguishes "the run finished" from "the preview is complete".
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Components reports one entry per component dry run raised by this one, so a
	// consumer can see the shape and state of the preview without parsing the summary
	// markdown. Empty for a non-compound request.
	Components []DryRunComponentStatus `json:"components,omitempty"`
}

// DryRunComponentStatus reports a single component dry run raised by a compound one.
type DryRunComponentStatus struct {
	// Promise serving the component request.
	Promise string `json:"promise"`
	// Request is the name of the component resource request.
	Request string `json:"request"`
	// Namespace of the component resource request.
	Namespace string `json:"namespace,omitempty"`
	// DryRun is the name of the DryRun raised for this component.
	DryRun string `json:"dryRun"`
	// Phase is Pending, Succeeded or Failed.
	Phase string `json:"phase"`
	// Message carries the detail when Phase is Failed.
	Message string `json:"message,omitempty"`
}

//+kubebuilder:object:root=true

// DryRunList contains a list of DryRun.
type DryRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DryRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &DryRun{}, &DryRunList{})
		return nil
	})
}
