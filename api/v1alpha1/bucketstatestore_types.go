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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	AuthMethodIAM       = "IAM"
	AuthMethodAccessKey = "accessKey"
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

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster,path=bucketstatestores,categories=kratix
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Indicates the state store is ready to use"

// BucketStateStore is the Schema for the bucketstatestores API
type BucketStateStore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketStateStoreSpec `json:"spec,omitempty"`
	Status StateStoreStatus     `json:"status,omitempty"`
}

func (b *BucketStateStore) GetSecretRef() *corev1.SecretReference {
	return b.Spec.SecretRef
}

func (b *BucketStateStore) ValidateSecretRef() error {
	if b.Spec.AuthMethod == AuthMethodAccessKey {
		if b.Spec.SecretRef == nil {
			return fmt.Errorf("spec.secretRef must be set when using authentication method accessKey")
		}
		if b.Spec.SecretRef.Name == "" {
			return fmt.Errorf("spec.secretRef must contain secret name")
		}
		if b.Spec.SecretRef.Namespace == "" {
			return fmt.Errorf("spec.secretRef must contain secret namespace")
		}
	}
	return nil
}

func (b *BucketStateStore) GetStatus() *StateStoreStatus {
	return &b.Status
}

func (b *BucketStateStore) SetStatus(status StateStoreStatus) {
	b.Status = status
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
