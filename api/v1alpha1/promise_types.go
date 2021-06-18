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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PromiseSpec defines the desired state of Promise
type PromiseSpec struct {

	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// TODO: apiextemnsion.CustomResourceDefinitionSpec struct(s) don't have the required jsontags and
	// cannot be used as a Type. See https://github.com/kubernetes-sigs/controller-tools/pull/528
	// && https://github.com/kubernetes-sigs/controller-tools/issues/291
	//
	// OPA Validation pattern:
	// https://github.com/open-policy-agent/frameworks/blob/1307ba72bce38ee3cf44f94def1bbc41eb4ffa90/constraint/pkg/apis/templates/v1beta1/constrainttemplate_types.go#L46
	// CRD runtime.RawExtension      `json:"crd,omitempty"`

	// X's CustomResourceDefinition to create the X-aaS offering
	//
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:EmbeddedResource
	CRD runtime.RawExtension `json:"crd,omitempty"`

	// Array of Image tags to transform from input request custom resource to output resource(s)
	RequestPipeline []string `json:"requestPipeline,omitempty"`
}

// PromiseStatus defines the observed state of Promise
type PromiseStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Promise is the Schema for the promises API
type Promise struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromiseSpec   `json:"spec,omitempty"`
	Status PromiseStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PromiseList contains a list of Promise
type PromiseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Promise `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Promise{}, &PromiseList{})
}
