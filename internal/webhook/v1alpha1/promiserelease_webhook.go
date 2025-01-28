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
	"fmt"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	promiseFetcher    v1alpha1.PromiseFetcher
	promisereleaselog = logf.Log.WithName("promiserelease-resource")
)

func SetupPromiseReleaseWebhookWithManager(mgr ctrl.Manager, c client.Client, pf v1alpha1.PromiseFetcher) error {
	k8sClient = c
	promiseFetcher = pf
	return ctrl.NewWebhookManagedBy(mgr).For(&v1alpha1.PromiseRelease{}).
		WithValidator(&PromiseReleaseCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-platform-kratix-io-v1alpha1-promiserelease,mutating=false,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=promisereleases,verbs=create;update,versions=v1alpha1,name=vpromiserelease.kb.io,admissionReviewVersions=v1

type PromiseReleaseCustomValidator struct{}

var _ webhook.CustomValidator = &PromiseReleaseCustomValidator{}

func (p PromiseReleaseCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	r, ok := obj.(*v1alpha1.PromiseRelease)
	if !ok {
		return nil, fmt.Errorf("expected a PromiseRelease object but got %T", obj)
	}

	promisereleaselog.Info("validate create", "name", r.Name)

	if err = validate(r); err != nil {
		return nil, err
	}

	secretRefData, err := r.FetchSecretFromReference(k8sClient)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch data from secretRef: %w", err)
	}

	authHeader, exists := secretRefData["authorizationHeader"]
	if !exists {
		authHeader = []byte("")
	}

	promise, err := promiseFetcher.FromURL(r.Spec.SourceRef.URL, string(authHeader))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch promise: %w", err)
	}

	promiseVersion, found := promise.GetLabels()[v1alpha1.PromiseVersionLabel]
	if !found {
		msg := fmt.Sprintf("Warning: version label (%s) not found on promise, installation will fail", v1alpha1.PromiseVersionLabel)
		return []string{msg}, nil
	}

	if promiseVersion != r.Spec.Version {
		msg := fmt.Sprintf("Warning: version labels do not match, found: %s, expected: %s, installation will fail", promiseVersion, r.Spec.Version)
		return []string{msg}, nil
	}

	return nil, nil
}

func (p PromiseReleaseCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	r, ok := newObj.(*v1alpha1.PromiseRelease)
	if !ok {
		return nil, fmt.Errorf("expected a PromiseRelease object but got %T", newObj)
	}

	promisereleaselog.Info("validate update", "name", r.Name)

	if err = validate(r); err != nil {
		return nil, err
	}

	return nil, nil
}

func (p PromiseReleaseCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

func validate(r *v1alpha1.PromiseRelease) error {
	if r.Spec.SourceRef.URL == "" {
		return fmt.Errorf("sourceRef.url must be set")
	}
	return nil
}
