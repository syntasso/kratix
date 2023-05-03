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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// StateStoreSpec defines the desired state of StateStore
type StateStoreSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	SecretRef *v1.SecretReference `json:"secretRef,omitempty"`

	S3 *S3Spec `json:"s3,omitempty"`

	secretData map[string][]byte
}

// StateStoreStatus defines the observed state of StateStore
type StateStoreStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// StateStore is the Schema for the statestores API
type StateStore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StateStoreSpec   `json:"spec,omitempty"`
	Status StateStoreStatus `json:"status,omitempty"`
}

func (s *StateStore) Credentials() map[string][]byte {
	return s.Spec.secretData
}

func (s *StateStore) SetCredentials(secret *v1.Secret) {
	s.Spec.secretData = secret.Data
}

type StateStoreProtocol string

const (
	StateStoreS3  StateStoreProtocol = "s3"
	StateStoreGit StateStoreProtocol = "git"
)

func (s *StateStore) Protocol() StateStoreProtocol {
	// Setting this lets us switch between protocol specific implementations, e.g. credentials)
	// We default to Git to keep it working during initial S3 implementation.
	// TODO: Should revisit fallback once Git is fully implemented
	if s.Spec.S3 != nil {
		return StateStoreS3
	}
	return StateStoreGit
}

//+kubebuilder:object:root=true

// StateStoreList contains a list of StateStore
type StateStoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StateStore `json:"items"`
}

// StateStoreReference is a reference to a StateStore
type StateStoreReference struct {
	Name string `json:"name"`
	//+kubebuilder:validation:Optional
	Namespace string `json:"namespace"`
}

type S3Spec struct {
	BucketName string `json:"bucketName"`
	Endpoint   string `json:"endpoint"`

	//+kubebuilder:validation:Optional
	//+kubebuilder:default=true
	UseSSL bool `json:"useSSL"`
}

func init() {
	SchemeBuilder.Register(&StateStore{}, &StateStoreList{})
}
