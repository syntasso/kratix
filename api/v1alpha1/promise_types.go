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
	"strconv"

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	PromiseStatusAvailable   = "Available"
	PromiseStatusUnavailable = "Unavailable"
	PromisePlural            = "promises"
	KratixResourceHashLabel  = "kratix.io/hash"
)

// PromiseSpec defines the desired state of Promise
type PromiseSpec struct {

	// API an application developers will use to request a Resource from this Promise.
	// Must be a valid kubernetes custom resource definition.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:EmbeddedResource
	// +kubebuilder:validation:Optional
	API *runtime.RawExtension `json:"api,omitempty"`

	// A list of pipelines to be executed at different stages of the Promise lifecycle.
	Workflows Workflows `json:"workflows,omitempty"`

	// A list of Promises that are required by this Promise.
	// All required Promises must be present and available for this promise to be made available.
	RequiredPromises []RequiredPromise `json:"requiredPromises,omitempty"`

	// A collection of prerequisites that enable the creation of a Resource.
	Dependencies Dependencies `json:"dependencies,omitempty"`

	// A list of key and value pairs (labels) used for scheduling.
	DestinationSelectors []PromiseScheduling `json:"destinationSelectors,omitempty"`
}

type RequiredPromise struct {
	// Name of Promise
	Name string `json:"name,omitempty"`
	// Version of Promise
	Version string `json:"version,omitempty"`
}

type Workflows struct {
	Resource WorkflowTriggers `json:"resource,omitempty"`
	Promise  WorkflowTriggers `json:"promise,omitempty"`
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

type PromiseScheduling struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// For /kratix/metadata/destination-selectors.yaml
type WorkflowDestinationSelectors struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
	// +optional
	Directory string `json:"directory,omitempty"`
}

// PromiseStatus defines the observed state of Promise
type PromiseStatus struct {
	Conditions         []metav1.Condition      `json:"conditions,omitempty"`
	Version            string                  `json:"version,omitempty"`
	ObservedGeneration int64                   `json:"observedGeneration,omitempty"`
	Kind               string                  `json:"kind,omitempty"`
	APIVersion         string                  `json:"apiVersion,omitempty"`
	Status             string                  `json:"status,omitempty"`
	RequiredPromises   []RequiredPromiseStatus `json:"requiredPromises,omitempty"`
	RequiredBy         []RequiredBy            `json:"requiredBy,omitempty"`
	LastAvailableTime  *metav1.Time            `json:"lastAvailableTime,omitempty"`
}

type PromiseSummary struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

type RequiredBy struct {
	Promise         PromiseSummary `json:"promise,omitempty"`
	RequiredVersion string         `json:"requiredVersion,omitempty"`
}

