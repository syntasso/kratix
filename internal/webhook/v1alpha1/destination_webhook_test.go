package v1alpha1_test

import (
	"context"

	kratixWebhook "github.com/syntasso/kratix/internal/webhook/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		fakeClient = newFakeClientWithStateStoreRefIndex()

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

	Describe("validating state store references", func() {
		BeforeEach(func() {
			destination.Spec.Path = "destination-path"
		})

		It("fails when stateStoreRef is missing", func() {
			destination.Spec.StateStoreRef = nil

			warnings, err := validator.ValidateCreate(ctx, destination)

			Expect(err).To(MatchError("stateStoreRef field is required"))
			Expect(warnings).To(BeEmpty())
		})

		It("warns when the referenced BucketStateStore does not exist", func() {
			destination.Spec.StateStoreRef = &v1alpha1.StateStoreReference{
				Name: "missing-bucket-state-store",
				Kind: "BucketStateStore",
			}

			warnings, err := validator.ValidateCreate(ctx, destination)

			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ConsistOf(
				`Warning: referenced BucketStateStore "missing-bucket-state-store" does not exist`,
			))
		})

		It("warns when the referenced GitStateStore does not exist", func() {
			destination.Spec.StateStoreRef = &v1alpha1.StateStoreReference{
				Name: "missing-git-state-store",
				Kind: "GitStateStore",
			}

			warnings, err := validator.ValidateCreate(ctx, destination)

			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ConsistOf(
				`Warning: referenced GitStateStore "missing-git-state-store" does not exist`,
			))
		})

		It("does not warn when the referenced state store exists", func() {
			stateStore := &v1alpha1.GitStateStore{
				ObjectMeta: metav1.ObjectMeta{
					Name: "existing-git-state-store",
				},
			}
			Expect(fakeClient.Create(ctx, stateStore)).To(Succeed())

			destination.Spec.StateStoreRef = &v1alpha1.StateStoreReference{
				Name: stateStore.Name,
				Kind: "GitStateStore",
			}

			warnings, err := validator.ValidateCreate(ctx, destination)

			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(BeEmpty())
		})

		It("warns on update when the referenced state store does not exist", func() {
			destination.Spec.StateStoreRef = &v1alpha1.StateStoreReference{
				Name: "missing-git-state-store",
				Kind: "GitStateStore",
			}

			warnings, err := validator.ValidateUpdate(ctx, &v1alpha1.Destination{}, destination)

			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ConsistOf(
				`Warning: referenced GitStateStore "missing-git-state-store" does not exist`,
			))
		})
	})

	Describe("validating filepath.mode against the state store's delivery mode", func() {
		const docsURL = "see https://docs.kratix.io/main/reference/destinations/intro"

		BeforeEach(func() {
			destination.Spec.Path = "destination-path"
			destination.Spec.Filepath.Mode = v1alpha1.FilepathModeAggregatedYAML
		})

		for _, kind := range []string{"GitStateStore", "BucketStateStore"} {
			When("the referenced "+kind+" has a delivery mode", func() {
				BeforeEach(func() {
					createStateStore(ctx, fakeClient, kind, "mode-store", &v1alpha1.Delivery{Mode: "x"})
					destination.Spec.StateStoreRef = &v1alpha1.StateStoreReference{Name: "mode-store", Kind: kind}
				})

				It("refuses an aggregatedYAML Destination on create", func() {
					warnings, err := validator.ValidateCreate(ctx, destination)

					Expect(err).To(MatchError(
						`filepath.mode aggregatedYAML cannot be used with a state store that has a delivery mode (` +
							kind + `/mode-store has delivery.mode "x"); ` + docsURL,
					))
					Expect(warnings).To(BeEmpty())
				})

				It("refuses an update into aggregatedYAML", func() {
					oldDestination := destination.DeepCopy()
					oldDestination.Spec.Filepath.Mode = v1alpha1.FilepathModeNestedByMetadata

					_, err := validator.ValidateUpdate(ctx, oldDestination, destination)

					Expect(err).To(MatchError(ContainSubstring(
						`filepath.mode aggregatedYAML cannot be used with a state store that has a delivery mode (` + kind + `/mode-store`,
					)))
				})

				It("allows the update of an aggregatedYAML Destination that is being deleted", func() {
					beingDeleted := destination.DeepCopy()
					beingDeleted.DeletionTimestamp = &metav1.Time{}
					beingDeleted.Finalizers = []string{"kratix.io/test"}

					_, err := validator.ValidateUpdate(ctx, destination, beingDeleted)
					Expect(err).NotTo(HaveOccurred())

					_, err = validator.ValidateUpdate(ctx, destination, destination)
					Expect(err).To(MatchError(ContainSubstring("filepath.mode aggregatedYAML cannot be used")))
				})

				for _, mode := range []string{v1alpha1.FilepathModeNestedByMetadata, v1alpha1.FilepathModeNone} {
					It("allows a "+mode+" Destination", func() {
						destination.Spec.Filepath.Mode = mode

						warnings, err := validator.ValidateCreate(ctx, destination)

						Expect(err).NotTo(HaveOccurred())
						Expect(warnings).To(BeEmpty())
					})
				}
			})

			When("the referenced "+kind+" does not exist", func() {
				It("only warns about an aggregatedYAML Destination", func() {
					destination.Spec.StateStoreRef = &v1alpha1.StateStoreReference{Name: "missing-store", Kind: kind}

					warnings, err := validator.ValidateCreate(ctx, destination)

					Expect(err).NotTo(HaveOccurred())
					Expect(warnings).To(ConsistOf(`Warning: referenced ` + kind + ` "missing-store" does not exist`))
				})
			})

			When("the referenced "+kind+" has no delivery", func() {
				It("allows an aggregatedYAML Destination", func() {
					createStateStore(ctx, fakeClient, kind, "plain-store", nil)
					destination.Spec.StateStoreRef = &v1alpha1.StateStoreReference{Name: "plain-store", Kind: kind}

					warnings, err := validator.ValidateCreate(ctx, destination)

					Expect(err).NotTo(HaveOccurred())
					Expect(warnings).To(BeEmpty())
				})
			})

			When("the referenced "+kind+" has a delivery with no mode", func() {
				It("allows an aggregatedYAML Destination", func() {
					createStateStore(ctx, fakeClient, kind, "empty-delivery-store", &v1alpha1.Delivery{})
					destination.Spec.StateStoreRef = &v1alpha1.StateStoreReference{Name: "empty-delivery-store", Kind: kind}

					warnings, err := validator.ValidateCreate(ctx, destination)

					Expect(err).NotTo(HaveOccurred())
					Expect(warnings).To(BeEmpty())
				})
			})
		}
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
