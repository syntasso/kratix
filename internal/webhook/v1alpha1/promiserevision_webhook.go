package v1alpha1

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	return nil, validateReconciliationIntervalAnnotation(obj)
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type
// PromiseRevision. It validates only when the annotation's value changes, so an existing
// out-of-policy value is grandfathered rather than locking the revision against every future
// update once the floor is raised past it.
func (v *PromiseRevisionCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *platformv1alpha1.PromiseRevision) (admission.Warnings, error) {
	oldValue := oldObj.GetAnnotations()[platformv1alpha1.ReconciliationIntervalAnnotation]
	newValue := newObj.GetAnnotations()[platformv1alpha1.ReconciliationIntervalAnnotation]
	if oldValue == newValue {
		return nil, nil
	}
	return nil, validateReconciliationIntervalAnnotation(newObj)
}

// validateReconciliationIntervalAnnotation rejects platformv1alpha1.ReconciliationIntervalAnnotation
// values that ReconciliationInterval would otherwise decline silently: unparseable syntax and
// values below platformv1alpha1.MinReconciliationInterval get distinct messages so the caller
// knows which one to fix.
func validateReconciliationIntervalAnnotation(revision *platformv1alpha1.PromiseRevision) error {
	raw, ok := revision.GetAnnotations()[platformv1alpha1.ReconciliationIntervalAnnotation]
	if !ok {
		return nil
	}

	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("metadata.annotations[%s]: Invalid value: %q: must be a valid duration",
			platformv1alpha1.ReconciliationIntervalAnnotation, raw)
	}
	if d < platformv1alpha1.MinReconciliationInterval {
		return fmt.Errorf("metadata.annotations[%s]: Invalid value: %q: must be at least %s",
			platformv1alpha1.ReconciliationIntervalAnnotation, raw, platformv1alpha1.MinReconciliationInterval)
	}
	return nil
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
