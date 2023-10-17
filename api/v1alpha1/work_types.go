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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
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

	WorkloadGroups []WorkloadGroup `json:"workloadGroups,omitempty"`
}

type WorkloadGroup struct {
	DestinationSelectorsOverride []Selector `json:"destinationSelectorsOverride,omitempty"`
	WorkloadCoreFields           `json:",inline"`
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

type DependencySelectorGroup struct {
	selector     *Selector
	dependencies Dependencies
}

// Note: The efficiency of this function is based on an assumption that both the
// number of selectors and dependencies will be small. The efficiency is O(n*m)
// where n is the number of selectors and m is the number of dependencies.
// The benefit of this solution is that we keep the number of workplacements as
// low as possible. By allowing a more relaxed grouping (i.e. doing a string
// compare on the selector instead of a deep equals) we could increase
// efficieny greatly.
func NewPromiseDependenciesWork(promise *Promise) (*Work, error) {
	dependenciesGroup, err := SplitDependenciesBySelector(promise.Spec.Dependencies)
	if err != nil {
		return nil, err
	}

	workloadGroupList, err := MergeDependencyGroupIntoExistingWorkloadGroup(dependenciesGroup, nil, promise.GetName(), "static/dependencies.yaml")
	if err != nil {
		return nil, err
	}

	work := &Work{
		ObjectMeta: metav1.ObjectMeta{
			Name:      promise.GetName(),
			Namespace: KratixSystemNamespace,
			Labels:    promise.GenerateSharedLabels(),
		},
		Spec: WorkSpec{
			WorkloadGroups: workloadGroupList,
			Replicas:       DependencyReplicas,
			DestinationSelectors: WorkScheduling{
				Promise: promise.Spec.DestinationSelectors,
			},
		},
	}

	return work, nil
}

func MergeDependencyGroupIntoExistingWorkloadGroup(dependenciesGroup []DependencySelectorGroup, workloadGroupList []WorkloadGroup, promiseName, fileName string) ([]WorkloadGroup, error) {
	for i, dep := range dependenciesGroup {
		var override []Selector

		filePath := fileName
		if dep.selector != nil {
			override = []Selector{*dep.selector}
			filePath = fmt.Sprintf("%s.%d.yaml", strings.TrimSuffix(fileName, ".yaml"), i)
		}

		yamlBytes, err := dep.dependencies.Marshal()
		if err != nil {
			return nil, err
		}

		workload := Workload{
			Content:  string(yamlBytes),
			Filepath: filePath,
		}

		var found bool
		for i, existingWorkloadGroup := range workloadGroupList {
			if selectorsMatch(existingWorkloadGroup.DestinationSelectorsOverride, override) {
				workloadGroupList[i].Workloads = append(existingWorkloadGroup.Workloads, workload)
				found = true
				break
			}
		}

		if found {
			continue
		}

		workloadGroupList = append(workloadGroupList, WorkloadGroup{
			DestinationSelectorsOverride: override,
			WorkloadCoreFields: WorkloadCoreFields{
				PromiseName: promiseName,
				Workloads:   []Workload{workload},
			},
		})
	}

	return workloadGroupList, nil
}

func selectorsMatch(a, b []Selector) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}

	if len(a) != len(b) {
		return false
	}

	return a[0].Equals(&b[0])
}

func SplitDependenciesBySelector(deps Dependencies) ([]DependencySelectorGroup, error) {
	dependenciesGroup := []DependencySelectorGroup{}

	for _, dep := range deps {
		var selector *Selector

		annotations := dep.GetAnnotations()
		if override, found := annotations[DestinationSelectorsOverride]; found {
			selector = &Selector{}
			if err := yaml.Unmarshal([]byte(override), selector); err != nil {
				return nil, err
			}
		}

		var found bool
		for i, depGroup := range dependenciesGroup {
			if depGroup.selector.Equals(selector) {
				dependenciesGroup[i].dependencies = append(dependenciesGroup[i].dependencies, dep)
				found = true
				break
			}
		}

		if !found {
			dependenciesGroup = append(dependenciesGroup, DependencySelectorGroup{selector: selector, dependencies: []Dependency{dep}})
		}
	}

	return dependenciesGroup, nil
}

func (w *Work) MergeWorkloadGroups(workloadGroups []WorkloadGroup) {
	groupIndexMap := map[*Selector]int{}
	for i, group := range w.Spec.WorkloadGroups {
		if len(group.DestinationSelectorsOverride) == 0 {
			groupIndexMap[nil] = i
			continue
		}

		groupIndexMap[&group.DestinationSelectorsOverride[0]] = i
	}

	for _, group := range workloadGroups {
		var selector *Selector
		if len(group.DestinationSelectorsOverride) != 0 {
			selector = &group.DestinationSelectorsOverride[0]
		}
		index, found := groupIndexMap[selector]
		if !found {
			w.Spec.WorkloadGroups = append(w.Spec.WorkloadGroups, group)
			continue
		}
		w.Spec.WorkloadGroups[index].Workloads = append(w.Spec.WorkloadGroups[index].Workloads, group.Workloads...)
	}

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
