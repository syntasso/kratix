package controllers_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
	"github.com/syntasso/kratix/controllers/controllersfakes"
)

var (
	quickTimeout  = time.Second * 5
	quickInterval = time.Millisecond

	ctx         context.Context
	reconciler  *controllers.PromiseReconciler
	promise     *v1alpha1.Promise
	promiseName types.NamespacedName

	promiseGroup        = "marketplace.kratix.io"
	promiseResourceName = "redis"
	expectedCRDName     = promiseResourceName + "." + promiseGroup
	promiseCommonLabels = map[string]string{
		"kratix-promise-id": "redis",
	}
	managerRestarted bool
)

const promiseKind = "Promise"

var _ = Describe("PromiseController", func() {

	BeforeEach(func() {
		ctx = context.Background()
		managerRestarted = false
		reconciler = &controllers.PromiseReconciler{
			Client:              fakeK8sClient,
			ApiextensionsClient: fakeApiExtensionsClient,
			Log:                 ctrl.Log.WithName("controllers").WithName("Promise"),
			Manager:             &controllersfakes.FakeManager{},
			RestartManager: func() {
				managerRestarted = true
			},
		}

		promise = promiseFromFile(promisePath)
		promiseName = types.NamespacedName{
			Name:      promise.GetName(),
			Namespace: promise.GetNamespace(),
		}

		Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
		Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
		promise.UID = types.UID("1234abcd")
		Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())
	})

	Describe("Promise Reconciliation", func() {
		When("the promise only contains static dependencies", func() {
			When("the promise is being created", func() {
				It("re-reconciles until completetion", func() {
					By("creating the CRD", func() {
						result, err := reconcileUntilCompletion(reconciler, promise, &opts{
							funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
						})

						Expect(err).NotTo(HaveOccurred())
						Expect(result).To(Equal(ctrl.Result{}))

						crd, err := fakeApiExtensionsClient.CustomResourceDefinitions().Get(ctx, expectedCRDName, metav1.GetOptions{})

						Expect(err).NotTo(HaveOccurred())
						Expect(crd.Spec.Names.Plural + "." + crd.Spec.Group).To(Equal(expectedCRDName))
					})

					By("updating the status with the CRD values", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Status.APIVersion).To(Equal("marketplace.kratix.io/v1alpha1"))
						Expect(promise.Status.Kind).To(Equal("redis"))
					})

					By("setting the finalizer for the CRD", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Finalizers).To(ContainElement("kratix.io/api-crd-cleanup"))
					})

					By("creating the resources for the dynamic controller", func() {
						controllerResourceName := types.NamespacedName{
							Name: promise.GetControllerResourceName(),
						}
						clusterrole := &rbacv1.ClusterRole{}
						Expect(fakeK8sClient.Get(ctx, controllerResourceName, clusterrole)).To(Succeed())

						Expect(clusterrole.Rules).To(ConsistOf(
							rbacv1.PolicyRule{
								Verbs:     []string{rbacv1.VerbAll},
								APIGroups: []string{promiseGroup},
								Resources: []string{promiseResourceName},
							},
							rbacv1.PolicyRule{
								Verbs:     []string{"update"},
								APIGroups: []string{promiseGroup},
								Resources: []string{promiseResourceName + "/finalizers"},
							},
							rbacv1.PolicyRule{
								Verbs:     []string{"get", "update", "patch"},
								APIGroups: []string{promiseGroup},
								Resources: []string{promiseResourceName + "/status"},
							},
						))
						Expect(clusterrole.GetLabels()).To(Equal(promiseCommonLabels))

						binding := &rbacv1.ClusterRoleBinding{}
						Expect(fakeK8sClient.Get(ctx, controllerResourceName, binding)).To(Succeed(), "Expected controller binding to exist")
						Expect(binding.RoleRef.Name).To(Equal(promise.GetControllerResourceName()))
						Expect(binding.Subjects).To(HaveLen(1))
						Expect(binding.Subjects[0]).To(Equal(rbacv1.Subject{
							Kind:      "ServiceAccount",
							Namespace: v1alpha1.KratixSystemNamespace,
							Name:      "kratix-platform-controller-manager",
						}))
						Expect(binding.GetLabels()).To(Equal(promiseCommonLabels))
					})

					By("setting the finalizer for the dynamic controller resources", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Finalizers).To(ContainElement("kratix.io/dynamic-controller-dependant-resources-cleanup"))
					})

					By("setting the finalizer for dependencies", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Finalizers).To(ContainElement("kratix.io/dependencies-cleanup"))
					})

					By("setting the finalizer for resource requests", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Finalizers).To(ContainElement("kratix.io/resource-request-cleanup"))
					})

					By("creating a Work resource for the dependencies", func() {
						workNamespacedName := types.NamespacedName{
							Name:      promise.GetName(),
							Namespace: v1alpha1.KratixSystemNamespace,
						}
						Expect(fakeK8sClient.Get(ctx, workNamespacedName, &v1alpha1.Work{})).To(Succeed())
					})

					By("updating the status.obeservedGeneration", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Status.ObservedGeneration).To(Equal(promise.Generation))
					})
				})
			})

			When("the promise is being deleted", func() {
				BeforeEach(func() {
					result, err := reconcileUntilCompletion(reconciler, promise, &opts{
						funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
					})

					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
				})

				It("deletes all associated resources", func() {
					_, err := fakeApiExtensionsClient.CustomResourceDefinitions().Get(ctx, expectedCRDName, metav1.GetOptions{})
					Expect(err).NotTo(HaveOccurred())
					controllerResourceName := types.NamespacedName{
						Name: promise.GetControllerResourceName(),
					}
					clusterrole := &rbacv1.ClusterRole{}
					Expect(fakeK8sClient.Get(ctx, controllerResourceName, clusterrole)).To(Succeed())
					binding := &rbacv1.ClusterRoleBinding{}
					Expect(fakeK8sClient.Get(ctx, controllerResourceName, binding)).To(Succeed(), "Expected controller binding to exist")
					workNamespacedName := types.NamespacedName{
						Name:      promise.GetName(),
						Namespace: v1alpha1.KratixSystemNamespace,
					}
					Expect(fakeK8sClient.Get(ctx, workNamespacedName, &v1alpha1.Work{})).To(Succeed())

					Expect(fakeK8sClient.Delete(ctx, promise)).To(Succeed())
					result, err := reconcileUntilCompletion(reconciler, promise, &opts{errorBudget: 5})
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
					Expect(managerRestarted).To(BeTrue())

					_, err = fakeApiExtensionsClient.CustomResourceDefinitions().Get(ctx, expectedCRDName, metav1.GetOptions{})
					Expect(err).To(MatchError(ContainSubstring("not found")))
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(MatchError(ContainSubstring("not found")))
					Expect(fakeK8sClient.Get(ctx, controllerResourceName, clusterrole)).To(MatchError(ContainSubstring("not found")))
					Expect(fakeK8sClient.Get(ctx, controllerResourceName, binding)).To(MatchError(ContainSubstring("not found")))
					Expect(fakeK8sClient.Get(ctx, workNamespacedName, &v1alpha1.Work{})).To(MatchError(ContainSubstring("not found")))
				})
			})
		})
	})
})

func autoMarkCRDAsEstablished(obj client.Object) error {
	crd, err := fakeApiExtensionsClient.CustomResourceDefinitions().Get(context.Background(), expectedCRDName, metav1.GetOptions{})

	if err != nil {
		return err
	}
	crd.Status.Conditions = append(crd.Status.Conditions, apiextensionsv1.CustomResourceDefinitionCondition{
		Type:               apiextensionsv1.Established,
		Status:             apiextensionsv1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	})
	_, err = fakeApiExtensionsClient.CustomResourceDefinitions().Update(context.Background(), crd, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return nil
}
