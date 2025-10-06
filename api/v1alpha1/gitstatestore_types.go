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

const (
	BasicAuthMethod = "basicAuth"
	SSHAuthMethod   = "ssh"
	GitHubAppAuthMethod = "githubApp"
)

// GitStateStoreSpec defines the desired state of GitStateStore
type GitStateStoreSpec struct {
	// URL of the git repository.
	URL string `json:"url,omitempty"`

	StateStoreCoreFields `json:",inline"`

	// Branch of the git repository; default to main.
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=main
	Branch string `json:"branch,omitempty"`

	// Authentication method used to access the StateStore.
	// Default to basicAuth; options are basicAuth, ssh, and githubApp.
	// +kubebuilder:validation:Enum=basicAuth;ssh;githubApp
	// +kubebuilder:default:=basicAuth
	AuthMethod string `json:"authMethod,omitempty"`
	// Git author name and email used to commit this git state store; name defaults to 'kratix'
	// +kubebuilder:default:={name: "kratix"}
	GitAuthor GitAuthor `json:"gitAuthor,omitempty"`
}

type GitAuthor struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// StateStoreStatus defines the observed state of a state store
type StateStoreStatus struct {
	Status     string             `json:"status"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,path=gitstatestores,categories=kratix
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Indicates the state store is ready to use"

// GitStateStore is the Schema for the gitstatestores API
type GitStateStore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GitStateStoreSpec `json:"spec,omitempty"`
	Status StateStoreStatus  `json:"status,omitempty"`
}

func (g *GitStateStore) GetSecretRef() *corev1.SecretReference {
	return g.Spec.SecretRef
}

func (g *GitStateStore) GetStatus() *StateStoreStatus {
	return &g.Status
}

func (g *GitStateStore) SetStatus(status StateStoreStatus) {
	g.Status = status
}

// +kubebuilder:object:root=true

// GitStateStoreList contains a list of GitStateStore
type GitStateStoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GitStateStore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GitStateStore{}, &GitStateStoreList{})
}
