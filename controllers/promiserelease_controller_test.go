package controllers_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
	"github.com/syntasso/kratix/controllers/controllersfakes"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("PromiseReleaseController", func() {
	var (
		promiseRelease               v1alpha1.PromiseRelease
		promiseReleaseNamespacedName types.NamespacedName
		reconciler                   *controllers.PromiseReleaseReconciler
		fakeFetcher                  *controllersfakes.FakePromiseFetcher
		promise                      *v1alpha1.Promise
	)

	BeforeEach(func() {
		fakeFetcher = &controllersfakes.FakePromiseFetcher{}
		fakeFetcher.FromURLReturns(promiseFromFile(promisePath), nil)

		reconciler = &controllers.PromiseReleaseReconciler{
			Client:         fakeK8sClient,
			Scheme:         scheme.Scheme,
			PromiseFetcher: fakeFetcher,
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
				Expect(err).ToNot(HaveOccurred())
				// Install the PromiseRelease
				err = fakeK8sClient.Create(context.TODO(), &promiseRelease)
				Expect(err).ToNot(HaveOccurred())

				_, err = reconcile(reconciler, &promiseRelease)
				Expect(err).ToNot(HaveOccurred(), "reconciliation failed; expected it to work")

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
				Expect(err).ToNot(HaveOccurred())
				Expect(promiseRelease.Status.Installed).To(BeTrue())
			})
		})

		When("the sourceRef type is unknown", func() {
			It("errors", func() {
				promiseRelease.Spec.SourceRef.Type = "unknown"

				err := fakeK8sClient.Create(context.TODO(), &promiseRelease)
				Expect(err).ToNot(HaveOccurred())

				_, err = reconcile(reconciler, &promiseRelease)

				Expect(err).To(MatchError("unknown sourceRef type: unknown"))
			})
		})

		When("the sourceRef type is http", func() {
			When("the url fetcher errors", func() {
				BeforeEach(func() {
					fakeFetcher.FromURLReturns(nil, fmt.Errorf("can't do mate"))
					err := fakeK8sClient.Create(context.TODO(), &promiseRelease)
					Expect(err).ToNot(HaveOccurred())
				})

				It("installs the promise from the URL", func() {
					_, err := reconcile(reconciler, &promiseRelease)
					Expect(err).To(MatchError("failed to fetch promise from url: can't do mate"))
				})
			})

			When("the url fetcher succeeds", func() {
				When("the Promise has no defined version", func() {
					BeforeEach(func() {
						unversionedPromise := promiseFromFile(promisePath)
						unversionedPromise.Labels = nil
						fakeFetcher.FromURLReturns(unversionedPromise, nil)
						err := fakeK8sClient.Create(context.TODO(), &promiseRelease)
						Expect(err).ToNot(HaveOccurred())
					})

					It("errors", func() {
						result, err := reconcile(reconciler, &promiseRelease)
						Expect(result).To(Equal(ctrl.Result{}))
						Expect(err).To(MatchError(ContainSubstring("version label (kratix.io/promise-version) not found on promise; refusing to install")))
					})
				})

				When("the Promise version doesn't match the Promise Release version", func() {
					BeforeEach(func() {
						unversionedPromise := promiseFromFile(promisePath)
						unversionedPromise.Labels["kratix.io/promise-version"] = "v2.2.0"
						fakeFetcher.FromURLReturns(unversionedPromise, nil)
						err := fakeK8sClient.Create(context.TODO(), &promiseRelease)
						Expect(err).ToNot(HaveOccurred())
					})

					It("errors", func() {
						result, err := reconcile(reconciler, &promiseRelease)
						Expect(result).To(Equal(ctrl.Result{}))
						Expect(err).To(MatchError(ContainSubstring("version label on promise (v2.2.0) does not match version on promise release (v1.1.0); refusing to install")))
					})
				})

				When("the PromiseRelease does not specify a version", func() {
					BeforeEach(func() {
						promiseRelease.Spec.Version = ""
						err := fakeK8sClient.Create(context.TODO(), &promiseRelease)
						Expect(err).ToNot(HaveOccurred())

						_, err = reconcile(reconciler, &promiseRelease)
						Expect(err).ToNot(HaveOccurred())
					})

					It("installs the promise from the URL", func() {
						expectedPromise := promiseFromFile(promisePath)
						Expect(promise.Spec).To(Equal(expectedPromise.Spec))
					})

					It("updates the PromiseRelease with the Promise version", func() {
						err := fakeK8sClient.Get(context.Background(), promiseReleaseNamespacedName, &promiseRelease)
						Expect(err).ToNot(HaveOccurred())
						Expect(promiseRelease.Spec.Version).To(Equal("v1.1.0"))
					})
				})

				When("the Promise has a defined version", func() {
					BeforeEach(func() {
						err := fakeK8sClient.Create(context.TODO(), &promiseRelease)
						Expect(err).ToNot(HaveOccurred())

						_, err = reconcile(reconciler, &promiseRelease)
						Expect(err).ToNot(HaveOccurred())

						promise = fetchPromise(promiseReleaseNamespacedName)
						err = fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
						Expect(err).ToNot(HaveOccurred())
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

					It("updates the promise release status to installed", func() {
						Expect(promiseRelease.Status.Installed).To(BeTrue())
					})

					It("adds a finalizer for the promise to the promise release", func() {
						Expect(promiseRelease.GetFinalizers()).To(ConsistOf("kratix.io/promise-cleanup"))
					})
				})
			})
		})
	})

	When("the PromiseRelease is deleted", func() {
		var result ctrl.Result

		BeforeEach(func() {
			Expect(fakeK8sClient.Create(context.TODO(), &promiseRelease)).To(Succeed())

			_, err := reconcile(reconciler, &promiseRelease)
			Expect(err).ToNot(HaveOccurred())

			promise := fetchPromise(promiseReleaseNamespacedName)
			promise.SetFinalizers([]string{"test-finalizer"})
			Expect(fakeK8sClient.Update(context.TODO(), promise)).To(Succeed())

			Expect(fakeK8sClient.Delete(context.TODO(), &promiseRelease)).To(Succeed())

			result, err = reconcile(reconciler, &promiseRelease)
			Expect(err).ToNot(HaveOccurred())
		})

		It("triggers the removal of the promise", func() {
			promise := fetchPromise(promiseReleaseNamespacedName)
			Expect(promise.DeletionTimestamp).ToNot(BeNil())
		})

		It("keeps the promise release finalizer", func() {
			err := fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
			Expect(err).ToNot(HaveOccurred())
			Expect(promiseRelease.GetFinalizers()).To(ConsistOf("kratix.io/promise-cleanup"))
		})

		It("requeues", func() {
			Expect(result.RequeueAfter > 0).To(BeTrue())
		})

		When("the promise is gone", func() {
			It("removes the promise release on the next reconciliation", func() {
				deletePromise(promiseReleaseNamespacedName)

				_, err := reconcile(reconciler, &promiseRelease)
				Expect(err).ToNot(HaveOccurred())

				err = fakeK8sClient.Get(context.TODO(), promiseReleaseNamespacedName, &promiseRelease)
				Expect(errors.IsNotFound(err)).To(BeTrue())
			})
		})
	})

	When("the Promise is deleted outside of a PromiseRelease", func() {
		BeforeEach(func() {
			Expect(fakeK8sClient.Create(context.Background(), &promiseRelease)).To(Succeed())

			_, err := reconcile(reconciler, &promiseRelease)
			Expect(err).ToNot(HaveOccurred())

			promise = fetchPromise(promiseReleaseNamespacedName)
			Expect(promise).ToNot(BeNil())

			deletePromise(promiseReleaseNamespacedName)
		})

		It("reinstalls the promise in the next reconciliation", func() {
			_, err := reconcile(reconciler, &promiseRelease)
			Expect(err).ToNot(HaveOccurred())

			promise = fetchPromise(promiseReleaseNamespacedName)
			Expect(promise).ToNot(BeNil())
			Expect(promise.DeletionTimestamp).To(BeNil())
		})
	})

	When("the PromiseRelease is updated", func() {
		BeforeEach(func() {
			Expect(fakeK8sClient.Create(context.TODO(), &promiseRelease)).To(Succeed())

			_, err := reconcile(reconciler, &promiseRelease)
			Expect(err).ToNot(HaveOccurred())

			fakeFetcher.FromURLReturns(promiseFromFile(updatedPromisePath), nil)
			Expect(fakeK8sClient.Get(context.Background(), promiseReleaseNamespacedName, &promiseRelease)).To(Succeed())

			promiseRelease.Spec.Version = "v1.2.0"
			Expect(fakeK8sClient.Update(context.Background(), &promiseRelease)).To(Succeed())

			_, err = reconcile(reconciler, &promiseRelease)
			Expect(err).ToNot(HaveOccurred())

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
			Expect(err).ToNot(HaveOccurred())
			Expect(promiseRelease.Status.Installed).To(BeTrue())
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

	When("The PromiseRelease version is not specified", func() {})
})
