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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

// log is for logging in this package.
var bucketstatestorelog = logf.Log.WithName("bucketstatestore-resource")

// SetupBucketStateStoreWebhookWithManager registers the webhook for BucketStateStore in the manager.
func SetupBucketStateStoreWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&v1alpha1.BucketStateStore{}).
		WithValidator(&BucketStateStoreCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-platform-kratix-io-v1alpha1-bucketstatestore,mutating=false,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=bucketstatestores,verbs=create;update,versions=v1alpha1,name=vbucketstatestore-v1alpha1.kb.io,admissionReviewVersions=v1

// BucketStateStoreCustomValidator struct is responsible for validating the BucketStateStore resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type BucketStateStoreCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &BucketStateStoreCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type BucketStateStore.
func (v *BucketStateStoreCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	bucket, ok := obj.(*v1alpha1.BucketStateStore)
	if !ok {
		return nil, fmt.Errorf("expected a BucketStateStore object but got %T", obj)
	}
	bucketstatestorelog.Info("Validation for BucketStateStore upon creation", "name", bucket.GetName())

	if err := bucket.ValidateSecretRef(); err != nil {
		return nil, err
	}

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type BucketStateStore.
func (v *BucketStateStoreCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	bucket, ok := newObj.(*v1alpha1.BucketStateStore)
	if !ok {
		return nil, fmt.Errorf("expected a BucketStateStore object for the newObj but got %T", newObj)
	}
	bucketstatestorelog.Info("Validation for BucketStateStore upon update", "name", bucket.GetName())

	if err := bucket.ValidateSecretRef(); err != nil {
		return nil, err
	}

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type BucketStateStore.
func (v *BucketStateStoreCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
