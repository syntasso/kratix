package controllers_test

import (
	"context"
	"os"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
)

var _ = Describe("DynamicResourceRequestController", func() {
	var (
		reconciler          *controllers.DynamicResourceRequestController
		resReq              *unstructured.Unstructured
		resReqNameNamespace types.NamespacedName
	)

	BeforeEach(func() {
		ctx = context.Background()
		promise = promiseFromFile(promisePath)
		promiseName = types.NamespacedName{
			Name:      promise.GetName(),
			Namespace: promise.GetNamespace(),
		}

		rrCRD, err := promise.GetAPIAsCRD()
		Expect(err).ToNot(HaveOccurred())
		rrGVK := schema.GroupVersionKind{
			Group:   rrCRD.Spec.Group,
			Version: rrCRD.Spec.Versions[0].Name,
			Kind:    rrCRD.Spec.Names.Kind,
		}

		Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
		Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
		promise.UID = "1234abcd"
		Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())
		l = ctrl.Log.WithName("controllers").WithName("dynamic")

		enabled := true
		reconciler = &controllers.DynamicResourceRequestController{
			CanCreateResources:          &enabled,
			Client:                      fakeK8sClient,
			Scheme:                      scheme.Scheme,
			GVK:                         &rrGVK,
			CRD:                         rrCRD,
			PromiseIdentifier:           promise.GetName(),
			PromiseDestinationSelectors: promise.Spec.DestinationSelectors,
			Log:                         l,
			UID:                         "1234abcd",
			Enabled:                     &enabled,
		}

		yamlFile, err := os.ReadFile(resourceRequestPath)
		Expect(err).ToNot(HaveOccurred())

		resReq = &unstructured.Unstructured{}
		Expect(yaml.Unmarshal(yamlFile, resReq)).To(Succeed())
		Expect(fakeK8sClient.Create(ctx, resReq)).To(Succeed())
		resReqNameNamespace = types.NamespacedName{
			Name:      resReq.GetName(),
			Namespace: resReq.GetNamespace(),
		}
		Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
		resReq.SetUID("1234abcd")
		resReq.SetGeneration(1)
		Expect(fakeK8sClient.Update(ctx, resReq)).To(Succeed())
		Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())

		promiseCommonLabels = map[string]string{
			"kratix-promise-resource-request-id": promise.GetName() + "-" + resReq.GetName(),
			"kratix.io/resource-name":            resReq.GetName(),
			"kratix.io/promise-name":             promise.GetName(),
			"kratix.io/work-type":                "resource",
		}

	})

	When("resource is being created", func() {
		It("re-reconciles until completion", func() {
			result, err := t.reconcileUntilCompletion(reconciler, resReq)
			Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())

			resourceLabels := map[string]string{
				"kratix.io/promise-name": promise.GetName(),
			}

			resources := reconcileConfigureOptsArg.Resources[0].GetObjects()
			By("creating a service account for pipeline", func() {
				Expect(resources[0]).To(BeAssignableToTypeOf(&v1.ServiceAccount{}))
				sa := resources[0].(*v1.ServiceAccount)
				Expect(sa.GetLabels()).To(Equal(resourceLabels))
			})

			By("creates a role for the pipeline service account", func() {
				Expect(resources[2]).To(BeAssignableToTypeOf(&rbacv1.Role{}))
				role := resources[2].(*rbacv1.Role)
				Expect(role.GetLabels()).To(Equal(resourceLabels))
				Expect(role.Rules).To(ConsistOf(
					rbacv1.PolicyRule{
						Verbs:     []string{"get", "list", "update", "create", "patch"},
						APIGroups: []string{promiseGroup},
						Resources: []string{"redis", "redis/status"},
					},
					rbacv1.PolicyRule{
						Verbs:     []string{"*"},
						APIGroups: []string{"platform.kratix.io"},
						Resources: []string{"works"},
					},
				))
				Expect(role.GetLabels()).To(Equal(resourceLabels))
			})

			By("associates the new role with the new service account", func() {
				Expect(resources[3]).To(BeAssignableToTypeOf(&rbacv1.RoleBinding{}))
				binding := resources[3].(*rbacv1.RoleBinding)
				Expect(binding.RoleRef.Name).To(Equal("redis-resource-configure-first-pipeline"))
				Expect(binding.Subjects).To(HaveLen(1))
				Expect(binding.Subjects[0]).To(Equal(rbacv1.Subject{
					Kind:      "ServiceAccount",
					Namespace: resReq.GetNamespace(),
					Name:      "redis-resource-configure-first-pipeline",
				}))
				Expect(binding.GetLabels()).To(Equal(resourceLabels))
			})

			By("creates a config map with the promise scheduling in it", func() {
				Expect(resources[1]).To(BeAssignableToTypeOf(&v1.ConfigMap{}))
				configMap := resources[1].(*v1.ConfigMap)
				Expect(configMap.GetName()).To(Equal("destination-selectors-" + promise.GetName()))
				Expect(configMap.GetNamespace()).To(Equal("default"))
				Expect(configMap.GetLabels()).To(Equal(resourceLabels))
				Expect(configMap.Data).To(HaveKey("destinationSelectors"))
				space := regexp.MustCompile(`\s+`)
				destinationSelectors := space.ReplaceAllString(configMap.Data["destinationSelectors"], " ")
				Expect(strings.TrimSpace(destinationSelectors)).To(Equal(`- matchlabels: environment: dev source: promise`))
			})

			By("not requeuing, since the controller is watching the job", func() {
				Expect(err).To(BeNil())
				Expect(result).To(Equal(ctrl.Result{}))
			})

			By("finishing the creation once the job is finished", func() {
				setReconcileConfigureWorkflowToReturnFinished()
				result, err := t.reconcileUntilCompletion(reconciler, resReq)

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))
			})

			By("setting the finalizers on the resource", func() {
				Expect(resReq.GetFinalizers()).To(ConsistOf(
					"kratix.io/work-cleanup",
					"kratix.io/workflows-cleanup",
					"kratix.io/delete-workflows",
				))
			})

			By("setting the labels on the resource", func() {
				Expect(resReq.GetLabels()).To(Equal(
					map[string]string{
						"kratix.io/promise-name": promise.GetName(),
						"non-kratix-label":       "true",
					},
				))
			})

			By("setting the observedGeneration in the resource status", func() {
				Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
				status := resReq.Object["status"]
				Expect(status).NotTo(BeNil())
				statusMap := status.(map[string]interface{})
				Expect(statusMap["observedGeneration"]).To(Equal(int64(1)))
			})
		})

		When("CanCreateResources is set to false", func() {
			BeforeEach(func() {
				canCreate := false
				reconciler.CanCreateResources = &canCreate
			})

			It("sets the status of resource request to pending", func() {
				_, err := t.reconcileUntilCompletion(reconciler, resReq)
				Expect(err).To(MatchError("reconcile loop detected"))
				Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
				status := resReq.Object["status"]
				statusMap := status.(map[string]interface{})
				Expect(statusMap["message"].(string)).To(Equal("Pending"))
			})
		})
	})

	When("resource is being deleted", func() {
		BeforeEach(func() {
			setReconcileConfigureWorkflowToReturnFinished()
			result, err := t.reconcileUntilCompletion(reconciler, resReq)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
			Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
			Expect(resReq.GetFinalizers()).To(ConsistOf(
				"kratix.io/work-cleanup",
				"kratix.io/workflows-cleanup",
				"kratix.io/delete-workflows",
			))
		})

		It("re-reconciles until completion", func() {
			Expect(fakeK8sClient.Delete(ctx, resReq)).To(Succeed())
			_, err := t.reconcileUntilCompletion(reconciler, resReq)

			By("requeuing forever until delete jobs finishes", func() {
				Expect(err).To(MatchError("reconcile loop detected"))
			})

			setReconcileDeleteWorkflowToReturnFinished(resReq)
			result, err := t.reconcileUntilCompletion(reconciler, resReq)
			Expect(result).To(Equal(ctrl.Result{}))
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(MatchError(ContainSubstring("not found")))

			jobs := &batchv1.JobList{}
			works := &v1alpha1.WorkList{}
			Expect(fakeK8sClient.List(ctx, works)).To(Succeed())
			Expect(fakeK8sClient.List(ctx, jobs)).To(Succeed())
			Expect(works.Items).To(HaveLen(0))
			Expect(jobs.Items).To(HaveLen(0))
		})
	})
})
