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

	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/hash"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DefaultWorkloadGroupDirectory        = "."
	KratixPrefix                         = "kratix.io/"
	PromiseVersionLabel                  = KratixPrefix + "promise-version"
	PromiseNameLabel                     = KratixPrefix + "promise-name"
	ResourceNameLabel                    = KratixPrefix + "resource-name"
	PipelineNameLabel                    = KratixPrefix + "pipeline-name"
	PipelineNamespaceLabel               = KratixPrefix + "pipeline-namespace"
	WorkTypeLabel                        = KratixPrefix + "work-type"
	WorkActionLabel                      = KratixPrefix + "work-action"
	UserPermissionResourceNamespaceLabel = KratixPrefix + "resource-namespace"

	// Job annotations for cross-namespace resource identification
	JobResourceNamespaceAnnotation  = KratixPrefix + "job-resource-namespace"
	JobResourceNameAnnotation       = KratixPrefix + "job-resource-name"
	JobResourceKindAnnotation       = KratixPrefix + "job-resource-kind"
	JobResourceAPIVersionAnnotation = KratixPrefix + "job-resource-api-version"

	WorkTypePromise          = "promise"
	WorkTypeResource         = "resource"
	WorkTypeStaticDependency = "static-dependency"
)

// WorkStatus defines the observed state of Work
type WorkStatus struct {
	Conditions            []metav1.Condition `json:"conditions,omitempty"`
	WorkPlacements        int                `json:"workPlacements,omitempty"`
	WorkPlacementsCreated int                `json:"workPlacementsCreated,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:categories=kratix
//+kubebuilder:printcolumn:name="STATUS",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].message`,description="Status of this Work."

// Work is the Schema for the works API
type Work struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkSpec   `json:"spec,omitempty"`
	Status WorkStatus `json:"status,omitempty"`
}

// WorkSpec defines the desired state of Work
type WorkSpec struct {
	// Workload represents the manifest workload to be deployed on destination
	WorkloadGroups []WorkloadGroup `json:"workloadGroups,omitempty"`

	PromiseName string `json:"promiseName,omitempty"`
	// +optional
	ResourceName string `json:"resourceName,omitempty"`
}

func NewPromiseDependenciesWork(promise *Promise, name string) (*Work, error) {
	work := &Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: SystemNamespace,
			Labels:    promise.GenerateSharedLabels(),
		},
		Spec: WorkSpec{
			PromiseName: promise.GetName(),
		},
	}

	yamlBytes, err := promise.Spec.Dependencies.Marshal()
	if err != nil {
		return nil, err
	}

	workContent, err := compression.CompressContent(yamlBytes)
	if err != nil {
		return nil, err
	}

	work.Spec.WorkloadGroups = []WorkloadGroup{
		{
			ID:        hash.ComputeHash(DefaultWorkloadGroupDirectory),
			Directory: DefaultWorkloadGroupDirectory,
			Workloads: []Workload{
				{
					Content:  string(workContent),
					Filepath: fmt.Sprintf("static/%s-dependencies.yaml", promise.GetName()),
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
	return w.Spec.ResourceName != ""
}

func (w *Work) IsDependency() bool {
	return w.Spec.ResourceName == ""
}

// WorkloadGroup represents the workloads in a particular directory that should
// be scheduled to a Destination
type WorkloadGroup struct {
	// +optional
	// List of Workloads scheduled to target Destination;
	// Each Workload details name of the filepath on Destination,
	// and the compressed content of the workload.
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
	// Content of the workload, which is base64 encoded and compressed with gzip.
	Content string `json:"content,omitempty"`
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

// Returns the WorkloadGroupScheduling for the given source and directory
func (w *Work) GetWorkloadGroupScheduling(source, directory string) *WorkloadGroupScheduling {
	var promiseWorkflowSelectors *WorkloadGroupScheduling
	for _, wg := range w.Spec.WorkloadGroups {
		if wg.Directory == directory {
			for _, selectors := range wg.DestinationSelectors {
				if selectors.Source == source {
					promiseWorkflowSelectors = &selectors
					break
				}
			}
			break
		}
	}
	return promiseWorkflowSelectors
}

func (w *Work) GetDefaultScheduling(source string) *WorkloadGroupScheduling {
	return w.GetWorkloadGroupScheduling(source, DefaultWorkloadGroupDirectory)
}
