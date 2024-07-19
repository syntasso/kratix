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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

const (
	KratixPrefix        = "kratix.io/"
	PromiseVersionLabel = KratixPrefix + "promise-version"
)

var (
	promiseFetcher    PromiseFetcher
	promisereleaselog = logf.Log.WithName("promiserelease-resource")
)

func (r *PromiseRelease) SetupWebhookWithManager(mgr ctrl.Manager, pf PromiseFetcher) error {
	promiseFetcher = pf
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:path=/validate-platform-kratix-io-v1alpha1-promiserelease,mutating=false,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=promisereleases,verbs=create;update,versions=v1alpha1,name=vpromiserelease.kb.io,admissionReviewVersions=v1
var _ webhook.Validator = &PromiseRelease{}

func (r *PromiseRelease) ValidateCreate() (admission.Warnings, error) {
	promisereleaselog.Info("validate create", "name", r.Name)
	if err := r.validate(); err != nil {
		return nil, err
	}

	promise, err := promiseFetcher.FromURL(r.Spec.SourceRef.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch promise: %w", err)
	}

	promiseVersion, found := promise.GetLabels()[PromiseVersionLabel]
	if !found {
		msg := fmt.Sprintf("Warning: version label (%s) not found on promise, installation will fail", PromiseVersionLabel)
		return []string{msg}, nil
	}

	if promiseVersion != r.Spec.Version {
		msg := fmt.Sprintf("Warning: version labels do not match, found: %s, expected: %s, installation will fail", promiseVersion, r.Spec.Version)
		return []string{msg}, nil
	}

	return nil, nil
}

func (r *PromiseRelease) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	promisereleaselog.Info("validate update", "name", r.Name)
	if err := r.validate(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (r *PromiseRelease) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

func (r *PromiseRelease) validate() error {
	if r.Spec.SourceRef.URL == "" {
		return fmt.Errorf("sourceRef.url must be set")
	}
	return nil
}
