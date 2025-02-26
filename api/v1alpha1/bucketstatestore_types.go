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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BucketStateStoreSpec defines the desired state of BucketStateStore
type BucketStateStoreSpec struct {
	// Name of the bucket; required field.
	BucketName string `json:"bucketName"`
	// Endpoint to access the bucket.
	// Required field.
	Endpoint             string `json:"endpoint"`
	StateStoreCoreFields `json:",inline"`

	// Toggle to turn off or on SSL verification when connecting to the bucket.
	//+kubebuilder:validation:Optional
	Insecure bool `json:"insecure"`

	// Authentication method used to access the StateStore.
	// Default to accessKey; options are accessKey and IAM.
	//+kubebuilder:validation:Enum=accessKey;IAM
	//+kubebuilder:default:=accessKey
	AuthMethod string `json:"authMethod,omitempty"`
}

// BucketStateStoreStatus defines the observed state of BucketStateStore
type BucketStateStoreStatus struct {
	Status     string             `json:"status"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster,path=bucketstatestores,categories=kratix
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Indicates the state store is ready to use"

// BucketStateStore is the Schema for the bucketstatestores API
type BucketStateStore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketStateStoreSpec   `json:"spec,omitempty"`
	Status BucketStateStoreStatus `json:"status,omitempty"`
}

func (b *BucketStateStore) GetSecretRef() *corev1.SecretReference {
	return b.Spec.SecretRef
}

//+kubebuilder:object:root=true

// BucketStateStoreList contains a list of BucketStateStore
type BucketStateStoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BucketStateStore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BucketStateStore{}, &BucketStateStoreList{})
}
