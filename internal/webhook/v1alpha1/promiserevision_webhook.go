package v1alpha1

import (
	"context"
	"fmt"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	authenticationv1 "k8s.io/api/authentication/v1"
)

// log is for logging in this package.
var promiserevisionlog = logf.Log.WithName("promiserevision-resource")

// SetupPromiseRevisionWebhookWithManager registers the webhook for PromiseRevision in the manager.
func SetupPromiseRevisionWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &platformv1alpha1.PromiseRevision{}).
		WithValidator(&PromiseRevisionCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-platform-kratix-io-v1alpha1-promiserevision,mutating=false,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=promiserevisions,verbs=create;update;delete,versions=v1alpha1,name=vpromiserevision-v1alpha1.kb.io,admissionReviewVersions=v1

type PromiseRevisionCustomValidator struct{}

var _ admission.Validator[*platformv1alpha1.PromiseRevision] = &PromiseRevisionCustomValidator{}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type PromiseRevision.
func (v *PromiseRevisionCustomValidator) ValidateCreate(_ context.Context, obj *platformv1alpha1.PromiseRevision) (admission.Warnings, error) {
	return nil, nil
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type PromiseRevision.
func (v *PromiseRevisionCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *platformv1alpha1.PromiseRevision) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type PromiseRevision.
func (v *PromiseRevisionCustomValidator) ValidateDelete(ctx context.Context, revision *platformv1alpha1.PromiseRevision) (admission.Warnings, error) {
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
