package v1alpha1_test

import (
	"context"
	"fmt"

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

		// Create a fake client builder with indexing
		fakeClient = clientfake.NewClientBuilder().WithScheme(scheme.Scheme).
			WithIndex(&v1alpha1.Destination{}, "stateStoreRef", func(obj client.Object) []string {
				dst := obj.(*v1alpha1.Destination)
				return []string{
					fmt.Sprintf("%s.%s",
						dst.Spec.StateStoreRef.Kind,
						dst.Spec.StateStoreRef.Name,
					),
				}
			}).
			Build()

		defaulter = &kratixWebhook.DestinationCustomDefaulter{
			Client: fakeClient,
		}
		validator = &kratixWebhook.DestinationCustomValidator{
			Client: fakeClient,
		}

		destination = &v1alpha1.Destination{
			ObjectMeta: metav1.ObjectMeta{
				Name: "myDestination",
			},
			Spec: v1alpha1.DestinationSpec{},
		}
	})

	Describe("defaulting `path`", func() {
		Describe("for new destinations", func() {
			It("adds the skip annotation", func() {
				err := defaulter.Default(ctx, destination)
				Expect(err).NotTo(HaveOccurred())
				Expect(destination.Annotations[v1alpha1.SkipPathDefaultingAnnotation]).To(Equal("true"))
			})

			When("the path is empty", func() {
				It("uses the destination name as the default path", func() {
					destination.Spec.Path = ""
					err := defaulter.Default(ctx, destination)
					Expect(err).NotTo(HaveOccurred())
					Expect(destination.Spec.Path).To(Equal(destination.Name))
				})
			})
		})

		Describe("for existing destinations with the skip annotation", func() {
			BeforeEach(func() {
				destination.SetAnnotations(
					map[string]string{v1alpha1.SkipPathDefaultingAnnotation: "true"},
				)
				Expect(fakeClient.Create(ctx, destination)).To(Succeed())
			})

			When("the incoming path is empty", func() {
				It("uses the destination name as the default path", func() {
					destination.Spec.Path = ""
					err := defaulter.Default(ctx, destination)
					Expect(err).NotTo(HaveOccurred())
					Expect(destination.Spec.Path).To(Equal(destination.Name))
				})
			})

			When("the annotation is removed", func() {
				BeforeEach(func() {
					destination.SetAnnotations(nil)
				})

				It("restores the annotation", func() {
					err := defaulter.Default(ctx, destination)
					Expect(err).NotTo(HaveOccurred())
					Expect(destination.Annotations[v1alpha1.SkipPathDefaultingAnnotation]).To(Equal("true"))
				})
			})
		})
	})

	Describe("validating path uniqueness", func() {
		var (
			existingDestination *v1alpha1.Destination
			stateStore          *v1alpha1.BucketStateStore
		)

		BeforeEach(func() {
			stateStore = &v1alpha1.BucketStateStore{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-state-store",
				},
			}
			Expect(fakeClient.Create(ctx, stateStore)).To(Succeed())

			existingDestination = &v1alpha1.Destination{
				ObjectMeta: metav1.ObjectMeta{
					Name: "existing-destination",
				},
				Spec: v1alpha1.DestinationSpec{
					Path: "existing-path",
					StateStoreRef: &v1alpha1.StateStoreReference{
						Name: stateStore.Name,
						Kind: "BucketStateStore",
					},
				},
			}
			Expect(fakeClient.Create(ctx, existingDestination)).To(Succeed())

			destination.Spec.StateStoreRef = &v1alpha1.StateStoreReference{
				Name: stateStore.Name,
				Kind: "BucketStateStore",
			}
		})

		When("creating a new destination", func() {
			When("it uses the same state store as an existing destination", func() {
				It("fails when the path clashes", func() {
					destination.Spec.Path = existingDestination.Spec.Path
					warnings, err := validator.ValidateCreate(ctx, destination)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(
						ContainSubstring(
							"destination path 'existing-path' already exists for state store 'test-state-store'",
						),
					)
					Expect(warnings).To(BeEmpty())
				})

				It("succeeds when the path is unique", func() {
					destination.Spec.Path = "new-unique-path"
					warnings, err := validator.ValidateCreate(ctx, destination)
					Expect(err).NotTo(HaveOccurred())
					Expect(warnings).To(BeEmpty())
				})
			})

			When("it uses a different state store", func() {
				BeforeEach(func() {
					anotherStateStore := &v1alpha1.GitStateStore{
						ObjectMeta: metav1.ObjectMeta{
							Name: stateStore.Name,
						},
					}
					Expect(fakeClient.Create(ctx, anotherStateStore)).To(Succeed())

					destination.Spec.StateStoreRef = &v1alpha1.StateStoreReference{
						Name: anotherStateStore.Name,
						Kind: "GitStateStore",
					}
				})

				It("succeeds", func() {
					destination.Spec.Path = existingDestination.Spec.Path
					warnings, err := validator.ValidateCreate(ctx, destination)
					Expect(err).NotTo(HaveOccurred())
					Expect(warnings).To(BeEmpty())
				})
			})
		})

	})

	Describe("defaulting filename", func() {
		It("sets the filename when the filepath.mode requires it", func() {
			destination.Spec.Filepath = v1alpha1.Filepath{
				Mode: v1alpha1.FilepathModeAggregatedYAML,
			}
			err := defaulter.Default(ctx, destination)
			Expect(err).NotTo(HaveOccurred())
			Expect(destination.Spec.Filepath.Filename).To(Equal("aggregated.yaml"))
		})

		It("does not set a default filename when the filepath.mode doesn't require it", func() {
			for _, mode := range []string{
				v1alpha1.FilepathModeNestedByMetadata,
				v1alpha1.FilepathModeNone,
			} {
				destination.Spec.Filepath = v1alpha1.Filepath{Mode: mode}
				err := defaulter.Default(ctx, destination)
				Expect(err).NotTo(HaveOccurred())
				Expect(destination.Spec.Filepath.Filename).To(BeEmpty())
			}
		})
	})
})
