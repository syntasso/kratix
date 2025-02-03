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
	"path/filepath"

	"github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	destinationlog = logf.Log.WithName("destination-webhook")
)

func SetupDestinationWebhookWithManager(mgr ctrl.Manager, c client.Client) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&v1alpha1.Destination{}).
		WithDefaulter(&DestinationCustomDefaulter{Client: c}).
		Complete()
}

// Don't delete- breaking change
// +kubebuilder:webhook:path=/mutate-platform-kratix-io-v1alpha1-destination,mutating=true,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=destinations,verbs=create;update,versions=v1alpha1,name=vdestination.kb.io,admissionReviewVersions=v1

type DestinationCustomDefaulter struct {
	Client client.Client
}

var _ webhook.CustomDefaulter = &DestinationCustomDefaulter{}

const SkipPathDefaultingAnnotation = "kratix.io/skip-path-defaulting"

func (d DestinationCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	destination, ok := obj.(*v1alpha1.Destination)
	if !ok {
		return fmt.Errorf("expected Destination but got %T", obj)
	}

	destinationlog.Info("defaulting Destination", "name", destination.Name)

	if destination.Annotations == nil {
		destination.Annotations = make(map[string]string)
	}

	existingDestination := &v1alpha1.Destination{}
	if err := d.Client.Get(ctx, client.ObjectKey{Name: destination.Name}, existingDestination); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}

		// this is a new destination it should not have the destination name as
		// a suffix, since destinations are solely relying on the `path` spec
		// field
		destination.Annotations[SkipPathDefaultingAnnotation] = "true"
	}

	// the destination exist, but the annotation was removed
	// we should add it back to prevent defaulting when we shouldn't
	if _, found := existingDestination.Annotations[SkipPathDefaultingAnnotation]; found {
		destination.Annotations[SkipPathDefaultingAnnotation] = "true"
	}

	// this destination has already been defaulted; skip it
	if _, found := destination.Annotations[SkipPathDefaultingAnnotation]; found {
		return nil
	}

	// this destination was created prior to the change of behaviour of `path`
	// the `path` should be updated to include the destination name to ensure
	// backwards compatibility
	destination.Spec.Path = filepath.Join(destination.Name, destination.Spec.Path)
	destination.Annotations[SkipPathDefaultingAnnotation] = "true"

	return nil
}
