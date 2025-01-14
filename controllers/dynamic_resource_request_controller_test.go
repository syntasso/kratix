package controllers_test

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/controllers"
	"github.com/syntasso/kratix/lib/resourceutil"
)

var _ = Describe("DynamicResourceRequestController", func() {
	var (
		reconciler          *controllers.DynamicResourceRequestController
		resReq              *unstructured.Unstructured
		resReqNameNamespace types.NamespacedName
		startTime           time.Time
	)

	BeforeEach(func() {
		startTime = time.Now().Add(-time.Minute)
		ctx = context.Background()
		promise = createPromise(promisePath)

		rrGVK, rrCRD, err := promise.GetAPI()
		Expect(err).ToNot(HaveOccurred())

		l = ctrl.Log.WithName("controllers").WithName("dynamic")

		reconciler = &controllers.DynamicResourceRequestController{
			CanCreateResources:          ptr.To(true),
			Enabled:                     ptr.To(true),
			Client:                      fakeK8sClient,
			Scheme:                      scheme.Scheme,
			GVK:                         rrGVK,
			CRD:                         rrCRD,
			PromiseIdentifier:           promise.GetName(),
			PromiseDestinationSelectors: promise.Spec.DestinationSelectors,
			Log:                         l,
			UID:                         "1234abcd",
		}

		resReq = createResourceRequest(resourceRequestPath)
		resReqNameNamespace = client.ObjectKeyFromObject(resReq)

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
				setConfigureWorkflowStatus(resReq, v1.ConditionTrue)
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

			By("setting the lastSuccessfulConfigureWorkflowTime in the resource status", func() {
				Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
				status := resReq.Object["status"]
				Expect(status).NotTo(BeNil())
				statusMap := status.(map[string]interface{})
				lastSuccessfulConfigureWorkflowTime, err := time.Parse(time.RFC3339, statusMap["lastSuccessfulConfigureWorkflowTime"].(string))
				Expect(err).NotTo(HaveOccurred())
				Expect(lastSuccessfulConfigureWorkflowTime).To(BeTemporally(">", startTime))
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

	Describe("Resource Request Status", func() {
		BeforeEach(func() {
			result, err := t.reconcileUntilCompletion(reconciler, resReq)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
			Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
			setReconcileConfigureWorkflowToReturnFinished()
		})

		Describe("lastSuccessfulConfigureWorkflowTime", func() {
			When("it's empty", func() {
				It("remains empty when the workflow fails", func() {
					setConfigureWorkflowStatus(resReq, v1.ConditionFalse)

					result, err := t.reconcileUntilCompletion(reconciler, resReq)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
					Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())

					status := resourceutil.GetStatus(resReq, "lastSuccessfulConfigureWorkflowTime")
					Expect(status).To(BeEmpty())
				})

				It("is set to the time the workflow finished with the right reason", func() {
					lastTransitionTime := time.Now().Add(-time.Minute)
					setConfigureWorkflowStatus(resReq, v1.ConditionTrue, lastTransitionTime)

					result, err := t.reconcileUntilCompletion(reconciler, resReq)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
					Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())

					lastSuccessfulConfigureWorkflowTime := resourceutil.GetStatus(resReq, "lastSuccessfulConfigureWorkflowTime")
					Expect(lastSuccessfulConfigureWorkflowTime).To(Equal(lastTransitionTime.Format(time.RFC3339)))
				})
			})

			When("it is set to a time", func() {
				BeforeEach(func() {
					lastTransitionTime := time.Now().Add(-time.Hour)
					setConfigureWorkflowStatus(resReq, v1.ConditionTrue, lastTransitionTime)

					result, err := t.reconcileUntilCompletion(reconciler, resReq)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
					Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
				})

				It("remains the same when the workflow fails", func() {
					before := resourceutil.GetStatus(resReq, "lastSuccessfulConfigureWorkflowTime")
					Expect(before).NotTo(BeEmpty())

					setConfigureWorkflowStatus(resReq, v1.ConditionFalse, time.Now())

					result, err := t.reconcileUntilCompletion(reconciler, resReq)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
					after := resourceutil.GetStatus(resReq, "lastSuccessfulConfigureWorkflowTime")
					Expect(after).NotTo(BeEmpty())
					Expect(before).To(Equal(after))
				})

				It("remains the same when the condition is True but not for the right Reason", func() {
					before := resourceutil.GetStatus(resReq, "lastSuccessfulConfigureWorkflowTime")
					Expect(before).NotTo(BeEmpty())
					resourceutil.SetCondition(resReq, &clusterv1.Condition{
						Type:               resourceutil.ConfigureWorkflowCompletedCondition,
						Status:             v1.ConditionTrue,
						Reason:             "SomeOtherReason",
						Message:            fmt.Sprintf("some-reason-%s", time.Now().Format(time.RFC3339)),
						LastTransitionTime: metav1.NewTime(time.Now()),
					})
					Expect(fakeK8sClient.Status().Update(ctx, resReq)).To(Succeed())

					result, err := t.reconcileUntilCompletion(reconciler, resReq)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))

					Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
					after := resourceutil.GetStatus(resReq, "lastSuccessfulConfigureWorkflowTime")
					Expect(after).NotTo(BeEmpty())
					Expect(before).To(Equal(after))
				})

				It("is updated when the workflow finishes successfully at a later time", func() {
					before := resourceutil.GetStatus(resReq, "lastSuccessfulConfigureWorkflowTime")
					Expect(before).NotTo(BeEmpty())

					expectedAfter := time.Now()
					setConfigureWorkflowStatus(resReq, v1.ConditionTrue, expectedAfter)

					result, err := t.reconcileUntilCompletion(reconciler, resReq)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
					Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())

					actualAfter := resourceutil.GetStatus(resReq, "lastSuccessfulConfigureWorkflowTime")
					Expect(actualAfter).NotTo(BeEmpty())
					Expect(actualAfter).To(Equal(expectedAfter.Format(time.RFC3339)))
				})
			})
		})
	})
})

func setConfigureWorkflowStatus(resReq *unstructured.Unstructured, status v1.ConditionStatus, lastTransitionTime ...time.Time) {
	var t time.Time
	if len(lastTransitionTime) > 0 {
		t = lastTransitionTime[0]
	} else {
		t = time.Now()
	}

	if resReq.Object["status"] == nil {
		resReq.Object["status"] = map[string]interface{}{}
	}
	resourceutil.SetCondition(resReq, &clusterv1.Condition{
		Type:               resourceutil.ConfigureWorkflowCompletedCondition,
		Status:             status,
		Reason:             resourceutil.PipelinesExecutedSuccessfully,
		Message:            fmt.Sprintf("some-reason-%s", t.Format(time.RFC3339)),
		LastTransitionTime: metav1.NewTime(t),
	})
	Expect(fakeK8sClient.Status().Update(ctx, resReq)).To(Succeed())
}

func createResourceRequest(resourceRequestPath string) *unstructured.Unstructured {
	yamlFile, err := os.ReadFile(resourceRequestPath)
	Expect(err).ToNot(HaveOccurred())

	resReq := &unstructured.Unstructured{}
	Expect(yaml.Unmarshal(yamlFile, resReq)).To(Succeed())

	Expect(fakeK8sClient.Create(ctx, resReq)).To(Succeed())
	resReqNameNamespace := client.ObjectKeyFromObject(resReq)

	Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
	resReq.SetUID("1234abcd")
	resReq.SetGeneration(1)

	Expect(fakeK8sClient.Update(ctx, resReq)).To(Succeed())
	Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
	return resReq
}
