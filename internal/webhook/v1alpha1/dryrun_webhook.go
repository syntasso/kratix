package v1alpha1

import (
	"context"
	"fmt"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupDryRunWebhookWithManager registers admission validation independently of
// whether the DryRun controller is enabled.
func SetupDryRunWebhookWithManager(mgr ctrl.Manager, enabled bool) error {
	return ctrl.NewWebhookManagedBy(mgr, &platformv1alpha1.DryRun{}).
		WithValidator(&DryRunCustomValidator{Reader: mgr.GetAPIReader(), Enabled: enabled}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-platform-kratix-io-v1alpha1-dryrun,mutating=false,failurePolicy=fail,sideEffects=None,groups=platform.kratix.io,resources=dryruns,verbs=create,versions=v1alpha1,name=vdryrun-v1alpha1.kb.io,admissionReviewVersions=v1

// DryRunCustomValidator rejects DryRuns that cannot be processed safely.
type DryRunCustomValidator struct {
	Reader  client.Reader
	Enabled bool
}

var _ admission.Validator[*platformv1alpha1.DryRun] = &DryRunCustomValidator{}

func (v *DryRunCustomValidator) ValidateCreate(ctx context.Context, dryRun *platformv1alpha1.DryRun) (admission.Warnings, error) {
	return nil, v.validate(ctx, dryRun)
}

func (v *DryRunCustomValidator) ValidateUpdate(context.Context, *platformv1alpha1.DryRun, *platformv1alpha1.DryRun) (admission.Warnings, error) {
	return nil, nil
}

func (v *DryRunCustomValidator) ValidateDelete(context.Context, *platformv1alpha1.DryRun) (admission.Warnings, error) {
	return nil, nil
}

func (v *DryRunCustomValidator) validate(ctx context.Context, dryRun *platformv1alpha1.DryRun) error {
	if !v.Enabled {
		return fmt.Errorf("dry run feature is disabled")
	}
	if v.Reader == nil {
		return fmt.Errorf("dry run validation is unavailable")
	}

	promise := &platformv1alpha1.Promise{}
	if err := v.Reader.Get(ctx, types.NamespacedName{Name: dryRun.Spec.PromiseRef.Name}, promise); err != nil {
		return fmt.Errorf("getting Promise %s: %w", dryRun.Spec.PromiseRef.Name, err)
	}

	revision, err := selectedDryRunRevision(ctx, v.Reader, promise, dryRun.Spec.PromiseRef.Version)
	if err != nil {
		return err
	}
	if !revision.Spec.PromiseSpec.Workflows.Config.SupportsDryRun() {
		return fmt.Errorf("promise %s revision %s does not support dry runs", promise.Name, revision.Spec.Version)
	}
	return nil
}

func selectedDryRunRevision(
	ctx context.Context, reader client.Reader, promise *platformv1alpha1.Promise, version string,
) (*platformv1alpha1.PromiseRevision, error) {
	revisions := &platformv1alpha1.PromiseRevisionList{}
	if err := reader.List(ctx, revisions, &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(promise.GenerateSharedLabels()),
	}); err != nil {
		return nil, fmt.Errorf("listing PromiseRevisions for Promise %s: %w", promise.Name, err)
	}

	if version == "" || version == "latest" {
		for i := range revisions.Items {
			if revisions.Items[i].Status.Latest {
				return &revisions.Items[i], nil
			}
		}
		return nil, fmt.Errorf("cannot find latest PromiseRevision for Promise %s", promise.Name)
	}

	var selected *platformv1alpha1.PromiseRevision
	for i := range revisions.Items {
		if revisions.Items[i].Spec.Version != version {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("multiple PromiseRevisions for Promise %s with version %s", promise.Name, version)
		}
		selected = &revisions.Items[i]
	}
	if selected == nil {
		return nil, fmt.Errorf("cannot find PromiseRevision for Promise %s with version %s", promise.Name, version)
	}
	return selected, nil
}
