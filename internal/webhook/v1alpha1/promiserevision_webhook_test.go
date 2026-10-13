package v1alpha1_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	kratixWebhook "github.com/syntasso/kratix/internal/webhook/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	platformv1alpha1 "github.com/syntasso/kratix/api/v1alpha1"
)

var _ = Describe("PromiseRevision Webhook", func() {
	var (
		obj       *platformv1alpha1.PromiseRevision
		validator *kratixWebhook.PromiseRevisionCustomValidator
	)

	BeforeEach(func() {
		obj = &platformv1alpha1.PromiseRevision{}
		validator = &kratixWebhook.PromiseRevisionCustomValidator{}
		Expect(validator).NotTo(BeNil())
		Expect(obj).NotTo(BeNil())
	})

	Context("request comes from a regular user", func() {
		When("deleting PromiseRevision under Validating Webhook", func() {
			It("should deny deletion if revision .status.latest is true", func() {
				obj.Status.Latest = true
				ctx := newCtxWithUserInfo("unit-test-user")
				Expect(validator.ValidateDelete(ctx, obj)).Error().To(HaveOccurred())
			})

			It("allows deletion when revision .status.latest is not set to true", func() {
				ctx := newCtxWithUserInfo("unit-test-user")
				Expect(validator.ValidateDelete(ctx, obj)).Error().NotTo(HaveOccurred())
			})
		})

		When("deleting a PromiseRevision whose version is still used by ResourceBindings", func() {
			binding := func(name, promiseName, version string) *platformv1alpha1.ResourceBinding {
				return &platformv1alpha1.ResourceBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: "default",
						Labels:    map[string]string{platformv1alpha1.PromiseNameLabel: promiseName},
					},
					Spec: platformv1alpha1.ResourceBindingSpec{Version: version},
				}
			}

			BeforeEach(func() {
				obj.Name = "redis-v1-0-0"
				obj.Spec.PromiseRef.Name = "redis"
				obj.Spec.Version = "v1.0.0"
			})

			withBindings := func(bindings ...client.Object) {
				Expect(platformv1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())
				validator.Client = clientfake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(bindings...).Build()
			}

			It("allows the deletion and warns that the bindings and their resources are kept", func() {
				withBindings(
					binding("a", "redis", "v1.0.0"),
					binding("b", "redis", "v1.0.0"),
				)
				warnings, err := validator.ValidateDelete(newCtxWithUserInfo("unit-test-user"), obj)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(ConsistOf(SatisfyAll(
					ContainSubstring(`"redis-v1-0-0"`),
					ContainSubstring("2 ResourceBinding(s)"),
					ContainSubstring("v1.0.0"),
					ContainSubstring("not deleted"),
				)))
			})

			It("does not warn about bindings on another version or another Promise", func() {
				withBindings(
					binding("a", "redis", "v2.0.0"),
					binding("b", "postgres", "v1.0.0"),
				)
				warnings, err := validator.ValidateDelete(newCtxWithUserInfo("unit-test-user"), obj)
				Expect(err).NotTo(HaveOccurred())
				Expect(warnings).To(BeEmpty())
			})
		})
	})

	Context("request comes from a service account from the kratix-platform-system namespace", func() {
		When("deleting PromiseRevision under Validating Webhook", func() {
			It("allows deletion even if revision .status.latest is true", func() {
				obj.Status.Latest = true
				ctx := newCtxWithUserInfo("system:serviceaccount:kratix-platform-system:kratix-platform-controller-manager")
				Expect(validator.ValidateDelete(ctx, obj)).Error().NotTo(HaveOccurred())
			})
		})
	})

	Context("validating the reconciliation-interval annotation", func() {
		When("creating a PromiseRevision", func() {
			It("rejects a value below the floor", func() {
				obj.Annotations = map[string]string{
					platformv1alpha1.ReconciliationIntervalAnnotation: "30s",
				}
				_, err := validator.ValidateCreate(context.Background(), obj)
				Expect(err).To(MatchError(ContainSubstring("must be at least 1m0s")))
			})

			It("accepts a value exactly at the floor", func() {
				obj.Annotations = map[string]string{
					platformv1alpha1.ReconciliationIntervalAnnotation: "1m",
				}
				_, err := validator.ValidateCreate(context.Background(), obj)
				Expect(err).NotTo(HaveOccurred())
			})

			It("rejects a value that does not parse as a duration", func() {
				obj.Annotations = map[string]string{
					platformv1alpha1.ReconciliationIntervalAnnotation: "soon",
				}
				_, err := validator.ValidateCreate(context.Background(), obj)
				Expect(err).To(MatchError(ContainSubstring("must be a valid duration")))
			})

			It("allows a PromiseRevision without the annotation", func() {
				_, err := validator.ValidateCreate(context.Background(), obj)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		When("updating a PromiseRevision", func() {
			It("rejects a value below the floor", func() {
				obj.Annotations = map[string]string{
					platformv1alpha1.ReconciliationIntervalAnnotation: "30s",
				}
				_, err := validator.ValidateUpdate(context.Background(), &platformv1alpha1.PromiseRevision{}, obj)
				Expect(err).To(MatchError(ContainSubstring("must be at least 1m0s")))
			})

			It("rejects a value that does not parse as a duration", func() {
				obj.Annotations = map[string]string{
					platformv1alpha1.ReconciliationIntervalAnnotation: "soon",
				}
				_, err := validator.ValidateUpdate(context.Background(), &platformv1alpha1.PromiseRevision{}, obj)
				Expect(err).To(MatchError(ContainSubstring("must be a valid duration")))
			})

			It("accepts a value exactly at the floor", func() {
				obj.Annotations = map[string]string{
					platformv1alpha1.ReconciliationIntervalAnnotation: "1m",
				}
				_, err := validator.ValidateUpdate(context.Background(), &platformv1alpha1.PromiseRevision{}, obj)
				Expect(err).NotTo(HaveOccurred())
			})

			It("accepts an update that leaves an existing out-of-policy value untouched", func() {
				oldObj := &platformv1alpha1.PromiseRevision{}
				oldObj.Annotations = map[string]string{
					platformv1alpha1.ReconciliationIntervalAnnotation: "30s",
				}
				obj.Annotations = map[string]string{
					platformv1alpha1.ReconciliationIntervalAnnotation: "30s",
				}
				_, err := validator.ValidateUpdate(context.Background(), oldObj, obj)
				Expect(err).NotTo(HaveOccurred())
			})

			It("rejects an update that changes an out-of-policy value to a different out-of-policy value", func() {
				oldObj := &platformv1alpha1.PromiseRevision{}
				oldObj.Annotations = map[string]string{
					platformv1alpha1.ReconciliationIntervalAnnotation: "30s",
				}
				obj.Annotations = map[string]string{
					platformv1alpha1.ReconciliationIntervalAnnotation: "10s",
				}
				_, err := validator.ValidateUpdate(context.Background(), oldObj, obj)
				Expect(err).To(MatchError(ContainSubstring("must be at least 1m0s")))
			})
		})
	})

})

func newCtxWithUserInfo(username string) context.Context {
	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			UserInfo: authv1.UserInfo{
				Username: username,
			},
		},
	}
	return admission.NewContextWithRequest(context.Background(), req)
}
