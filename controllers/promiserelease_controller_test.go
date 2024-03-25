package controllers_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/api/v1alpha1/v1alpha1fakes"
	"github.com/syntasso/kratix/controllers"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("PromiseReleaseController", func() {
	var (
		promiseRelease               v1alpha1.PromiseRelease
		promiseReleaseNamespacedName types.NamespacedName
		reconciler                   *controllers.PromiseReleaseReconciler
		fakeFetcher                  *v1alpha1fakes.FakePromiseFetcher
		promise                      *v1alpha1.Promise
		err                          error
		result                       ctrl.Result
	)

	BeforeEach(func() {
		fakeFetcher = &v1alpha1fakes.FakePromiseFetcher{}
		fakeFetcher.FromURLReturns(promiseFromFile(promisePath), nil)

		reconciler = &controllers.PromiseReleaseReconciler{
			Client:         fakeK8sClient,
			Scheme:         scheme.Scheme,
			PromiseFetcher: fakeFetcher,
			Log:            ctrl.Log.WithName("controllers").WithName("PromiseRelease"),
		}

		promiseRelease = v1alpha1.PromiseRelease{
			ObjectMeta: metav1.ObjectMeta{
				Name: "redis",
			},
			TypeMeta: metav1.TypeMeta{
				Kind:       "PromiseRelease",
				APIVersion: "platform.kratix.io/v1alpha1",
			},
			Spec: v1alpha1.PromiseReleaseSpec{
				Version: "v1.1.0",
				SourceRef: v1alpha1.SourceRef{
					Type: "http",
					URL:  "example.com",
				},
			},
		}

		promiseReleaseNamespacedName = types.NamespacedName{Name: "redis"}
	})

	When("the PromiseRelease is installed", func() {
		When("the Promise already exists outside of a PromiseRelease", func() {
			BeforeEach(func() {
				promise = promiseFromFile(promisePath)
				err := fakeK8sClient.Create(context.TODO(), promise)
				Expect(err).NotTo(HaveOccurred())
				// Install the PromiseRelease
				err = fakeK8sClient.Create(context.TODO(), &promiseRelease)
				Expect(err).NotTo(HaveOccurred())

				_, err = t.reconcileUntilCompletion(reconciler, &promiseRelease)
				Expect(err).NotTo(HaveOccurred(), "reconciliation failed; expected it to work")

				promise = fetchPromise(promiseReleaseNamespacedName)
			})

			It("sets the promise as a dependent of the promise release", func() {
				tru := true
				Expect(promise.GetOwnerReferences()).To(ContainElement(metav1.OwnerReference{
					APIVersion:         "platform.kratix.io/v1alpha1",
					Kind:               "PromiseRelease",
					Name:               "redis",
					Controller:         &tru,
					BlockOwnerDeletion: &tru,
				}))
			})

			It("adds the promise release labels to the promise", func() {
				Expect(promise.Labels).To(SatisfyAll(
					HaveKeyWithValue("kratix.io/promise-version", "v1.1.0"),
					HaveKeyWithValue("kratix.io/promise-release-name", "redis"),
				))
			})

			It("sets the promise release status to installed", func() {
				err := fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
				Expect(err).NotTo(HaveOccurred())
				Expect(promiseRelease.Status.Status).To(Equal("Installed"))
				Expect(promiseRelease.Status.Conditions).To(HaveLen(1))
				Expect(promiseRelease.Status.Conditions[0].Type).To(Equal("Installed"))
				Expect(promiseRelease.Status.Conditions[0].Status).To(Equal(metav1.ConditionTrue))
				Expect(promiseRelease.Status.Conditions[0].Message).To(Equal("Installed successfully"))
				Expect(promiseRelease.Status.Conditions[0].Reason).To(Equal("InstalledSuccessfully"))
			})
		})

		When("the sourceRef type is http", func() {
			When("the url fetcher errors", func() {
				BeforeEach(func() {
					fakeFetcher.FromURLReturns(nil, fmt.Errorf("can't do mate"))
					err = fakeK8sClient.Create(context.TODO(), &promiseRelease)
					Expect(err).NotTo(HaveOccurred())
					_, err = t.reconcileUntilCompletion(reconciler, &promiseRelease)
				})

				It("errors", func() {
					Expect(err).To(MatchError("failed to fetch promise from url: can't do mate"))
				})

				It("updates the status", func() {
					err := fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
					Expect(err).NotTo(HaveOccurred())
					Expect(promiseRelease.Status.Status).To(Equal("Error installing"))
					Expect(promiseRelease.Status.Conditions).To(HaveLen(1))
					Expect(promiseRelease.Status.Conditions[0].Type).To(Equal("Installed"))
					Expect(promiseRelease.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))
					Expect(promiseRelease.Status.Conditions[0].Message).To(Equal("Failed to fetch Promise from URL"))
					Expect(promiseRelease.Status.Conditions[0].Reason).To(Equal("FailedToFetchPromise"))
				})
			})

			When("the url fetcher succeeds", func() {
				When("the Promise has no defined version", func() {

					BeforeEach(func() {
						unversionedPromise := promiseFromFile(promisePath)
						unversionedPromise.Labels = nil
						fakeFetcher.FromURLReturns(unversionedPromise, nil)
						err = fakeK8sClient.Create(context.TODO(), &promiseRelease)
						Expect(err).NotTo(HaveOccurred())
						result, err = t.reconcileUntilCompletion(reconciler, &promiseRelease)
					})

					It("errors", func() {
						Expect(result).To(Equal(ctrl.Result{}))
						Expect(err).To(MatchError(ContainSubstring("version label (kratix.io/promise-version) not found on promise; refusing to install")))
					})

					It("updates the status", func() {
						err := fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
						Expect(err).NotTo(HaveOccurred())
						Expect(promiseRelease.Status.Status).To(Equal("Error installing"))
						Expect(promiseRelease.Status.Conditions).To(HaveLen(1))
						Expect(promiseRelease.Status.Conditions[0].Type).To(Equal("Installed"))
						Expect(promiseRelease.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))
						Expect(promiseRelease.Status.Conditions[0].Message).To(Equal("Version label not found on Promise"))
						Expect(promiseRelease.Status.Conditions[0].Reason).To(Equal("VersionLabelNotFound"))
					})
				})

				When("the Promise version doesn't match the Promise Release version", func() {
					BeforeEach(func() {
						unversionedPromise := promiseFromFile(promisePath)
						unversionedPromise.Labels["kratix.io/promise-version"] = "v2.2.0"
						fakeFetcher.FromURLReturns(unversionedPromise, nil)
						err = fakeK8sClient.Create(context.TODO(), &promiseRelease)
						Expect(err).NotTo(HaveOccurred())
						result, err = t.reconcileUntilCompletion(reconciler, &promiseRelease)
					})

					It("errors", func() {
						Expect(result).To(Equal(ctrl.Result{}))
						Expect(err).To(MatchError(ContainSubstring("version label on promise (v2.2.0) does not match version on promise release (v1.1.0); refusing to install")))
					})

					It("updates the status", func() {
						err := fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
						Expect(err).NotTo(HaveOccurred())
						Expect(promiseRelease.Status.Status).To(Equal("Error installing"))
						Expect(promiseRelease.Status.Conditions).To(HaveLen(1))
						Expect(promiseRelease.Status.Conditions[0].Type).To(Equal("Installed"))
						Expect(promiseRelease.Status.Conditions[0].Status).To(Equal(metav1.ConditionFalse))
						Expect(promiseRelease.Status.Conditions[0].Message).To(Equal("Version labels do not match, found: v2.2.0, expected: v1.1.0"))
						Expect(promiseRelease.Status.Conditions[0].Reason).To(Equal("VersionNotMatching"))
					})
				})

				When("the PromiseRelease does not specify a version", func() {
					BeforeEach(func() {
						promiseRelease.Spec.Version = ""
						err = fakeK8sClient.Create(context.TODO(), &promiseRelease)
						Expect(err).NotTo(HaveOccurred())

						_, err = t.reconcileUntilCompletion(reconciler, &promiseRelease)
						Expect(err).NotTo(HaveOccurred())
					})

					It("installs the promise from the URL", func() {
						expectedPromise := promiseFromFile(promisePath)
						Expect(promise.Spec).To(Equal(expectedPromise.Spec))
					})

					It("updates the PromiseRelease with the Promise version", func() {
						err := fakeK8sClient.Get(context.Background(), promiseReleaseNamespacedName, &promiseRelease)
						Expect(err).NotTo(HaveOccurred())
						Expect(promiseRelease.Spec.Version).To(Equal("v1.1.0"))
					})

					It("sets the promise release status to installed", func() {
						err := fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
						Expect(err).NotTo(HaveOccurred())
						Expect(promiseRelease.Status.Status).To(Equal("Installed"))
					})
				})

				When("the Promise has a defined version", func() {
					BeforeEach(func() {
						err := fakeK8sClient.Create(context.TODO(), &promiseRelease)
						Expect(err).NotTo(HaveOccurred())

						_, err = t.reconcileUntilCompletion(reconciler, &promiseRelease)
						Expect(err).NotTo(HaveOccurred())

						promise = fetchPromise(promiseReleaseNamespacedName)
						err = fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
						Expect(err).NotTo(HaveOccurred())
					})

					It("installs the promise from the URL", func() {
						expectedPromise := promiseFromFile(promisePath)
						Expect(promise.Spec).To(Equal(expectedPromise.Spec))
					})

					It("sets the labels on the promise from the URL definition, including the promise release labels", func() {
						Expect(promise.Labels).To(Equal(map[string]string{
							"new-promise-label":              "value",
							"clashing-label":                 "new-promise-value",
							"kratix.io/promise-version":      "v1.1.0",
							"kratix.io/promise-release-name": "redis",
						}))
					})

					It("sets the annotations on the promise from the URL definition", func() {
						Expect(promise.Annotations).To(Equal(map[string]string{
							"new-promise-annotation": "value",
							"clashing-annotation":    "new-promise-value",
						}))
					})

					It("sets the promise as a dependent of the promise release", func() {
						tru := true
						Expect(promise.GetOwnerReferences()).To(ContainElement(metav1.OwnerReference{
							APIVersion:         "platform.kratix.io/v1alpha1",
							Kind:               "PromiseRelease",
							Name:               "redis",
							Controller:         &tru,
							BlockOwnerDeletion: &tru,
						}))
					})

					It("sets the promise release status to installed", func() {
						err := fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
						Expect(err).NotTo(HaveOccurred())
						Expect(promiseRelease.Status.Status).To(Equal("Installed"))
					})

					It("adds a finalizer for the promise to the promise release", func() {
						Expect(promiseRelease.GetFinalizers()).To(ConsistOf("kratix.io/promise-cleanup"))
					})
				})

				When("the Promise manifest is invalid", func() {
					var (
						promiseNamespacedName types.NamespacedName
						eventRecorder         *record.FakeRecorder
					)

					BeforeEach(func() {
						promiseNamespacedName = types.NamespacedName{
							Name: promise.Name,
							// Note: Promise is cluster-scoped
						}

						fakeK8sClient = &mockPromiseCtrlRuntimeClient{
							Client:  fakeK8sClient,
							promise: promise.Name,
							mockErr: errors.NewInvalid( // Create mock error for invalid promise
								promise.GroupVersionKind().GroupKind(),
								promise.Name,
								field.ErrorList{},
							),
						}

						eventRecorder = record.NewFakeRecorder(1024)

						reconciler = &controllers.PromiseReleaseReconciler{
							Client:         fakeK8sClient,
							Scheme:         scheme.Scheme,
							PromiseFetcher: fakeFetcher,
							Log:            ctrl.Log.WithName("controllers").WithName("PromiseRelease"),
							EventRecorder:  eventRecorder,
						}

						err := fakeK8sClient.Create(context.TODO(), &promiseRelease)
						Expect(err).NotTo(HaveOccurred())
					})

					It("fails the PromiseRelease's reconciliation", func() {
						_, err := t.reconcileUntilCompletion(reconciler, &promiseRelease)
						Expect(err).To(HaveOccurred())
						Expect(eventRecorder.Events).To(Receive(ContainSubstring(
							fmt.Sprintf("Promise.platform.kratix.io %q is invalid", promise.Name)),
						))
					})

					It("doesn't create the Promise", func() {
						err := fakeK8sClient.Get(context.TODO(), promiseNamespacedName, promise)
						Expect(err).To(HaveOccurred())
					})
				})
			})
		})
	})

	When("the PromiseRelease is deleted", func() {
		var err error

		BeforeEach(func() {
			Expect(fakeK8sClient.Create(context.TODO(), &promiseRelease)).To(Succeed())

			_, err = t.reconcileUntilCompletion(reconciler, &promiseRelease)
			Expect(err).NotTo(HaveOccurred())

			promise := fetchPromise(promiseReleaseNamespacedName)
			promise.SetFinalizers([]string{"test-finalizer"})
			Expect(fakeK8sClient.Update(context.TODO(), promise)).To(Succeed())

			Expect(fakeK8sClient.Delete(context.TODO(), &promiseRelease)).To(Succeed())

			_, err = t.reconcileUntilCompletion(reconciler, &promiseRelease)
		})

		It("triggers the removal of the promise", func() {
			promise := fetchPromise(promiseReleaseNamespacedName)
			Expect(promise.DeletionTimestamp).NotTo(BeNil())
		})

		It("keeps the promise release finalizer", func() {
			err := fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
			Expect(err).NotTo(HaveOccurred())
			Expect(promiseRelease.GetFinalizers()).To(ConsistOf("kratix.io/promise-cleanup"))
		})

		It("requeues forever", func() {
			Expect(err).To(MatchError("reconcile loop detected"))
		})

		When("the promise is gone", func() {
			It("removes the promise release on the next reconciliation", func() {
				//Remove finalizers from promise so it can disappear
				deletePromise(promiseReleaseNamespacedName)

				_, err := t.reconcileUntilCompletion(reconciler, &promiseRelease)
				Expect(err).NotTo(HaveOccurred())

				err = fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})
		})
	})

	When("the Promise is deleted outside of a PromiseRelease", func() {
		BeforeEach(func() {
			Expect(fakeK8sClient.Create(context.Background(), &promiseRelease)).To(Succeed())

			_, err := t.reconcileUntilCompletion(reconciler, &promiseRelease)
			Expect(err).NotTo(HaveOccurred())

			promise = fetchPromise(promiseReleaseNamespacedName)
			Expect(promise).NotTo(BeNil())

			deletePromise(promiseReleaseNamespacedName)
		})

		It("reinstalls the promise in the next reconciliation", func() {
			_, err := t.reconcileUntilCompletion(reconciler, &promiseRelease)
			Expect(err).NotTo(HaveOccurred())

			promise = fetchPromise(promiseReleaseNamespacedName)
			Expect(promise).NotTo(BeNil())
			Expect(promise.DeletionTimestamp).To(BeNil())
		})
	})

	When("the PromiseRelease is updated", func() {
		BeforeEach(func() {
			Expect(fakeK8sClient.Create(context.TODO(), &promiseRelease)).To(Succeed())

			_, err := t.reconcileUntilCompletion(reconciler, &promiseRelease)
			Expect(err).NotTo(HaveOccurred())

			fakeFetcher.FromURLReturns(promiseFromFile(updatedPromisePath), nil)
			Expect(fakeK8sClient.Get(context.Background(), promiseReleaseNamespacedName, &promiseRelease)).To(Succeed())

			promiseRelease.Spec.Version = "v1.2.0"
			Expect(fakeK8sClient.Update(context.Background(), &promiseRelease)).To(Succeed())

			_, err = t.reconcileUntilCompletion(reconciler, &promiseRelease)
			Expect(err).NotTo(HaveOccurred())

			promise = fetchPromise(promiseReleaseNamespacedName)
		})

		Context("sourceRef URL", func() {
			It("downloads the latest version from the url", func() {
				Expect(fakeFetcher.FromURLCallCount()).To(Equal(2))
				Expect(fakeFetcher.FromURLArgsForCall(0)).To(Equal("example.com"))
			})
		})

		It("updates the promise labels", func() {
			Expect(promise.Labels).To(Equal(map[string]string{
				"new-promise-label":              "value",
				"new-promise-v2-label":           "value",
				"clashing-label":                 "new-promise-v2-value",
				"kratix.io/promise-version":      "v1.2.0",
				"kratix.io/promise-release-name": "redis",
			}))
		})

		It("updates the promise annotations", func() {
			Expect(promise.Annotations).To(Equal(map[string]string{
				"new-promise-annotation":    "value",
				"new-promise-v2-annotation": "value",
				"clashing-annotation":       "new-promise-v2-value",
			}))
		})

		It("updates the promise release status", func() {
			err := fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
			Expect(err).NotTo(HaveOccurred())
			Expect(promiseRelease.Status.Status).To(Equal("Installed"))
		})

		It("retains the promise release finalizer", func() {
			Expect(promiseRelease.GetFinalizers()).To(ConsistOf("kratix.io/promise-cleanup"))
		})

		It("updates the promise spec", func() {
			promise := fetchPromise(promiseReleaseNamespacedName)
			expectedPromise := promiseFromFile(updatedPromisePath)
			Expect(promise.Spec).To(Equal(expectedPromise.Spec))
		})
	})
})

type mockPromiseCtrlRuntimeClient struct {
	client.Client
	promise string              // Name of the Promise
	mockErr *errors.StatusError // Error returned by this client when expected conditions meet
}

// Create mocks the client.Client's Create method in mockPromiseCtrlRuntimeClient
//
// If the object being created is of kind "Promise", and the name matches, it returns a predefined error. Else, it
// invokes the client.Client 's Create method.
func (m *mockPromiseCtrlRuntimeClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	// Parse the object as "Promise"
	if _, ok := obj.(*v1alpha1.Promise); !ok {
		// When the object is not a Promise, invoke the client.Client's Create method
		return m.Client.Create(ctx, obj)
	}

	// Check if the object's name matches the expected name
	if obj.GetName() == m.promise {
		return m.mockErr
	}

	// When the object doesn't match the required Promise, invoke the client.Client's Create method
	return m.Client.Create(ctx, obj)
}
