/*
Copyright 2021 Syntasso.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

distributed under the License is distributed on an "AS IS" BASIS,
Unless required by applicable law or agreed to in writing, software
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// FeatureFlag is the Schema for the FeatureFlag API
type FeatureFlag struct {
	// Embed the type containing the common fields.
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec FeatureFlagSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// FeatureFlagList contains a list of FeatureFlag
type FeatureFlagList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FeatureFlag `json:"items"`
}

type FeatureFlagSpec struct {
	// Description of the feature flag
	Description string `json:"description,omitempty"`

	// Enabled indicates if the feature flag is enabled
	Enabled bool `json:"enabled,omitempty"`

	// DestinationSelectors is a list of selectors that determine which destinations
	// this feature flag applies to. If empty, the feature flag applies to all
	// destinations.
	DestinationSelectors []FeatureFlagSelectors `json:"destinationSelectors,omitempty"`
}

type FeatureFlagSelectors struct {
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

func init() {
	SchemeBuilder.Register(&FeatureFlag{}, &FeatureFlagList{})
}
