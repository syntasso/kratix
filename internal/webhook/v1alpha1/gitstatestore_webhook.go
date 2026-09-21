package v1alpha1

import (
	"context"
	"fmt"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	v1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

var gitstatestorelog = logf.Log.WithName("gitstatestore-resource")

func SetupGitStateStoreWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &v1alpha1.GitStateStore{}).
		WithValidator(&GitStateStoreCustomValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-platform-kratix-io-v1alpha1-gitstatestore,mutating=false,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=gitstatestores,verbs=create;update,versions=v1alpha1,name=vgitstatestore-v1alpha1.kb.io,admissionReviewVersions=v1

// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type GitStateStoreCustomValidator struct {
}

var _ admission.Validator[*v1alpha1.GitStateStore] = &GitStateStoreCustomValidator{}

func (v *GitStateStoreCustomValidator) ValidateCreate(ctx context.Context, gitStateStore *v1alpha1.GitStateStore) (admission.Warnings, error) {
	gitstatestorelog.Info("Validation for GitStateStore upon creation", "name", gitStateStore.GetName())

	return warnOnInsecureWithoutHTTPS(gitStateStore), nil
}

func (v *GitStateStoreCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *v1alpha1.GitStateStore) (admission.Warnings, error) {
	gitstatestorelog.Info("Validation for GitStateStore upon update", "name", newObj.GetName())

	return warnOnInsecureWithoutHTTPS(newObj), nil
}

func (v *GitStateStoreCustomValidator) ValidateDelete(ctx context.Context, obj *v1alpha1.GitStateStore) (admission.Warnings, error) {
	return nil, nil
}

// warnOnInsecureWithoutHTTPS flags that spec.insecure is inert, since TLS verification
// is only ever negotiated over https.
func warnOnInsecureWithoutHTTPS(gitStateStore *v1alpha1.GitStateStore) admission.Warnings {
	url := gitStateStore.Spec.URL
	if gitStateStore.Spec.Insecure == nil || strings.HasPrefix(strings.ToLower(url), "https://") {
		return nil
	}

	return admission.Warnings{
		fmt.Sprintf("spec.insecure only applies to https urls; it has no effect on %s", url),
	}
}
