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
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	authenticationv1 "k8s.io/api/authentication/v1"
)

// nolint:unused
// log is for logging in this package.
var promiserevisionlog = logf.Log.WithName("promiserevision-resource")

// SetupPromiseRevisionWebhookWithManager registers the webhook for PromiseRevision in the manager.
func SetupPromiseRevisionWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&platformv1alpha1.PromiseRevision{}).
		WithValidator(&PromiseRevisionCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-platform-kratix-io-v1alpha1-promiserevision,mutating=false,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=promiserevisions,verbs=create;update;delete,versions=v1alpha1,name=vpromiserevision-v1alpha1.kb.io,admissionReviewVersions=v1

// PromiseRevisionCustomValidator struct is responsible for validating the PromiseRevision resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type PromiseRevisionCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &PromiseRevisionCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type PromiseRevision.
func (v *PromiseRevisionCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type PromiseRevision.
func (v *PromiseRevisionCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type PromiseRevision.
func (v *PromiseRevisionCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	revision, ok := obj.(*platformv1alpha1.PromiseRevision)
	if !ok {
		return nil, fmt.Errorf("expected a PromiseRevision object but got %T", obj)
	}
	promiserevisionlog.Info("Validation for PromiseRevision upon deletion", "name", revision.GetName())

	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		promiserevisionlog.Error(err, "could not get admission request from context")
		return nil, nil
	}

	user := req.UserInfo
	if revision.Status.Latest && !isKratixController(user) {
		promiserevisionlog.Info("This PromiseRevision is marked as latest; it cannot be deleted", "name", revision.GetName())
		return nil, fmt.Errorf("can not delete the latest PromiseRevision")
	}
	return nil, nil
}

// isKratixController is a helper that checks if the request comes from
// a service account from the kratix-platform-system namespace or system garbage collector
func isKratixController(user authenticationv1.UserInfo) bool {
	if strings.HasPrefix(user.Username, "system:serviceaccount:kratix-platform-system") {
		return true
	}
	if user.Username == "system:serviceaccount:kube-system:generic-garbage-collector" {
		return true
	}
	return false
}