type RequiredPromiseStatus struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	State   string `json:"state,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster,path=promises,categories=kratix
//+kubebuilder:printcolumn:JSONPath=".status.status",name="Status",type=string
//+kubebuilder:printcolumn:JSONPath=".status.kind",name=Kind,type=string
//+kubebuilder:printcolumn:JSONPath=".status.apiVersion",name="API Version",type=string
//+kubebuilder:printcolumn:JSONPath=".status.version",name="Version",type=string

// Promise is the Schema for the promises API
type Promise struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromiseSpec   `json:"spec,omitempty"`
	Status PromiseStatus `json:"status,omitempty"`
}

var ErrNoAPI = fmt.Errorf("promise does not contain an API")

func SquashPromiseScheduling(scheduling []PromiseScheduling) map[string]string {
	if len(scheduling) == 0 {
		return nil
	}

	labels := map[string]string{}
	//Reverse order, first item in the array gets priority this way
	for i := len(scheduling) - 1; i >= 0; i-- {
		for key, value := range scheduling[i].MatchLabels {
			labels[key] = value
		}
	}
	return labels
}

func (p *Promise) GetSchedulingSelectors() map[string]string {
	return generateLabelSelectorsFromScheduling(p.Spec.DestinationSelectors)
}

func generateLabelSelectorsFromScheduling(scheduling []PromiseScheduling) map[string]string {
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
	return p.Spec.API == nil || p.Spec.API.Raw == nil
}

func (p *Promise) GetAPIAsCRD() (*apiextensionsv1.CustomResourceDefinition, error) {
	if p.DoesNotContainAPI() {
		return nil, ErrNoAPI
	}

	crd := apiextensionsv1.CustomResourceDefinition{}
	if err := json.Unmarshal(p.Spec.API.Raw, &crd); err != nil {
		return nil, fmt.Errorf("api is not a valid CRD: %w", err)
	}

	return &crd, nil
}

func (p *Promise) ContainsAPI() bool {
	return !p.DoesNotContainAPI()
}

func (p *Promise) GenerateSharedLabels() map[string]string {
	return GenerateSharedLabelsForPromise(p.Name)
}

func GenerateSharedLabelsForPromise(promiseName string) map[string]string {
	return map[string]string{
		PromiseNameLabel: promiseName,
	}
}

func (p *Promise) GetControllerResourceName() string {
	return p.GetName() + "-promise-controller"
}

func (p *Promise) GetPipelineResourceName() string {
	return p.GetName() + "-resource-pipeline"
}

func (p *Promise) GetPipelineResourceNamespace() string {
	return "default"
}

func (p *Promise) ToUnstructured() (*unstructured.Unstructured, error) {
	objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(p)
	if err != nil {
		return nil, err
	}
	unstructuredPromise := &unstructured.Unstructured{Object: objMap}

	return unstructuredPromise, nil
}

func (d Dependencies) Marshal() ([]byte, error) {
	buf := new(bytes.Buffer)
	encoder := yaml.NewEncoder(buf)
	for _, workload := range d {
		err := encoder.Encode(workload.Object)
		if err != nil {
			return nil, err
		}
	}

	return io.ReadAll(buf)
}

func (p *Promise) GetCondition(conditionType string) *metav1.Condition {
	for i := range p.Status.Conditions {
		if p.Status.Conditions[i].Type == conditionType {
			return &p.Status.Conditions[i]
		}
	}
	return nil
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

func (p *Promise) GetWorkloadGroupScheduling() []WorkloadGroupScheduling {
	workloadGroupScheduling := []WorkloadGroupScheduling{}
	for _, scheduling := range p.Spec.DestinationSelectors {
		workloadGroupScheduling = append(workloadGroupScheduling, WorkloadGroupScheduling{
			MatchLabels: scheduling.MatchLabels,
			Source:      "promise",
		})
	}

	return workloadGroupScheduling
}

func (p *Promise) generatePipelinesObjects(workflowType Type, workflowAction Action, resourceRequest *unstructured.Unstructured, logger logr.Logger) ([]PipelineJobResources, error) {
	promisePipelines, err := NewPipelinesMap(p, logger)
	if err != nil {
		return nil, err
	}

	var allResources []PipelineJobResources
	pipelines := promisePipelines[workflowType][workflowAction]

	lastIndex := len(pipelines) - 1
	for i, pipe := range pipelines {
		isLast := i == lastIndex
		additionalJobEnv := []corev1.EnvVar{
			{Name: "IS_LAST_PIPELINE", Value: strconv.FormatBool(isLast)},
		}

		var resources PipelineJobResources
		var err error
		switch workflowType {
		case WorkflowTypeResource:
			resources, err = pipe.ForResource(p, workflowAction, resourceRequest).Resources(additionalJobEnv)
		case WorkflowTypePromise:
			resources, err = pipe.ForPromise(p, workflowAction).Resources(additionalJobEnv)
		}
		if err != nil {
			return nil, err
		}

		allResources = append(allResources, resources)
	}
	return allResources, nil
}

func (p *Promise) GeneratePromisePipelines(workflowAction Action, logger logr.Logger) ([]PipelineJobResources, error) {
	return p.generatePipelinesObjects(WorkflowTypePromise, workflowAction, nil, logger)
}

func (p *Promise) GenerateResourcePipelines(workflowAction Action, resourceRequest *unstructured.Unstructured, logger logr.Logger) ([]PipelineJobResources, error) {
	return p.generatePipelinesObjects(WorkflowTypeResource, workflowAction, resourceRequest, logger)
}

func (p *Promise) HasPipeline(workflowType Type, workflowAction Action) bool {
	switch workflowType {
	case WorkflowTypeResource:
		switch workflowAction {
		case WorkflowActionConfigure:
			return len(p.Spec.Workflows.Resource.Configure) > 0
		case WorkflowActionDelete:
			return len(p.Spec.Workflows.Resource.Delete) > 0
		}
	case WorkflowTypePromise:
		switch workflowAction {
		case WorkflowActionConfigure:
			return len(p.Spec.Workflows.Promise.Configure) > 0
		case WorkflowActionDelete:
			return len(p.Spec.Workflows.Promise.Delete) > 0
		}
	}
	return false
}

type pipelineMap map[Type]map[Action][]Pipeline

func NewPipelinesMap(promise *Promise, logger logr.Logger) (pipelineMap, error) {
	unstructuredMap := map[Type]map[Action][]unstructured.Unstructured{
		WorkflowTypeResource: {
			WorkflowActionConfigure: promise.Spec.Workflows.Resource.Configure,
			WorkflowActionDelete:    promise.Spec.Workflows.Resource.Delete,
		},
		WorkflowTypePromise: {
			WorkflowActionConfigure: promise.Spec.Workflows.Promise.Configure,
			WorkflowActionDelete:    promise.Spec.Workflows.Promise.Delete,
		},
	}

	pipelinesMap := map[Type]map[Action][]Pipeline{}

	for _, t := range []Type{WorkflowTypeResource, WorkflowTypePromise} {
		if _, ok := pipelinesMap[t]; !ok {
			pipelinesMap[t] = map[Action][]Pipeline{}
		}
		for _, a := range []Action{WorkflowActionConfigure, WorkflowActionDelete} {
			pipelines, err := PipelinesFromUnstructured(unstructuredMap[t][a], logger)
			if err != nil {
				return nil, err
			}
			pipelinesMap[t][a] = pipelines
		}

	}

	return pipelinesMap, nil
}
