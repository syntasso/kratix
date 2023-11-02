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
	"github.com/syntasso/kratix/lib/hash"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const DependencyReplicas = -1
const ResourceRequestReplicas = 1
const DefaultWorkloadGroupDirectory = "."

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
	// -1 denotes dependencies, 1 denotes Resource Request
	Replicas int `json:"replicas,omitempty"`

	WorkloadCoreFields `json:",inline"`
}

type WorkloadCoreFields struct {
	// Workload represents the manifest workload to be deployed on destination
	WorkloadGroups []WorkloadGroup `json:"workloadGroups,omitempty"`

	PromiseName string `json:"promiseName,omitempty"`
	// +optional
	ResourceName string `json:"resourceName,omitempty"`
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
		},
	}

	yamlBytes, err := promise.Spec.Dependencies.Marshal()
	if err != nil {
		return nil, err
	}

	work.Spec.WorkloadGroups = []WorkloadGroup{
		{
			ID:        hash.ComputeHash(DefaultWorkloadGroupDirectory),
			Directory: DefaultWorkloadGroupDirectory,
			Workloads: []Workload{
				{
					Content:  string(yamlBytes),
					Filepath: "static/dependencies.yaml",
				},
			},
		},
	}

	if len(promise.Spec.DestinationSelectors) > 0 {
		work.Spec.WorkloadGroups[0].DestinationSelectors = []WorkloadGroupScheduling{
			{
				MatchLabels: SquashPromiseScheduling(promise.Spec.DestinationSelectors),
				Source:      "promise",
			},
		}
	}

	return work, nil
}

func (w *Work) IsResourceRequest() bool {
	return w.Spec.Replicas == ResourceRequestReplicas
}

func (w *Work) IsDependency() bool {
	return w.Spec.Replicas == DependencyReplicas
}

// WorkloadGroup represents the workloads in a particular directory that should
// be scheduled to a to Destination
type WorkloadGroup struct {
	// +optional
	Workloads            []Workload                `json:"workloads,omitempty"`
	Directory            string                    `json:"directory,omitempty"`
	ID                   string                    `json:"id,omitempty"`
	DestinationSelectors []WorkloadGroupScheduling `json:"destinationSelectors,omitempty"`
}

type WorkloadGroupScheduling struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
	Source      string            `json:"source,omitempty"`
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
