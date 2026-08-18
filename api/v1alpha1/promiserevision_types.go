/*
Copyright 2025 Syntasso.

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
	"time"

	"github.com/syntasso/kratix/lib/objectutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SkipResourceRequestCleanupOnDeleteAnnotation is set on a PromiseRevision that is being removed
// only to replace its object (for example legacy DNS name → deterministic name) while keeping the
// same promise version. When present, the PromiseRevision controller must not delete ResourceRequests
// for this revision and must only drop its resource-request cleanup finalizer.
const SkipResourceRequestCleanupOnDeleteAnnotation = KratixPrefix + "skip-resource-request-cleanup-on-delete"

// LatestRevisionLabel marks the PromiseRevision that is currently latest for the promise (at most
// one revision per promise should carry this label).
const LatestRevisionLabel = KratixPrefix + "latest-revision"

// MetadataBoolTrue is the conventional string value for boolean Kubernetes labels and annotations.
const MetadataBoolTrue = "true"

// PromiseRevisionSpec defines the desired state of PromiseRevision
type PromiseRevisionSpec struct {
	// PromiseRef is the reference to the Promise this revision is based on.
	// +required
	PromiseRef PromiseRef `json:"promiseRef"`

	// PromiseSpec is the Spec of the Promise this revision is based on.
	// +required
	PromiseSpec PromiseSpec `json:"promiseSpec"`
	// Version is the version of the Promise this revision is based on.
	// +required
	Version string `json:"version"`
}

// PromiseRevisionStatus defines the observed state of PromiseRevision.
type PromiseRevisionStatus struct {
	// Latest is true if this revision is the latest revision for the Promise. Only one revision can be the latest at a time.
	Latest bool `json:"latest,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,path=promiserevisions,categories=kratix
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Promise",type=string,JSONPath=`.spec.promiseRef.name`,description="The name of the Promise this revision is based on."
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.version`,description="The version of the Promise this revision is based on."
// +kubebuilder:printcolumn:name="Latest",type=boolean,JSONPath=`.status.latest`,description="Indicates if this PromiseRevision is the latest."

// PromiseRevision is the Schema for the promiserevisions API
type PromiseRevision struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of PromiseRevision
	// +required
	Spec PromiseRevisionSpec `json:"spec"`

	// status defines the observed state of PromiseRevision
	// +optional
	Status PromiseRevisionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PromiseRevisionList contains a list of PromiseRevision
type PromiseRevisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PromiseRevision `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, &PromiseRevision{}, &PromiseRevisionList{})
		return nil
	})
}

func (pr *PromiseRevision) GetPromiseName() string {
	return pr.Spec.PromiseRef.Name
}

// SetSkipResourceRequestCleanupOnDelete sets SkipResourceRequestCleanupOnDeleteAnnotation so
// deletion of this object does not trigger ResourceRequest cleanup (name migration only).
func (pr *PromiseRevision) SetSkipResourceRequestCleanupOnDelete() {
	ann := pr.GetAnnotations()
	if ann == nil {
		pr.SetAnnotations(map[string]string{
			SkipResourceRequestCleanupOnDeleteAnnotation: MetadataBoolTrue,
		})
		return
	}
	ann[SkipResourceRequestCleanupOnDeleteAnnotation] = MetadataBoolTrue
}

// SkipResourceRequestCleanupOnDelete reports whether ResourceRequest cleanup must be skipped on delete.
func (pr *PromiseRevision) SkipResourceRequestCleanupOnDelete() bool {
	return pr.GetAnnotations()[SkipResourceRequestCleanupOnDeleteAnnotation] == MetadataBoolTrue
}

// HasLatestRevisionLabel reports whether this revision carries the latest-revision label.
func (pr *PromiseRevision) HasLatestRevisionLabel() bool {
	return pr.GetLabels()[LatestRevisionLabel] == MetadataBoolTrue
}

// SetLatestRevisionLabel sets LatestRevisionLabel on metadata (value MetadataBoolTrue).
func (pr *PromiseRevision) SetLatestRevisionLabel() {
	l := pr.GetLabels()
	if l == nil {
		pr.SetLabels(map[string]string{LatestRevisionLabel: MetadataBoolTrue})
		return
	}
	l[LatestRevisionLabel] = MetadataBoolTrue
}

// ClearLatestRevisionLabel removes LatestRevisionLabel from metadata labels if present.
func (pr *PromiseRevision) ClearLatestRevisionLabel() {
	l := pr.GetLabels()
	if l == nil {
		return
	}
	delete(l, LatestRevisionLabel)
}

// MinReconciliationInterval is the floor enforced in three places: the Promise validating webhook,
// for spec.workflows.config.reconciliationInterval; PromiseRevisionCustomValidator, at admission,
// for ReconciliationIntervalAnnotation; and ReconciliationInterval, the read path, when it reads
// that annotation.
const MinReconciliationInterval = time.Minute

// ReconciliationIntervalAnnotation overrides a revision's reconciliation interval ahead of its
// spec snapshot. ReconciliationInterval reads it first; a value that fails to parse, or falls
// below MinReconciliationInterval, is declined and falls through to the spec snapshot.
const ReconciliationIntervalAnnotation = KratixPrefix + "reconciliation-interval"

// ReconciliationInterval returns this revision's reconciliation interval and true: the value of
// ReconciliationIntervalAnnotation if it parses and meets MinReconciliationInterval, otherwise
// the spec snapshot's interval. It returns fallback and false when neither is set.
func (pr *PromiseRevision) ReconciliationInterval(fallback time.Duration) (time.Duration, bool) {
	if raw, ok := pr.GetAnnotations()[ReconciliationIntervalAnnotation]; ok {
		if d, err := time.ParseDuration(raw); err == nil && d >= MinReconciliationInterval {
			return d, true
		}
	}
	interval := pr.Spec.PromiseSpec.Workflows.Config.ReconciliationInterval
	if interval == nil {
		return fallback, false
	}
	return interval.Duration, true
}

// ReconciliationIntervalAnnotation reports how ReconciliationInterval treated this revision's
// ReconciliationIntervalAnnotation: applied is true when the annotation supplied the interval;
// declined is true when it is set but was rejected - unparseable, or below MinReconciliationInterval -
// so the interval fell through to the spec snapshot. Both are false when the annotation is absent.
func (pr *PromiseRevision) ReconciliationIntervalAnnotation() (applied, declined bool) {
	raw, ok := pr.GetAnnotations()[ReconciliationIntervalAnnotation]
	if !ok {
		return false, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < MinReconciliationInterval {
		return false, true
	}
	return true, false
}

func NewPromiseRevision(promise *Promise, version string) *PromiseRevision {
	return &PromiseRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:   objectutil.GenerateDeterministicObjectName(promise.GetName(), version),
			Labels: promise.GenerateSharedLabels(),
		},
		Spec: PromiseRevisionSpec{
			PromiseRef: PromiseRef{
				Name: promise.GetName(),
			},
			PromiseSpec: promise.Spec,
			Version:     version,
		},
	}
}
