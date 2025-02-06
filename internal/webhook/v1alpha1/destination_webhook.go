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

	stderror "errors"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

//+kubebuilder:webhook:path=/mutate-platform-kratix-io-v1alpha1-destination,mutating=true,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=destinations,verbs=create;update,versions=v1alpha1,name=mdestination.kb.io,admissionReviewVersions=v1

// DestinationCustomDefaulter is a custom defaulter for Destination.
// It ensures Destination resources created prior to #234 continue to work.
// This Defaulter should be removed at later versions of Kratix.
type DestinationCustomDefaulter struct {
	Client client.Client
	Logger logr.Logger
}

var _ webhook.CustomDefaulter = &DestinationCustomDefaulter{}

// Default implements a Mutating Webhook for the Destination resource.
func (d *DestinationCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	destination, ok := obj.(*v1alpha1.Destination)
	if !ok {
		return fmt.Errorf("expected Destination but got %T", obj)
	}

	d.Logger.Info("defaulting Destination", "name", destination.Name)

	if destination.Annotations == nil {
		destination.Annotations = make(map[string]string)
	}

	existingDestination := &v1alpha1.Destination{}
	if err := d.Client.Get(ctx, client.ObjectKey{Name: destination.Name}, existingDestination); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}

		// this is a new destination we should add the annotation so the
		// controller doesn't try to append the destination name to the path on
		// the next reconcile
		destination.Annotations[v1alpha1.SkipPathDefaultingAnnotation] = "true"
	}

	// this is here to prevent the annotation from being removed on already patched destinations
	if _, found := existingDestination.Annotations[v1alpha1.SkipPathDefaultingAnnotation]; found {
		destination.Annotations[v1alpha1.SkipPathDefaultingAnnotation] = "true"
	}

	return nil
}

// +kubebuilder:webhook:path=/validate-platform-kratix-io-v1alpha1-destination,mutating=false,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=destinations,verbs=create;update,versions=v1alpha1,name=vdestination.kb.io,admissionReviewVersions=v1

// DestinationCustomValidator is a custom validator for Destination.
type DestinationCustomValidator struct{}

var _ webhook.CustomValidator = &DestinationCustomValidator{}

// ValidateCreate implements a validating webhook for the Destination resource.
func (v *DestinationCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	destination, ok := obj.(*v1alpha1.Destination)
	if !ok {
		return nil, fmt.Errorf("expected Destination but got %T", obj)
	}

	if destination.Spec.Path == "" {
		return nil, stderror.New("path field is required")
	}

	return nil, nil
}

// ValidateUpdate implements a validating webhook for the Destination resource.
func (v *DestinationCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	return v.ValidateCreate(ctx, newObj)
}

// ValidateDelete implements a validating webhook for the Destination resource.
func (v *DestinationCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// SetupDestinationWebhookWithManager sets up the mutating and validating webhooks with the Manager.
func SetupDestinationWebhookWithManager(mgr ctrl.Manager, c client.Client) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&v1alpha1.Destination{}).
		WithDefaulter(&DestinationCustomDefaulter{Client: c}).
		WithValidator(&DestinationCustomValidator{}).
		Complete()
}
