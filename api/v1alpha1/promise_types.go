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
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"gopkg.in/yaml.v2"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// PromiseSpec defines the desired state of Promise
type PromiseSpec struct {

	// Important: Run "make" to regenerate code after modifying this file

	// TODO (has since been merged!): apiextemnsion.CustomResourceDefinitionSpec struct(s) don't have the required jsontags and
	// cannot be used as a Type. See https://github.com/kubernetes-sigs/controller-tools/pull/528
	// && https://github.com/kubernetes-sigs/controller-tools/issues/291
	//
	// OPA Validation pattern:
	// https://github.com/open-policy-agent/frameworks/blob/1307ba72bce38ee3cf44f94def1bbc41eb4ffa90/constraint/pkg/apis/templates/v1beta1/constrainttemplate_types.go#L46
	// API runtime.RawExtension      `json:"api,omitempty"`

	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:EmbeddedResource
	API runtime.RawExtension `json:"api,omitempty"`

	Workflows Workflows `json:"workflows,omitempty"`

	Dependencies Dependencies `json:"dependencies,omitempty"`

	DestinationSelectors []Selector `json:"destinationSelectors,omitempty"`
}

type Workflows struct {
	Resource WorkflowTriggers `json:"resource,omitempty"`
}

type WorkflowTriggers struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	Configure []unstructured.Unstructured `json:"configure,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Delete []unstructured.Unstructured `json:"delete,omitempty"`
}

type Dependencies []Dependency

// Resources represents the manifest workload to be deployed on Destinations
type Dependency struct {
	// Manifests represents a list of resources to be deployed on the Destination
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	unstructured.Unstructured `json:",inline"`
}

type Selector struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// PromiseStatus defines the observed state of Promise
type PromiseStatus struct {
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster,path=promises

// Promise is the Schema for the promises API
type Promise struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromiseSpec   `json:"spec,omitempty"`
	Status PromiseStatus `json:"status,omitempty"`
}

func (p *Promise) GetSchedulingSelectors() map[string]string {
	return generateLabelSelectorsFromScheduling(p.Spec.DestinationSelectors)
}

func generateLabelSelectorsFromScheduling(scheduling []Selector) map[string]string {
	// TODO: Support more complex scheduling as it is introduced including resource selection and
	//		 different target options.
	schedulingSelectors := map[string]string{}
	for _, schedulingConfig := range scheduling {
		schedulingSelectors = labels.Merge(schedulingConfig.MatchLabels, schedulingSelectors)
	}
	return schedulingSelectors
}

func (p *Promise) DoesNotContainAPI() bool {
	// if a workflow is set but there is not an API the workflow is ignored
	// TODO how can we prevent this scenario from happening
	return p.Spec.API.Raw == nil
}

func (p *Promise) GetAPIAsCRD() (*v1.CustomResourceDefinition, error) {
	if p.DoesNotContainAPI() {
		return nil, fmt.Errorf("promise does not contain an API")
	}

	crd := v1.CustomResourceDefinition{}
	if err := json.Unmarshal(p.Spec.API.Raw, &crd); err != nil {
		return nil, fmt.Errorf("api.metadata.name is immutable")
	}

	return &crd, nil
}

func (p *Promise) ContainsAPI() bool {
	return !p.DoesNotContainAPI()
}

func (p *Promise) GenerateSharedLabels() map[string]string {
	return map[string]string{
		"kratix-promise-id": p.GetName(),
	}
}

func (p *Promise) GetControllerResourceName() string {
	return p.GetName() + "-promise-controller"
}

func (p *Promise) GetPipelineResourceName() string {
	return p.GetName() + "-promise-pipeline"
}

func (p *Promise) GetPipelineResourceNamespace() string {
	return "default"
}

func (d Dependencies) Marshal() ([]byte, error) {
	buf := new(bytes.Buffer)
	encoder := yaml.NewEncoder(buf)
	for _, workload := range d {
		err := encoder.Encode(workload.Unstructured.Object)
		if err != nil {
			return nil, err
		}
	}

	return io.ReadAll(buf)
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
