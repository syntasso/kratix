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

	dryRunConfig := func(supported bool) platformv1alpha1.WorkflowConfig {
		return platformv1alpha1.WorkflowConfig{DryRun: &platformv1alpha1.DryRunWorkflowConfig{Supported: supported}}
	}

	BeforeEach(func() {
		Expect(platformv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
		dryRun = &platformv1alpha1.DryRun{
			Spec: platformv1alpha1.DryRunSpec{
				PromiseRef: platformv1alpha1.DryRunPromiseRef{Name: "redis"},
			},
		}
		promise = &platformv1alpha1.Promise{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "redis",
				Labels: map[string]string{platformv1alpha1.PromiseVersionLabel: "v2.0.0"},
			},
			Spec: platformv1alpha1.PromiseSpec{Workflows: platformv1alpha1.Workflows{Config: dryRunConfig(true)}},
		}
	})

	revision := func(version string, supported bool) *platformv1alpha1.PromiseRevision {
		return &platformv1alpha1.PromiseRevision{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "redis-" + version,
				Labels: map[string]string{platformv1alpha1.PromiseNameLabel: "redis"},
			},
			Spec: platformv1alpha1.PromiseRevisionSpec{
				PromiseRef:  platformv1alpha1.PromiseRef{Name: "redis"},
				PromiseSpec: platformv1alpha1.PromiseSpec{Workflows: platformv1alpha1.Workflows{Config: dryRunConfig(supported)}},
				Version:     version,
			},
		}
	}

	validator := func(enabled bool, revisions ...*platformv1alpha1.PromiseRevision) *kratixWebhook.DryRunCustomValidator {
		objects := []runtime.Object{promise}
		for _, r := range revisions {
			objects = append(objects, r)
		}
		return &kratixWebhook.DryRunCustomValidator{
			Reader:  clientfake.NewClientBuilder().WithScheme(scheme.Scheme).WithRuntimeObjects(objects...).Build(),
			Enabled: enabled,
		}
	}

	It("rejects every DryRun when the feature flag is off", func() {
		_, err := validator(false).ValidateCreate(context.Background(), dryRun)
		Expect(err).To(MatchError(ContainSubstring("dry run feature is not supported")))
	})

	When("no version is requested", func() {
		DescribeTable("checks the Promise's dry run support",
			func(config *platformv1alpha1.DryRunWorkflowConfig, expectedErr string) {
				promise.Spec.Workflows.Config.DryRun = config
				_, err := validator(true).ValidateCreate(context.Background(), dryRun)
				if expectedErr == "" {
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(err).To(MatchError(ContainSubstring(expectedErr)))
				}
			},
			Entry("accepts when the Promise supports dry runs",
				&platformv1alpha1.DryRunWorkflowConfig{Supported: true}, ""),
			Entry("rejects when the Promise does not support dry runs",
				&platformv1alpha1.DryRunWorkflowConfig{Supported: false}, "promise redis version v2.0.0 does not support dry runs"),
			Entry("rejects when dryRun config is not set on the Promise",
				nil, "promise redis version v2.0.0 does not support dry runs"),
		)
	})

	When("an exact version is requested", func() {
		DescribeTable("checks the requested promise revision's dry run support",
			func(requestedVersion string, promiseSupported, revisionSupported bool, expectedErr string) {
				dryRun.Spec.PromiseRef.Version = requestedVersion
				promise.Spec.Workflows.Config = dryRunConfig(promiseSupported)
				_, err := validator(true, revision("v1.0.0", revisionSupported)).ValidateCreate(context.Background(), dryRun)
				if expectedErr == "" {
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(err).To(MatchError(ContainSubstring(expectedErr)))
				}
			},
			Entry("accepts a supported revision even when the latest Promise does not support dry runs",
				"v1.0.0", false, true, ""),
			Entry("rejects an unsupported revision when the latest Promise supports dry runs",
				"v1.0.0", true, false, "promise redis version v1.0.0 does not support dry runs"),
			Entry("rejects a version that does not exist",
				"v9.0.0", true, true, "cannot find PromiseRevision for Promise redis with version v9.0.0"),
		)
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
