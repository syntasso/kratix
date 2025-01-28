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
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const TypeHTTP = "http"

// PromiseReleaseSpec defines the desired state of PromiseRelease
type PromiseReleaseSpec struct {
	Version string `json:"version,omitempty"`
	// +kubebuilder:default:={type: "http"}
	SourceRef SourceRef `json:"sourceRef"`
}

type SourceRef struct {
	// +kubebuilder:validation:Enum:={http}
	Type string `json:"type"`
	URL  string `json:"url,omitempty"`
	// Reference a secret with credentials to access the source.
	// For more details on the secret format, see the documentation:
	//   https://docs.kratix.io/main/reference/promises/releases#promise-release
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`
}

// PromiseReleaseStatus defines the observed state of PromiseRelease
type PromiseReleaseStatus struct {
	Status     string             `json:"status,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster,path=promisereleases,categories=kratix
//+kubebuilder:printcolumn:JSONPath=".status.status",name="Status",type=string
//+kubebuilder:printcolumn:JSONPath=".spec.version",name="Version",type=string

// PromiseRelease is the Schema for the promisereleases API
type PromiseRelease struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromiseReleaseSpec   `json:"spec,omitempty"`
	Status PromiseReleaseStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PromiseReleaseList contains a list of PromiseRelease
type PromiseReleaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromiseRelease `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PromiseRelease{}, &PromiseReleaseList{})
}

func (pr *PromiseRelease) FetchSecretFromReference(k8sClient client.Client) (map[string][]byte, error) {
	if pr.Spec.SourceRef.SecretRef == nil {
		return nil, nil //nolint:nilnil
	}

	fetchSecret := &corev1.Secret{}
	ns := pr.Namespace
	if pr.Spec.SourceRef.SecretRef.Namespace != "" {
		ns = pr.Spec.SourceRef.SecretRef.Namespace
	}
	namespacedName := client.ObjectKey{
		Namespace: ns,
		Name:      pr.Spec.SourceRef.SecretRef.Name,
	}

	err := k8sClient.Get(context.TODO(), namespacedName, fetchSecret)
	if err != nil {
		return nil, err
	}
	return fetchSecret.Data, nil
}
