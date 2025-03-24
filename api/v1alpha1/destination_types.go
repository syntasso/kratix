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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type StateStoreCoreFields struct {
	// Path within the StateStore to write documents. This path should be allocated
	// to Kratix as it will create, update, and delete files within this path.
	// Path structure begins with provided path and ends with namespaced destination name:
	//   <StateStore.Spec.Path>/<Destination.Spec.Path>/<Destination.Metadata.Namespace>/<Destination.Metadata.Name>/
	//+kubebuilder:validation:Optional
	Path string `json:"path,omitempty"`
	// SecretRef specifies the Secret containing authentication credentials
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`
}

// TODO: revisit if we want all destination secrets on a single known namespaces
// (i.e. kratix-platform-system) or if we want to allow users to specify a
// namespace for each destination secret.

// DestinationSpec defines the desired state of Destination.
type DestinationSpec struct {
	// Path within StateStore to write documents, this will be appended to any
	// specficed Spec.Path provided in the referenced StateStore.
	// The Path structure will be:
	//   <StateStore.Spec.Path>/<Destination.Spec.Path>/
	// Kratix may create other subdirectories, depending on the Filepath.mode you select.
	// To write to the root of the StateStore.Spec.Path, set Path to "." or "/".
	// Defaults to the Destination name
	// +kubebuilder:validation:Required
	Path string `json:"path,omitempty"`

	StateStoreRef *StateStoreReference `json:"stateStoreRef,omitempty"`

	// By default, Kratix will schedule works without labels to all destinations
	// (for promise dependencies) or to a random destination (for resource
	// requests). If StrictMatchLabels is true, Kratix will only schedule works
	// to this destination if it can be selected by the Promise's
	// destinationSelectors. An empty label set on the work won't be scheduled
	// to this destination, unless the destination label set is also empty
	// +kubebuilder:validation:Optional
	StrictMatchLabels bool `json:"strictMatchLabels,omitempty"`

	//The filepath mode to use when writing files to the destination.
	// +kubebuilder:default:={mode: "nestedByMetadata"}
	Filepath Filepath `json:"filepath,omitempty"`

	// cleanup can be set to either:
	// - none (default): no cleanup after removing the destination
	// - all: workplacements and statestore contents will be removed after removing the destination
	// +kubebuilder:validation:Enum:={none,all}
	// +kubebuilder:default:="none"
	Cleanup string `json:"cleanup,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default:={}
	InitWorkloads InitWorkloads `json:"initWorkloads"`
}

type InitWorkloads struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default:=true
	Enabled bool `json:"enabled,omitempty"`
}

const (
	// if modifying these dont forget to edit below where they are written as a
	// kubebuilder comment for setting the default and Enum values.
	FilepathModeNone             = "none"
	FilepathModeNestedByMetadata = "nestedByMetadata"
	FilepathModeAggregatedYAML   = "aggregatedYAML"
	DestinationCleanupAll        = "all"
	DestinationCleanupNone       = "none"
)

type Filepath struct {
	// +kubebuilder:validation:Enum:={nestedByMetadata,aggregatedYAML,none}
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="filepath.mode is immutable"
	// filepath.mode can be set to either:
	// - nestedByMetadata (default): files from the pipeline will be placed in a nested directory structure
	// - aggregatedYAML: all files from all pipeliens will be aggregated into a single YAML file
	// - none: file from the pipeline will be placed in a flat directory structure
	// filepath.mode is immutable
	Mode string `json:"mode,omitempty"`

	// +kubebuilder:validation:Optional
	// If filepath.mode is set to aggregatedYAML, this field can be set to
	// specify the filename of the aggregated YAML file.  Defaults to
	// "aggregated.yaml"
	Filename string `json:"filename,omitempty"`
}

// it gets defaulted by the K8s API, but for unit testing it wont be defaulted
// since its not a real k8s api, so it may be empty when running unit tests.
func (d *Destination) GetFilepathMode() string {
	if d.Spec.Filepath.Mode == "" {
		return FilepathModeNestedByMetadata
	}
	return d.Spec.Filepath.Mode
}

func (d *Destination) GetCleanup() string {
	if d.Spec.Cleanup == "" {
		return DestinationCleanupNone
	}
	return d.Spec.Cleanup
}

// DestinationStatus defines the observed state of Destination
type DestinationStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,path=destinations,categories=kratix
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Indicates the destination is ready to use"

// Destination is the Schema for the Destinations API
type Destination struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DestinationSpec   `json:"spec,omitempty"`
	Status DestinationStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DestinationList contains a list of Destination
type DestinationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Destination `json:"items"`
}

// StateStoreReference is a reference to a StateStore
type StateStoreReference struct {
	// +kubebuilder:validation:Enum=BucketStateStore;GitStateStore
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func init() {
	SchemeBuilder.Register(&Destination{}, &DestinationList{})
}
