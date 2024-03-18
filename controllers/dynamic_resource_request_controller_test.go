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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
			Version: string(rrCRD.Spec.Versions[0].Name),
			Kind:    rrCRD.Spec.Names.Kind,
		}

		Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
		Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
		promise.UID = types.UID("1234abcd")
		Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())

		enabled := true
		reconciler = &controllers.DynamicResourceRequestController{
			CanCreateResources: &enabled,
			Client:             fakeK8sClient,
			Scheme:             scheme.Scheme,
			GVK:                &rrGVK,
			CRD:                rrCRD,
			PromiseIdentifier:  promise.GetName(),
			ConfigurePipelines: []v1alpha1.Pipeline{
				{
					Spec: v1alpha1.PipelineSpec{
						Containers: []v1alpha1.Container{
							{
								Name:  "test",
								Image: "configure:v0.1.0",
							},
						},
					},
				},
			},
			DeletePipelines: []v1alpha1.Pipeline{
				{
					Spec: v1alpha1.PipelineSpec{
						Containers: []v1alpha1.Container{
							{
								Name:  "test",
								Image: "delete:v0.1.0",
							},
						},
					},
				},
			},

			PromiseDestinationSelectors: promise.Spec.DestinationSelectors,
			// promiseWorkflowSelectors:    work.GetDefaultScheduling("promise-workflow"),
			Log:     ctrl.Log.WithName("controllers").WithName("Promise"),
			UID:     "1234abcd",
			Enabled: &enabled,
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
		Expect(fakeK8sClient.Update(ctx, resReq)).To(Succeed())
		Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())

		promiseCommonLabels = map[string]string{
			"kratix-promise-id":                  promise.GetName(),
			"kratix-promise-resource-request-id": promise.GetName() + "-" + resReq.GetName(),
			"kratix.io/resource-name":            resReq.GetName(),
			"kratix.io/promise-name":             promise.GetName(),
			"kratix.io/work-type":                "resource",
		}

	})

	When("resource is being created", func() {
		It("re-reconciles until completetion", func() {
			_, err := t.reconcileUntilCompletion(reconciler, resReq)
			Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())

			resourceCommonName := types.NamespacedName{
				Name:      promise.GetName() + "-resource-pipeline",
				Namespace: "default",
			}

			resourceLabels := map[string]string{
				"kratix-promise-id": promise.GetName(),
			}

			By("creating a service account for pipeline", func() {
				sa := &v1.ServiceAccount{}
				Eventually(func() error {
					return fakeK8sClient.Get(ctx, resourceCommonName, sa)
				}, timeout, interval).Should(Succeed(), "Expected SA for pipeline to exist")

				Expect(sa.GetLabels()).To(Equal(resourceLabels))
			})

			By("creates a role for the pipeline service account", func() {
				role := &rbacv1.Role{}
				Eventually(func() error {
					return fakeK8sClient.Get(ctx, resourceCommonName, role)
				}, timeout, interval).Should(Succeed(), "Expected Role for pipeline to exist")

				Expect(role.GetLabels()).To(Equal(resourceLabels))
				Expect(role.Rules).To(ConsistOf(
					rbacv1.PolicyRule{
						Verbs:     []string{"get", "list", "update", "create", "patch"},
						APIGroups: []string{promiseGroup},
						Resources: []string{"redis", "redis/status"},
					},
					rbacv1.PolicyRule{
						Verbs:     []string{"get", "update", "create", "patch"},
						APIGroups: []string{"platform.kratix.io"},
						Resources: []string{"works"},
					},
				))
				Expect(role.GetLabels()).To(Equal(resourceLabels))
			})

			By("associates the new role with the new service account", func() {
				binding := &rbacv1.RoleBinding{}
				Expect(fakeK8sClient.Get(ctx, resourceCommonName, binding)).To(Succeed(), "Expected RoleBinding for pipeline to exist")
				Expect(binding.RoleRef.Name).To(Equal(resourceCommonName.Name))
				Expect(binding.Subjects).To(HaveLen(1))
				Expect(binding.Subjects[0]).To(Equal(rbacv1.Subject{
					Kind:      "ServiceAccount",
					Namespace: resReq.GetNamespace(),
					Name:      resourceCommonName.Name,
				}))
				Expect(binding.GetLabels()).To(Equal(resourceLabels))
			})

			By("creates a config map with the promise scheduling in it", func() {
				configMap := &v1.ConfigMap{}
				configMapName := types.NamespacedName{
					Name:      "destination-selectors-" + promise.GetName(),
					Namespace: "default",
				}
				Expect(fakeK8sClient.Get(ctx, configMapName, configMap)).To(Succeed(), "Expected ConfigMap for pipeline to exist")
				Expect(configMap.GetLabels()).To(Equal(resourceLabels))
				Expect(configMap.Data).To(HaveKey("destinationSelectors"))
				space := regexp.MustCompile(`\s+`)
				destinationSelectors := space.ReplaceAllString(configMap.Data["destinationSelectors"], " ")
				Expect(strings.TrimSpace(destinationSelectors)).To(Equal(`- matchlabels: environment: dev source: promise`))
			})

			By("requeuing forever until jobs finishes", func() {
				Expect(err).To(MatchError("reconcile loop detected"))
				jobs := &batchv1.JobList{}
				Expect(fakeK8sClient.List(ctx, jobs)).To(Succeed())
				Expect(jobs.Items).To(HaveLen(1))
				Expect(jobs.Items[0].Spec.Template.Spec.InitContainers[1].Image).To(Equal("configure:v0.1.0"))
			})

			By("finishing the creation once the job is finished", func() {
				result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
					funcs: []func(client.Object) error{
						autoCompleteJobAndCreateWork(promiseCommonLabels, promise.GetName()+"-"+resReq.GetName()),
					},
				})

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))
			})

			By("seting the finalizers on the resource", func() {
				Expect(resReq.GetFinalizers()).To(ConsistOf(
					"kratix.io/work-cleanup",
					"kratix.io/workflows-cleanup",
					"kratix.io/delete-workflows",
				))
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

	When("resource is being updated", func() {
		BeforeEach(func() {
			result, err := t.reconcileUntilCompletion(reconciler, resReq, &opts{
				funcs: []func(client.Object) error{
					autoCompleteJobAndCreateWork(promiseCommonLabels, promise.GetName()+"-"+resReq.GetName()),
				},
			})

			Expect(result).To(Equal(ctrl.Result{}))
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())

			jobs := &batchv1.JobList{}
			Expect(fakeK8sClient.List(ctx, jobs)).To(Succeed())
			Expect(jobs.Items).To(HaveLen(1))
			Expect(jobs.Items[0].Spec.Template.Spec.InitContainers[1].Image).To(Equal("configure:v0.1.0"))
		})

		It("re-runs the pipeline", func() {
			yamlFile, err := os.ReadFile(resourceRequestUpdatedPath)
			Expect(err).ToNot(HaveOccurred())

			updateResReq := &unstructured.Unstructured{}
			Expect(yaml.Unmarshal(yamlFile, updateResReq)).To(Succeed())

			Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
			resReq.Object["spec"] = updateResReq.Object["spec"]
			Expect(fakeK8sClient.Update(ctx, resReq)).To(Succeed())

			//run the reconciler
			//check that a new job runs for the new resource request
			_, err = t.reconcileUntilCompletion(reconciler, resReq, &opts{
				funcs: []func(client.Object) error{
					// autoCompleteJobAndCreateWork(promiseCommonLabels, promise.GetName()+"-"+resReq.GetName()),
				},
			})

			By("requeuing forever until the new jobs finishes", func() {
				Expect(err).To(MatchError("reconcile loop detected"))
				jobs := &batchv1.JobList{}
				Expect(fakeK8sClient.List(ctx, jobs)).To(Succeed())
				Expect(jobs.Items).To(HaveLen(2))
				Expect(jobs.Items[0].Spec.Template.Spec.InitContainers[1].Image).To(Equal("configure:v0.1.0"))
				Expect(jobs.Items[1].Spec.Template.Spec.InitContainers[1].Image).To(Equal("configure:v0.1.0"))
			})

			result, err := t.reconcileUntilCompletion(reconciler, resReq, &opts{
				funcs: []func(client.Object) error{
					autoCompleteJobAndCreateWork(promiseCommonLabels, promise.GetName()+"-"+resReq.GetName()),
				},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))

			result, err = t.reconcileUntilCompletion(reconciler, resReq)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
			By("not creating additional jobs when the resource is unchanged", func() {
				jobs := &batchv1.JobList{}
				Expect(fakeK8sClient.List(ctx, jobs)).To(Succeed())
				Expect(jobs.Items).To(HaveLen(2))
			})
		})

		When("the request is updated repeatedly", func() {
			It("ensures only the last 5 jobs are kept", func() {
				var timestamp time.Time
				for i := 0; i < 10; i++ {
					if i == 6 {
						timestamp = time.Now()
					}

					Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
					resReq.Object["spec"].(map[string]interface{})["size"] = fmt.Sprintf("%d", i)
					Expect(fakeK8sClient.Update(ctx, resReq)).To(Succeed())

					_, err := t.reconcileUntilCompletion(reconciler, resReq, &opts{
						funcs: []func(client.Object) error{
							autoCompleteJobAndCreateWork(promiseCommonLabels, promise.GetName()+"-"+resReq.GetName()),
						},
					})
					Expect(err).NotTo(HaveOccurred())
				}

				jobs := &batchv1.JobList{}
				Expect(fakeK8sClient.List(ctx, jobs)).To(Succeed())
				Expect(jobs.Items).To(HaveLen(5))
				for _, job := range jobs.Items {
					Expect(job.CreationTimestamp.Time).To(BeTemporally(">", timestamp))
				}
			})
		})
	})

	When("resource is being deleted", func() {
		BeforeEach(func() {
			result, err := t.reconcileUntilCompletion(reconciler, resReq, &opts{
				funcs: []func(client.Object) error{
					autoCompleteJobAndCreateWork(promiseCommonLabels, promise.GetName()+"-"+resReq.GetName()),
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(ctrl.Result{}))
			Expect(fakeK8sClient.Get(ctx, resReqNameNamespace, resReq)).To(Succeed())
			Expect(resReq.GetFinalizers()).To(ConsistOf(
				"kratix.io/work-cleanup",
				"kratix.io/workflows-cleanup",
				"kratix.io/delete-workflows",
			))
		})

		It("re-reconciles until completetion", func() {
			Expect(fakeK8sClient.Delete(ctx, resReq)).To(Succeed())
			_, err := t.reconcileUntilCompletion(reconciler, resReq)

			By("requeuing forever until delete jobs finishes", func() {
				Expect(err).To(MatchError("reconcile loop detected"))
				jobs := &batchv1.JobList{}
				Expect(fakeK8sClient.List(ctx, jobs)).To(Succeed())
				Expect(jobs.Items).To(HaveLen(2))
				Expect([]string{
					jobs.Items[0].Spec.Template.Spec.InitContainers[1].Image,
					jobs.Items[1].Spec.Template.Spec.Containers[0].Image,
				}).To(ConsistOf("configure:v0.1.0", "delete:v0.1.0"))
			})

			result, err := t.reconcileUntilCompletion(reconciler, resReq, &opts{
				funcs: []func(client.Object) error{
					autoCompleteJobAndCreateWork(promiseCommonLabels, promise.GetName()+"-"+resReq.GetName()),
				},
			})
			Expect(result).To(Equal(ctrl.Result{}))
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
