package controllers_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
)

var _ = Describe("PromiseController", func() {
	var (
		ctx         context.Context
		reconciler  controllers.PromiseReconciler
		promise     *v1alpha1.Promise
		promiseName types.NamespacedName
	)
	const promiseKind = "Promise"

	BeforeEach(func() {
		ctx = context.Background()
		reconciler = controllers.PromiseReconciler{
			Client:              fakeK8sClient,
			ApiextensionsClient: apiextensionClient,
		}

		promise = promiseFromFile(promisePath)
		promiseName = types.NamespacedName{
			Name:      promise.GetName(),
			Namespace: promise.GetNamespace(),
		}
	})

	Describe("Promise Reconciliation", func() {
		It("succeeds when the promise doesn't exist", func() {
			result, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: "promise-not-found",
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
		})

		When("the promise is being deleted", func() {
			BeforeEach(func() {
				// TODO: create the promise, reconcile once, delete the promise
			})
		})

		When("the promise contains a version label", func() {
			BeforeEach(func() {
				promise.Labels["kratix.io/promise-version"] = "v1.2.3"
				Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())

				result, err := reconcile(&reconciler, promise, &opts{singleReconcile: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))
			})

			It("it gets its status updated with the version", func() {
				promise := fetchPromise(promiseName)
				Expect(promise.Status.Version).To(Equal("v1.2.3"))
			})
		})
	})
})
