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
	"reflect"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
// +kubebuilder:pruning:PreserveUnknownFields
type WorkPlacementStatus struct {
	// Current conditions of the WorkPlacement. Includes a Ready condition indicating all workloads are written to the StateStore
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// +optional
	// VersionID contains the version identifier of the last applied workplacement
	// For Git StateStores, this is the SHA of the last applied commit
	// For Bucket StateStores, this is always empty
	VersionID string `json:"versionID,omitempty"`

	// +optional
	// DeliveryReceipt identifies a write the StateStore's writer accepted but has not delivered.
	// It is set while WriteSucceeded is Unknown and cleared once the delivery resolves.
	DeliveryReceipt string `json:"deliveryReceipt,omitempty"`

	// +optional
	// DeliveryGeneration is the generation the outstanding delivery carries. An older one cannot
	// satisfy the current generation, however it resolves.
	DeliveryGeneration int64 `json:"deliveryGeneration,omitempty"`

	// +optional
	// DeliveryURL is where a person can follow the latest delivery, such as the pull request a
	// StateStore's delivery writer opened. It is set from every result that writer returns and
	// stays after the delivery resolves. It is empty for StateStores without a delivery writer.
	DeliveryURL string `json:"deliveryURL,omitempty"`

	// Status fields set by other controllers, kept so typed status updates do not drop them.
	Custom map[string]apiextensionsv1.JSON `json:"-"`
}

type workPlacementStatusFields WorkPlacementStatus

func (s *WorkPlacementStatus) MarshalJSON() ([]byte, error) {
	return marshalStatusWithCustomFields(workPlacementStatusFields(*s), s.Custom, workPlacementStatusFieldNames)
}

func (s *WorkPlacementStatus) UnmarshalJSON(data []byte) error {
	var typed workPlacementStatusFields
	custom, err := unmarshalStatusKeepingCustomFields(data, &typed, workPlacementStatusFieldNames)
	if err != nil {
		return err
	}
	typed.Custom = custom
	*s = WorkPlacementStatus(typed)
	return nil
}

var workPlacementStatusFieldNames = jsonFieldNames(reflect.TypeOf(WorkPlacementStatus{}))

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
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &WorkPlacement{}, &WorkPlacementList{})
		return nil
	})
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

const (
	WriteSucceededConditionType    = "WriteSucceeded"
	ScheduleSucceededConditionType = "ScheduleSucceeded"

	WorkloadsAwaitingDeliveryReason = "WorkloadsAwaitingDelivery"
	WorkloadsRejectedReason         = "WorkloadsRejected"
)

func (w *WorkPlacement) SetWriteSucceededCondition() bool {
	return meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
		Type:               WriteSucceededConditionType,
		Status:             metav1.ConditionTrue,
		Reason:             "WorkloadsWrittenToStateStore",
		Message:            "Workloads written to State Store",
		ObservedGeneration: w.GetGeneration(),
	})
}

func (w *WorkPlacement) IsWrittenAtCurrentGeneration(workGeneration int64) bool {
	writeSucceeded := meta.FindStatusCondition(w.Status.Conditions, WriteSucceededConditionType)
	return writeSucceeded != nil && writeSucceeded.Status == metav1.ConditionTrue &&
		writeSucceeded.ObservedGeneration == w.GetGeneration() &&
		w.Spec.WorkGeneration == workGeneration
}

func (w *WorkPlacement) SetWorkplacementReadyStatus() bool {
	var writeSucceeded, misplaced bool
	for _, cond := range w.Status.Conditions {
		if cond.Type == ScheduleSucceededConditionType && cond.Status == metav1.ConditionFalse {
			misplaced = true
		}
		if cond.Type == WriteSucceededConditionType && cond.Status == metav1.ConditionTrue {
			writeSucceeded = true
		}
	}
	if writeSucceeded && !misplaced {
		return meta.SetStatusCondition(&w.Status.Conditions,
			metav1.Condition{
				Type:               "Ready",
				Status:             metav1.ConditionTrue,
				Reason:             "WorkloadsWrittenToTargetDestination",
				Message:            "Ready",
				ObservedGeneration: w.GetGeneration(),
			},
		)
	}
	return false
}

// SetWriteDeferredCondition marks a write the writer accepted but has not delivered.
// Unknown, not False: False is read as a failed write and fails the resource request.
func (w *WorkPlacement) SetWriteDeferredCondition(message string) bool {
	writeSucceededUpdated := meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
		Type:               WriteSucceededConditionType,
		Status:             metav1.ConditionUnknown,
		Reason:             WorkloadsAwaitingDeliveryReason,
		Message:            message,
		ObservedGeneration: w.GetGeneration(),
	})
	readyUpdated := meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             WorkloadsAwaitingDeliveryReason,
		Message:            "Pending",
		ObservedGeneration: w.GetGeneration(),
	})
	return writeSucceededUpdated || readyUpdated
}

func (w *WorkPlacement) SetWriteFailedCondition(err error) bool {
	return w.setWriteFailed("WorkloadsFailedWrite", err.Error())
}

func (w *WorkPlacement) setWriteFailed(reason, message string) bool {
	writeSucceededUpdated := meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
		Type:               WriteSucceededConditionType,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: w.GetGeneration(),
	})
	readyUpdated := meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
		Status:             metav1.ConditionFalse,
		Type:               "Ready",
		Reason:             "WorkloadsFailedWrite",
		Message:            "Failing",
		ObservedGeneration: w.GetGeneration(),
	})
	return writeSucceededUpdated || readyUpdated
}

// SetWriteRejectedCondition marks a write the writer accepted and then rejected.
func (w *WorkPlacement) SetWriteRejectedCondition(reason string) bool {
	writeSucceededUpdated := meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
		Type:               WriteSucceededConditionType,
		Status:             metav1.ConditionFalse,
		Reason:             WorkloadsRejectedReason,
		Message:            reason,
		ObservedGeneration: w.GetGeneration(),
	})
	readyUpdated := meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
		Status:             metav1.ConditionFalse,
		Type:               "Ready",
		Reason:             WorkloadsRejectedReason,
		Message:            "Failing",
		ObservedGeneration: w.GetGeneration(),
	})
	return writeSucceededUpdated || readyUpdated
}

func (w *WorkPlacement) SetDestinationNotReadyCondition(message string) bool {
	writeSucceededUpdated := meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
		Type:               WriteSucceededConditionType,
		Status:             metav1.ConditionFalse,
		Reason:             "DestinationNotReady",
		Message:            message,
		ObservedGeneration: w.GetGeneration(),
	})
	readyUpdated := meta.SetStatusCondition(&w.Status.Conditions, metav1.Condition{
		Status:             metav1.ConditionFalse,
		Type:               "Ready",
		Reason:             "DestinationNotReady",
		Message:            "Failing",
		ObservedGeneration: w.GetGeneration(),
	})
	return writeSucceededUpdated || readyUpdated
}
