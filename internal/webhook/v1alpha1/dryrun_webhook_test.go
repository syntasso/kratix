package v1alpha1_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
	kratixWebhook "github.com/syntasso/kratix/internal/webhook/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var _ = Describe("DryRun Webhook", func() {
	var (
		dryRun  *platformv1alpha1.DryRun
		promise *platformv1alpha1.Promise
	)

	BeforeEach(func() {
		Expect(platformv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
		dryRun = &platformv1alpha1.DryRun{
			Spec: platformv1alpha1.DryRunSpec{
				PromiseRef: platformv1alpha1.DryRunPromiseRef{Name: "redis"},
			},
		}
		promise = &platformv1alpha1.Promise{ObjectMeta: metav1.ObjectMeta{Name: "redis"}}
	})

	revision := func(version string, latest, supported bool) *platformv1alpha1.PromiseRevision {
		return &platformv1alpha1.PromiseRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "redis-" + version,
				Labels: map[string]string{platformv1alpha1.PromiseNameLabel: "redis"},
			},
			Spec: platformv1alpha1.PromiseRevisionSpec{
				PromiseRef: platformv1alpha1.PromiseRef{Name: "redis"},
				PromiseSpec: platformv1alpha1.PromiseSpec{Workflows: platformv1alpha1.Workflows{
					Config: platformv1alpha1.WorkflowConfig{DryRun: &platformv1alpha1.DryRunWorkflowConfig{Supported: supported}},
				}},
				Version: version,
			},
			Status: platformv1alpha1.PromiseRevisionStatus{Latest: latest},
		}
	}

	validator := func(enabled bool, objects ...*platformv1alpha1.PromiseRevision) *kratixWebhook.DryRunCustomValidator {
		clientObjects := make([]runtime.Object, 0, len(objects)+1)
		clientObjects = append(clientObjects, promise)
		for _, object := range objects {
			clientObjects = append(clientObjects, object)
		}
		return &kratixWebhook.DryRunCustomValidator{
			Reader:  clientfake.NewClientBuilder().WithScheme(scheme.Scheme).WithRuntimeObjects(clientObjects...).Build(),
			Enabled: enabled,
		}
	}

	It("rejects every DryRun when the feature flag is off", func() {
		_, err := validator(false, revision("v1.0.0", true, true)).ValidateCreate(context.Background(), dryRun)
		Expect(err).To(MatchError(ContainSubstring("dry run feature is not supported")))
	})

	It("rejects when the selected latest revision does not support dry runs", func() {
		_, err := validator(true, revision("v1.0.0", true, false)).ValidateCreate(context.Background(), dryRun)
		Expect(err).To(MatchError(ContainSubstring("v1.0.0 does not support dry runs")))
	})

	It("accepts when the selected latest revision supports dry runs", func() {
		_, err := validator(true, revision("v1.0.0", true, true)).ValidateCreate(context.Background(), dryRun)
		Expect(err).NotTo(HaveOccurred())
	})

	It("validates the exact requested revision rather than latest", func() {
		dryRun.Spec.PromiseRef.Version = "v1.0.0"
		_, err := validator(true,
			revision("v1.0.0", false, true),
			revision("v2.0.0", true, false),
		).ValidateCreate(context.Background(), dryRun)
		Expect(err).NotTo(HaveOccurred())
	})

	It("treats an absent declaration as unsupported", func() {
		unsupported := revision("v1.0.0", true, false)
		unsupported.Spec.PromiseSpec.Workflows.Config.DryRun = nil
		_, err := validator(true, unsupported).ValidateCreate(context.Background(), dryRun)
		Expect(err).To(MatchError(ContainSubstring("does not support dry runs")))
	})

	It("returns an actionable error when the requested revision does not exist", func() {
		dryRun.Spec.PromiseRef.Version = "v9.0.0"
		_, err := validator(true, revision("v1.0.0", true, true)).ValidateCreate(context.Background(), dryRun)
		Expect(err).To(MatchError(ContainSubstring("cannot find PromiseRevision for Promise redis with version v9.0.0")))
	})

	It("allows updates to an existing DryRun", func() {
		_, err := validator(false).ValidateUpdate(context.Background(), dryRun, dryRun)
		Expect(err).NotTo(HaveOccurred())
	})

	It("allows deletion", func() {
		_, err := validator(false).ValidateDelete(context.Background(), dryRun)
		Expect(err).NotTo(HaveOccurred())
	})
})
