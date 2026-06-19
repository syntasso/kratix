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

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

var bucketstatestorelog = logf.Log.WithName("bucketstatestore-resource")

func SetupBucketStateStoreWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &v1alpha1.BucketStateStore{}).
		WithValidator(&BucketStateStoreCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-platform-kratix-io-v1alpha1-bucketstatestore,mutating=false,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=bucketstatestores,verbs=create;update,versions=v1alpha1,name=vbucketstatestore-v1alpha1.kb.io,admissionReviewVersions=v1

// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type BucketStateStoreCustomValidator struct {
}

var _ admission.Validator[*v1alpha1.BucketStateStore] = &BucketStateStoreCustomValidator{}

func (v *BucketStateStoreCustomValidator) ValidateCreate(ctx context.Context, bucket *v1alpha1.BucketStateStore) (admission.Warnings, error) {
	bucketstatestorelog.Info("Validation for BucketStateStore upon creation", "name", bucket.GetName())

	if err := bucket.ValidateSecretRef(); err != nil {
		return nil, err
	}

	return nil, nil
}

func (v *BucketStateStoreCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *v1alpha1.BucketStateStore) (admission.Warnings, error) {
	bucketstatestorelog.Info("Validation for BucketStateStore upon update", "name", newObj.GetName())

	if err := newObj.ValidateSecretRef(); err != nil {
		return nil, err
	}

	return nil, nil
}

func (v *BucketStateStoreCustomValidator) ValidateDelete(ctx context.Context, obj *v1alpha1.BucketStateStore) (admission.Warnings, error) {
	return nil, nil
}
