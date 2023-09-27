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
	"k8s.io/apimachinery/pkg/labels"
)

// WorkPlacementSpec defines the desired state of WorkPlacement
type WorkPlacementSpec struct {
	TargetDestinationName string `json:"targetDestinationName,omitempty"`

	WorkloadCoreFields `json:",inline"`
}

// WorkPlacementStatus defines the observed state of WorkPlacement
type WorkPlacementStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

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

const (
	WorkLabelKey      = KratixPrefix + "work"
	MisscheduledLabel = KratixPrefix + "misscheduled"
)

func init() {
	SchemeBuilder.Register(&WorkPlacement{}, &WorkPlacementList{})
}

func (w *WorkPlacement) SetMisscheduledLabel() {
	w.SetLabels(
		labels.Merge(w.Labels, map[string]string{MisscheduledLabel: "true"}),
	)
}

func NewWorkplacementListForWork(work *Work, workloadIndex int, destinations []string) *WorkPlacementList {
	workPlacementList := &WorkPlacementList{
		Items: []WorkPlacement{},
	}
	for _, targetDestinationName := range destinations {
		workPlacement := &WorkPlacement{}
		workPlacement.Namespace = work.GetNamespace()
		workPlacement.Name = fmt.Sprintf("%s.%s", work.Name, targetDestinationName)

		corefields := work.Spec.WorkloadGroups[workloadIndex].WorkloadCoreFields
		workPlacement.Spec.Workloads = corefields.Workloads
		workPlacement.Labels = map[string]string{
			WorkLabelKey: work.Name,
		}

		workPlacement.Spec.WorkloadCoreFields = corefields
		workPlacement.Spec.TargetDestinationName = targetDestinationName
		workPlacementList.Items = append(workPlacementList.Items, *workPlacement)
	}
	return workPlacementList
}

func (w *WorkPlacementList) MergeWorkloads(workPlacementList *WorkPlacementList) {
	destWorkloadMap := map[string]int{}
	for i, workPlacement := range w.Items {
		destWorkloadMap[workPlacement.Spec.TargetDestinationName] = i
	}

	for _, workPlacement := range workPlacementList.Items {
		if i, ok := destWorkloadMap[workPlacement.Spec.TargetDestinationName]; ok {
			w.Items[i].Spec.Workloads = append(w.Items[i].Spec.Workloads, workPlacement.Spec.Workloads...)
			continue
		}
		w.Items = append(w.Items, workPlacement)
	}
}

func (w *WorkPlacementList) Merge(workPlacementList *WorkPlacementList) {
	alreadyPresent := map[string]bool{}
	for _, workPlacement := range w.Items {
		alreadyPresent[workPlacement.Spec.TargetDestinationName] = true
	}

	for _, workPlacement := range workPlacementList.Items {
		if !alreadyPresent[workPlacement.Spec.TargetDestinationName] {
			w.Items = append(w.Items, workPlacement)
		}
	}
}

func (w *WorkPlacementList) GroupByDestinationName() map[string]WorkPlacement {
	m := map[string]WorkPlacement{}
	for _, workPlacement := range w.Items {
		m[workPlacement.Spec.TargetDestinationName] = workPlacement
	}
	return m
}
