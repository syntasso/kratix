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

const DependencyReplicas = -1
const ResourceRequestReplicas = 1

// WorkStatus defines the observed state of Work
type WorkStatus struct {
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Work is the Schema for the works API
type Work struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkSpec   `json:"spec,omitempty"`
	Status WorkStatus `json:"status,omitempty"`
}

// WorkSpec defines the desired state of Work
type WorkSpec struct {

	// DestinationSelectors is used for selecting the destination
	DestinationSelectors WorkScheduling `json:"destinationSelectors,omitempty"`

	// -1 denotes dependencies, 1 denotes Resource Request
	Replicas int `json:"replicas,omitempty"`

	WorkloadCoreFields `json:",inline"`
}

type WorkloadCoreFields struct {
	// Workload represents the manifest workload to be deployed on destination
	Workloads []Workload `json:"workloads,omitempty"`

	PromiseName string `json:"promiseName,omitempty"`
	// +optional
	ResourceName string `json:"resourceName,omitempty"`
}

type WorkScheduling struct {
	Promise  []Selector `json:"promise,omitempty"`
	Resource []Selector `json:"resource,omitempty"`
}

func NewPromiseDependenciesWork(promise *Promise) (*Work, error) {
	work := &Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      promise.GetName(),
			Namespace: KratixSystemNamespace,
			Labels:    promise.GenerateSharedLabels(),
		},
		Spec: WorkSpec{
			WorkloadCoreFields: WorkloadCoreFields{
				PromiseName: promise.GetName(),
			},
			Replicas: DependencyReplicas,
			DestinationSelectors: WorkScheduling{
				Promise: promise.Spec.DestinationSelectors,
			},
		},
	}

	yamlBytes, err := promise.Spec.Dependencies.Marshal()
	if err != nil {
		return nil, err
	}

	work.Spec.Workloads = []Workload{
		{
			Content:  string(yamlBytes),
			Filepath: "static/dependencies.yaml",
		},
	}

	return work, nil
}

func (w *Work) IsResourceRequest() bool {
	return w.Spec.Replicas == ResourceRequestReplicas
}

func (w *Work) IsDependency() bool {
	return w.Spec.Replicas == DependencyReplicas
}

func (w *Work) HasScheduling() bool {
	// Work has scheduling if either (or both) Promise or Resource has scheduling set
	return len(w.Spec.DestinationSelectors.Resource) > 0 && len(w.Spec.DestinationSelectors.Resource[0].MatchLabels) > 0 ||
		len(w.Spec.DestinationSelectors.Promise) > 0 && len(w.Spec.DestinationSelectors.Promise[0].MatchLabels) > 0
}

func (w *Work) GetSchedulingSelectors() map[string]string {
	return generateLabelSelectorsFromScheduling(append(w.Spec.DestinationSelectors.Promise, w.Spec.DestinationSelectors.Resource...))
}

// Workload represents the manifest workload to be deployed on destination
type Workload struct {
	// +optional
	Filepath string `json:"filepath,omitempty"`
	Content  string `json:"content,omitempty"`
}

//+kubebuilder:object:root=true

// WorkList contains a list of Work
type WorkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Work `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Work{}, &WorkList{})
}
