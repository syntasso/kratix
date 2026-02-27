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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkPlacementSpec defines the desired state of WorkPlacement
type WorkPlacementSpec struct {
	// Name of the Destination this WorkPlacement is assigned to
	TargetDestinationName string `json:"targetDestinationName,omitempty"`
	// List of workloads to be written to the target Destination's StateStore
	Workloads []Workload `json:"workloads,omitempty"`
	// Name of the Promise that generated the parent Work
	PromiseName string `json:"promiseName,omitempty"`
	// Name of the Resource Request that generated the parent Work. Empty for Promise-level dependencies
	// +optional
	ResourceName string `json:"resourceName,omitempty"`
	// Unique identifier for the workload group this WorkPlacement represents
	ID string `json:"id,omitempty"`

	// Generation of the parent Work
	WorkGeneration int64 `json:"generation,omitempty"`
}

// WorkPlacementStatus defines the observed state of WorkPlacement
type WorkPlacementStatus struct {
	// Current conditions of the WorkPlacement. Includes a Ready condition indicating all workloads are written to the StateStore
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	// VersionID contains the version identifier of the last applied workplacement
	// For Git StateStores, this is the SHA of the last applied commit
	// For Bucket StateStores, this is always empty
	VersionID string `json:"versionID,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=kratix,shortName=wp;wps
// +kubebuilder:printcolumn:name="DESTINATION",type=string,JSONPath=`.spec.targetDestinationName`,description="Destination this Workplacement is scheduled to."
// +kubebuilder:printcolumn:name="STATUS",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].message`,description="Status of this Workplacement"
// WorkPlacement is the Schema for the workplacements API
type WorkPlacement struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkPlacementSpec   `json:"spec,omitempty"`
	Status WorkPlacementStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkPlacementList contains a list of WorkPlacement
type WorkPlacementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkPlacement `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkPlacement{}, &WorkPlacementList{})
}

func (w *WorkPlacement) SetPipelineName(work *Work) {
	pipelineName := work.GetLabels()[PipelineNameLabel]
	w.Labels[PipelineNameLabel] = pipelineName
}

func (w *WorkPlacement) PipelineName() string {
	return w.GetLabels()[PipelineNameLabel]
}

func (w *WorkPlacement) GetUniqueID() string {
	return fmt.Sprintf("%s-%s", w.Namespace, w.Name)
}
