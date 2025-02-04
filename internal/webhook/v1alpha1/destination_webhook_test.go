package v1alpha1_test

import (
	"context"

	kratixWebhook "github.com/syntasso/kratix/internal/webhook/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
)

var _ = Describe("Destination Webhook", func() {
	var (
		defaulter   *kratixWebhook.DestinationCustomDefaulter
		validator   *kratixWebhook.DestinationCustomValidator
		destination *v1alpha1.Destination
		fakeClient  client.Client
	)

	ctx := context.TODO()

	BeforeEach(func() {
		err := v1alpha1.AddToScheme(scheme.Scheme)
		Expect(err).NotTo(HaveOccurred())
		fakeClient = clientfake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		defaulter = &kratixWebhook.DestinationCustomDefaulter{
			Client: fakeClient,
		}
		validator = &kratixWebhook.DestinationCustomValidator{}

		destination = &v1alpha1.Destination{
			ObjectMeta: metav1.ObjectMeta{
				Name: "myDestination",
			},
			Spec: v1alpha1.DestinationSpec{},
		}
	})

	Describe("defaulting `path`", func() {
		When("the destination does not exist", func() {
			It("does not default the path", func() {
				destination.Spec.Path = "."
				err := defaulter.Default(ctx, destination)
				Expect(err).NotTo(HaveOccurred())
				Expect(destination.Spec.Path).To(Equal("."))
			})

			It("adds the skip annotation", func() {
				err := defaulter.Default(ctx, destination)
				Expect(err).NotTo(HaveOccurred())
				Expect(destination.Annotations[kratixWebhook.SkipPathDefaultingAnnotation]).To(Equal("true"))
			})
		})

		When("the destination exists", func() {
			BeforeEach(func() {
				destination.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}
				Expect(fakeClient.Create(ctx, destination)).To(Succeed())
			})

			When("the annotation is not set", func() {
				It("adds a path defaulted annotation", func() {
					err := defaulter.Default(ctx, destination)
					Expect(err).NotTo(HaveOccurred())
					Expect(destination.Annotations[kratixWebhook.SkipPathDefaultingAnnotation]).To(Equal("true"))
				})

				When("path is not set", func() {
					It("sets it to the destination name", func() {
						err := defaulter.Default(ctx, destination)
						Expect(err).NotTo(HaveOccurred())
						Expect(destination.Spec.Path).To(Equal("myDestination"))
					})
				})

				When("path is set", func() {
					It("prefixes it with the destination name", func() {
						destination.Spec.Path = "subPath"
						err := defaulter.Default(ctx, destination)
						Expect(err).NotTo(HaveOccurred())
						Expect(destination.Spec.Path).To(Equal("myDestination/subPath"))
					})
				})
			})

			When("the skip annotation is set", func() {
				BeforeEach(func() {
					destination.SetAnnotations(map[string]string{kratixWebhook.SkipPathDefaultingAnnotation: "true"})
					Expect(fakeClient.Update(ctx, destination)).To(Succeed())
				})

				When("the path is set", func() {
					It("does not change the path", func() {
						destination.Spec.Path = "mydir"

						err := defaulter.Default(ctx, destination)
						Expect(err).NotTo(HaveOccurred())
						Expect(destination.Spec.Path).To(Equal("mydir"))
					})
				})

				When("the path is not set", func() {
					It("does not change the path", func() {
						err := defaulter.Default(ctx, destination)
						Expect(err).NotTo(HaveOccurred())
						Expect(destination.Spec.Path).To(BeEmpty())
					})
				})

				When("the annotation is removed", func() {
					BeforeEach(func() {
						destination.SetAnnotations(nil)
					})

					It("does not change the path", func() {
						destination.Spec.Path = "subdir"
						err := defaulter.Default(ctx, destination)
						Expect(err).NotTo(HaveOccurred())
						Expect(destination.Spec.Path).To(Equal("subdir"))
					})

					It("restores the annotation", func() {
						err := defaulter.Default(ctx, destination)
						Expect(err).NotTo(HaveOccurred())
						Expect(destination.Annotations[kratixWebhook.SkipPathDefaultingAnnotation]).To(Equal("true"))
					})
				})
			})
		})
	})

	Describe("validating `path`", func() {
		When("creating a new destination", func() {
			When("path is set", func() {
				It("succeeds", func() {
					destination.Spec.Path = "valid-path"
					warnings, err := validator.ValidateCreate(ctx, destination)
					Expect(err).NotTo(HaveOccurred())
					Expect(warnings).To(BeEmpty())
				})
			})

			When("path is empty", func() {
				It("fails with validation error", func() {
					destination.Spec.Path = ""
					warnings, err := validator.ValidateCreate(ctx, destination)
					Expect(err).To(MatchError(ContainSubstring("path field is required")))
					Expect(warnings).To(BeEmpty())
				})
			})
		})

		When("updating a destination", func() {
			When("path is set", func() {
				It("succeeds", func() {
					destination.Spec.Path = "valid-path"
					warnings, err := validator.ValidateUpdate(ctx, destination, destination)
					Expect(err).NotTo(HaveOccurred())
					Expect(warnings).To(BeEmpty())
				})
			})

			When("path is empty", func() {
				It("fails with validation error", func() {
					destination.Spec.Path = ""
					warnings, err := validator.ValidateUpdate(ctx, destination, destination)
					Expect(err).To(MatchError(ContainSubstring("path field is required")))
					Expect(warnings).To(BeEmpty())
				})
			})
		})
	})
})
