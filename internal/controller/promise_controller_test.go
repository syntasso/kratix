package controller_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/syntasso/kratix/internal/controller"
	"github.com/syntasso/kratix/internal/controller/controllerfakes"
	controllerConfig "sigs.k8s.io/controller-runtime/pkg/config"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/yaml"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/compression"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// TODO: make this scoped to the test instead of to the entire suite
var (
	ctx                  context.Context
	reconciler           *controller.PromiseReconciler
	promise              *v1alpha1.Promise
	promiseName          types.NamespacedName
	promiseResourcesName types.NamespacedName

	promiseGroup        = "marketplace.kratix.io"
	promiseResourceName string
	expectedCRDName     string
	promiseCommonLabels map[string]string
	managerRestarted    bool
	l                   logr.Logger
	eventRecorder       *record.FakeRecorder
)

var _ = Describe("PromiseController", func() {
	BeforeEach(func() {
		promiseResourceName = "redis"
		expectedCRDName = promiseResourceName + "." + promiseGroup
		ctx = context.Background()
		managerRestarted = false
		l = ctrl.Log.WithName("controllers").WithName("Promise")
		m := &controllerfakes.FakeManager{}
		m.GetControllerOptionsReturns(controllerConfig.Controller{
			SkipNameValidation: ptr.To(true)})
		eventRecorder = record.NewFakeRecorder(1024)
		reconciler = &controller.PromiseReconciler{
			Client:              fakeK8sClient,
			ApiextensionsClient: fakeApiExtensionsClient,
			Log:                 l,
			Manager:             m,
			RestartManager: func() {
				managerRestarted = true
			},
			ReconciliationInterval: controller.DefaultReconciliationInterval,
			EventRecorder:          eventRecorder,
		}
	})

	Describe("Promise Reconciliation", func() {
		When("the promise is being created", func() {
			When("it contains everything apart from promise workflows", func() {
				BeforeEach(func() {
					promise = createPromise(promisePath)
				})

				It("re-reconciles until completion", func() {
					result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
						funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
					})

					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))

					By("creating the CRD", func() {
						crd, err := fakeApiExtensionsClient.CustomResourceDefinitions().Get(ctx, expectedCRDName, metav1.GetOptions{})

						Expect(err).NotTo(HaveOccurred())
						Expect(crd.Spec.Names.Plural + "." + crd.Spec.Group).To(Equal(expectedCRDName))
						status, ok := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"]
						Expect(ok).To(BeTrue(), "expected .status to exist")

						Expect(status.XPreserveUnknownFields).ToNot(BeNil())
						Expect(*status.XPreserveUnknownFields).To(BeTrue())

						message, ok := status.Properties["message"]
						Expect(ok).To(BeTrue(), ".status.message did not exist. Spec %v", status)
						Expect(message.Type).To(Equal("string"))

						observedGeneration, ok := status.Properties["observedGeneration"]
						Expect(ok).To(BeTrue(), ".status.observedGeneration did not exist. Spec %v", status)
						Expect(observedGeneration.Type).To(Equal("integer"))
						Expect(observedGeneration.Format).To(Equal("int64"))

						workflows, ok := status.Properties["workflows"]
						Expect(ok).To(BeTrue(), ".status.workflows did not exist. Spec %v", status)
						Expect(workflows.Type).To(Equal("integer"))
						Expect(workflows.Format).To(Equal("int"))

						workflowsSucceeded, ok := status.Properties["workflows"]
						Expect(ok).To(BeTrue(), ".status.workflowsSucceeded did not exist. Spec %v", status)
						Expect(workflowsSucceeded.Type).To(Equal("integer"))
						Expect(workflowsSucceeded.Format).To(Equal("int"))

						workflowsFailed, ok := status.Properties["workflows"]
						Expect(ok).To(BeTrue(), ".status.workflowsFailed did not exist. Spec %v", status)
						Expect(workflowsFailed.Type).To(Equal("integer"))
						Expect(workflowsFailed.Format).To(Equal("int"))

						conditions, ok := status.Properties["conditions"]
						Expect(ok).To(BeTrue())
						Expect(conditions.Type).To(Equal("array"))

						conditionsProperties := conditions.Items.Schema.Properties

						lastTransitionTime, ok := conditionsProperties["lastTransitionTime"]
						Expect(ok).To(BeTrue())
						Expect(lastTransitionTime.Type).To(Equal("string"))

						message, ok = conditionsProperties["message"]
						Expect(ok).To(BeTrue())
						Expect(message.Type).To(Equal("string"))

						reason, ok := conditionsProperties["reason"]
						Expect(ok).To(BeTrue())
						Expect(reason.Type).To(Equal("string"))

						status, ok = conditionsProperties["status"]
						Expect(ok).To(BeTrue())
						Expect(status.Type).To(Equal("string"))

						typeField, ok := conditionsProperties["type"]
						Expect(ok).To(BeTrue())
						Expect(typeField.Type).To(Equal("string"))

						printerFields := crd.Spec.Versions[0].AdditionalPrinterColumns
						Expect(printerFields).ToNot(BeNil())
					})

					By("updating the status with the CRD values", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Status.APIVersion).To(Equal("marketplace.kratix.io/v1alpha1"))
						Expect(promise.Status.Kind).To(Equal("redis"))
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
							Namespace: v1alpha1.SystemNamespace,
							Name:      "kratix-platform-controller-manager",
						}))
						Expect(binding.GetLabels()).To(Equal(promiseCommonLabels))
					})

					By("setting the finalizers", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Finalizers).To(ConsistOf(
							"kratix.io/dynamic-controller-dependant-resources-cleanup",
							"kratix.io/dependencies-cleanup",
							"kratix.io/resource-request-cleanup",
							"kratix.io/api-crd-cleanup",
						))
					})

					By("creating a Work resource for the dependencies", func() {
						work := getWork("kratix-platform-system", promise.GetName(), "", "")
						Expect(inCompressedContents(work.Spec.WorkloadGroups[0].Workloads[0].Content, []byte("kind: Deployment"))).To(BeTrue())
						Expect(inCompressedContents(work.Spec.WorkloadGroups[0].Workloads[0].Content, []byte("kind: ClusterRoleBinding"))).To(BeTrue())
						Expect(work.GetAnnotations()).To(HaveKey("kratix.io/last-updated-at"))

					})

					By("updating the status.observedGeneration", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Status.ObservedGeneration).To(Equal(promise.Generation))
					})

					By("starting the dynamic controller", func() {
						Expect(reconciler.StartedDynamicControllers).To(HaveLen(1))
						Expect(*reconciler.StartedDynamicControllers[promise.GetDynamicControllerName(logr.Logger{})].CanCreateResources).To(BeTrue())
					})
				})
			})

			When("the promise has requirements", func() {
				var promise *v1alpha1.Promise

				BeforeEach(func() {
					promise = promiseFromFile(promiseWithRequirements)
					promiseName = types.NamespacedName{
						Name:      promise.GetName(),
						Namespace: promise.GetNamespace(),
					}
					promiseResourceName = "namespaces"
					expectedCRDName = promiseResourceName + "." + promiseGroup

					Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					promise.UID = "1234abcd"
					Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())
				})

				When("the promise requirements are not installed", func() {
					BeforeEach(func() {
						_, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
							funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
						})
						Expect(err).To(MatchError("reconcile loop detected"))
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					})

					It("updates the status to indicate the dependencies are not installed and the promise is unavailable", func() {
						Expect(promise.Status.Conditions).To(HaveLen(2))

						requirementsCond, condErr := getCondition(promise, "RequirementsFulfilled")
						Expect(condErr).NotTo(HaveOccurred())
						Expect(requirementsCond.Type).To(Equal("RequirementsFulfilled"))
						Expect(requirementsCond.Status).To(Equal(metav1.ConditionFalse))
						Expect(requirementsCond.Message).To(Equal("Requirements not fulfilled"))
						Expect(requirementsCond.Reason).To(Equal("RequirementsNotInstalled"))
						Expect(requirementsCond.LastTransitionTime).ToNot(BeNil())
						Expect(promise.Status.RequiredPromises).To(ConsistOf(
							v1alpha1.RequiredPromiseStatus{
								Name:    "kafka",
								Version: "v1.2.0",
								State:   "Requirement not installed",
							},
						))
						Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusUnavailable))
						cond, condErr := getCondition(promise, v1alpha1.PromiseAvailableConditionType)
						Expect(condErr).NotTo(HaveOccurred())
						assertPromiseUnavailableCondition(cond)
					})

					It("prevents RRs being reconciled", func() {
						Expect(reconciler.StartedDynamicControllers).To(HaveLen(1))
						Expect(*reconciler.StartedDynamicControllers[promise.GetDynamicControllerName(logr.Logger{})].CanCreateResources).To(BeFalse())
					})
				})

				When("the promise requirements are not installed at the specified version", func() {
					BeforeEach(func() {
						requiredPromise := &v1alpha1.Promise{}
						requiredPromiseName := types.NamespacedName{
							Name: "kafka",
						}
						err := fakeK8sClient.Create(ctx, &v1alpha1.Promise{
							ObjectMeta: metav1.ObjectMeta{
								Name: "kafka",
							},
						})
						Expect(err).ToNot(HaveOccurred())
						Expect(fakeK8sClient.Get(ctx, requiredPromiseName, requiredPromise)).To(Succeed())
						requiredPromise.Status.Status = "Available"
						requiredPromise.Status.Version = "v1.0.0"
						Expect(fakeK8sClient.Status().Update(ctx, requiredPromise)).To(Succeed())
					})

					It("updates the status to indicate the dependencies are not installed at the specified version", func() {
						_, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
							funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
						})

						Expect(err).To(MatchError("reconcile loop detected"))
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())

						Expect(promise.Status.Conditions).To(HaveLen(2))

						requirementsCond, condErr := getCondition(promise, "RequirementsFulfilled")
						Expect(condErr).NotTo(HaveOccurred())
						Expect(requirementsCond.Type).To(Equal("RequirementsFulfilled"))
						Expect(requirementsCond.Status).To(Equal(metav1.ConditionFalse))
						Expect(requirementsCond.Message).To(Equal("Requirements not fulfilled"))
						Expect(requirementsCond.Reason).To(Equal("RequirementsNotInstalled"))
						Expect(requirementsCond.LastTransitionTime).ToNot(BeNil())

						Expect(promise.Status.RequiredPromises).To(ConsistOf(
							v1alpha1.RequiredPromiseStatus{
								Name:    "kafka",
								Version: "v1.2.0",
								State:   "Requirement not installed at the specified version",
							},
						))

						Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusUnavailable))
						cond, condErr := getCondition(promise, v1alpha1.PromiseAvailableConditionType)
						Expect(condErr).NotTo(HaveOccurred())
						assertPromiseUnavailableCondition(cond)
					})
				})

				When("the promise requirements are installed at the specified version", func() {
					When("the required promise is not available", func() {
						BeforeEach(func() {
							requiredPromise := &v1alpha1.Promise{}
							requiredPromiseName := types.NamespacedName{
								Name: "kafka",
							}
							err := fakeK8sClient.Create(ctx, &v1alpha1.Promise{
								ObjectMeta: metav1.ObjectMeta{
									Name: "kafka",
								},
							})
							Expect(err).ToNot(HaveOccurred())
							Expect(fakeK8sClient.Get(ctx, requiredPromiseName, requiredPromise)).To(Succeed())
							requiredPromise.Status.Status = "Unavailable"
							requiredPromise.Status.Version = "v1.2.0"
							Expect(fakeK8sClient.Status().Update(ctx, requiredPromise)).To(Succeed())
						})

						It("returns false for the RequirementsFulfilled condition", func() {
							_, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
								funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
							})

							Expect(err).To(MatchError("reconcile loop detected"))
							Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())

							Expect(promise.Status.Conditions).To(HaveLen(2))
							requirementCond, condErr := getCondition(promise, "RequirementsFulfilled")
							Expect(condErr).NotTo(HaveOccurred())
							Expect(requirementCond.Type).To(Equal("RequirementsFulfilled"))
							Expect(requirementCond.Status).To(Equal(metav1.ConditionFalse))
							Expect(promise.Status.RequiredPromises).To(ConsistOf(
								v1alpha1.RequiredPromiseStatus{
									Name:    "kafka",
									Version: "v1.2.0",
									State:   "Requirement not installed at the specified version",
								},
							))

							Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusUnavailable))
							cond, condErr := getCondition(promise, v1alpha1.PromiseAvailableConditionType)
							Expect(condErr).NotTo(HaveOccurred())
							assertPromiseUnavailableCondition(cond)
						})
					})

					When("the required promise is available", func() {
						BeforeEach(func() {
							requiredPromise := &v1alpha1.Promise{}
							requiredPromiseName := types.NamespacedName{
								Name: "kafka",
							}
							err := fakeK8sClient.Create(ctx, &v1alpha1.Promise{
								ObjectMeta: metav1.ObjectMeta{
									Name: "kafka",
								},
							})
							Expect(err).ToNot(HaveOccurred())
							Expect(fakeK8sClient.Get(ctx, requiredPromiseName, requiredPromise)).To(Succeed())
							requiredPromise.Status.Status = "Available"
							requiredPromise.Status.Version = "v1.2.0"
							Expect(fakeK8sClient.Status().Update(ctx, requiredPromise)).To(Succeed())
						})

						It("returns true for the RequirementsFulfilled condition", func() {
							_, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
								funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
							})

							Expect(err).NotTo(HaveOccurred())
							Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())

							By("updating the status to indicate the requirements are fulfilled", func() {
								Expect(promise.Status.Conditions).To(HaveLen(2))

								requirementsCond, condErr := getCondition(promise, "RequirementsFulfilled")
								Expect(condErr).NotTo(HaveOccurred())
								Expect(requirementsCond.Type).To(Equal("RequirementsFulfilled"))
								Expect(requirementsCond.Status).To(Equal(metav1.ConditionTrue))

								Expect(promise.Status.RequiredPromises).To(ConsistOf(
									v1alpha1.RequiredPromiseStatus{
										Name:    "kafka",
										Version: "v1.2.0",
										State:   "Requirement installed",
									},
								))
							})

							By("updating the status to indicate the promise is available", func() {
								Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusAvailable))
								cond, condErr := getCondition(promise, v1alpha1.PromiseAvailableConditionType)
								Expect(condErr).NotTo(HaveOccurred())
								assertPromiseAvailableCondition(cond)
							})

							By("firing an event to indicate the promise is available", func() {
								Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
									"Normal Available Promise is available")))
							})

							By("starting the dynamic controller", func() {
								Expect(reconciler.StartedDynamicControllers).To(HaveLen(1))
								Expect(*reconciler.StartedDynamicControllers[promise.GetDynamicControllerName(logr.Logger{})].CanCreateResources).To(BeTrue())
							})
						})
					})
				})

				When("the promise requirements are changed after being installed", func() {
					BeforeEach(func() {
						// Install the required Promise
						requiredPromise := &v1alpha1.Promise{}
						requiredPromiseName := types.NamespacedName{
							Name: "kafka",
						}
						err := fakeK8sClient.Create(ctx, &v1alpha1.Promise{
							ObjectMeta: metav1.ObjectMeta{
								Name: "kafka",
							},
						})
						Expect(err).ToNot(HaveOccurred())
						Expect(fakeK8sClient.Get(ctx, requiredPromiseName, requiredPromise)).To(Succeed())
						requiredPromise.Status.Status = "Available"
						requiredPromise.Status.Version = "v1.2.0"
						Expect(fakeK8sClient.Status().Update(ctx, requiredPromise)).To(Succeed())

						// Reconcile
						_, err = t.reconcileUntilCompletion(reconciler, promise, &opts{
							funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
						})

						Expect(err).NotTo(HaveOccurred())
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusAvailable))
						cond, condErr := getCondition(promise, v1alpha1.PromiseAvailableConditionType)
						Expect(condErr).NotTo(HaveOccurred())
						assertPromiseAvailableCondition(cond)

						// Make the required Promise unavailable
						Expect(fakeK8sClient.Get(ctx, requiredPromiseName, requiredPromise)).To(Succeed())
						requiredPromise.Status.Status = "Unavailable"
						Expect(fakeK8sClient.Status().Update(ctx, requiredPromise)).To(Succeed())

						// Reconcile
						_, err = t.reconcileUntilCompletion(reconciler, promise, &opts{
							funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
						})

						Expect(err).To(MatchError("reconcile loop detected"))
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					})

					It("updates the status to indicate the requirements are no longer fulfilled", func() {
						Expect(promise.Status.Conditions).To(HaveLen(2))

						requirementsCond, condErr := getCondition(promise, "RequirementsFulfilled")
						Expect(condErr).NotTo(HaveOccurred())
						Expect(requirementsCond.Type).To(Equal("RequirementsFulfilled"))
						Expect(requirementsCond.Status).To(Equal(metav1.ConditionFalse))

						Expect(promise.Status.RequiredPromises).To(ConsistOf(
							v1alpha1.RequiredPromiseStatus{
								Name:    "kafka",
								Version: "v1.2.0",
								State:   "Requirement not installed at the specified version",
							},
						))

						Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusUnavailable))
						cond, condErr := getCondition(promise, v1alpha1.PromiseAvailableConditionType)
						Expect(condErr).NotTo(HaveOccurred())
						assertPromiseUnavailableCondition(cond)
					})

					It("prevents RRs being reconciled", func() {
						Expect(reconciler.StartedDynamicControllers).To(HaveLen(1))
						Expect(*reconciler.StartedDynamicControllers[promise.GetDynamicControllerName(logr.Logger{})].CanCreateResources).To(BeFalse())
					})

					It("fires an event to indicate the promise is no longer available", func() {
						Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
							"Warning Unavailable Promise no longer available: Requirements have changed")))
					})
				})
			})

			When("it contains Promise workflows", func() {
				BeforeEach(func() {
					promise = promiseFromFile(promiseWithWorkflowPath)
					promiseName = types.NamespacedName{
						Name:      promise.GetName(),
						Namespace: promise.GetNamespace(),
					}
					promiseCommonLabels = map[string]string{
						"kratix.io/promise-name": promise.GetName(),
					}
					promiseResourcesName = types.NamespacedName{
						Name:      promise.GetName() + "-promise-pipeline",
						Namespace: "kratix-platform-system",
					}

					Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					promise.UID = "1234abcd"
					Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())
				})

				It("re-reconciles until completion", func() {
					result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
						funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
					})

					By("setting the finalizer for the Promise workflows", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Finalizers).To(ContainElement("kratix.io/workflows-cleanup"))
					})

					resources := reconcileConfigureOptsArg.Resources[0].GetObjects()
					By("creates a service account for pipeline", func() {
						Expect(resources[0]).To(BeAssignableToTypeOf(&v1.ServiceAccount{}))
						sa := resources[0].(*v1.ServiceAccount)
						Expect(sa.GetLabels()).To(Equal(promiseCommonLabels))
					})

					By("creates a config map with the promise scheduling in it", func() {
						Expect(resources[1]).To(BeAssignableToTypeOf(&v1.ConfigMap{}))
						configMap := resources[1].(*v1.ConfigMap)
						Expect(configMap.GetName()).To(Equal("destination-selectors-" + promise.GetName()))
						Expect(configMap.GetNamespace()).To(Equal("kratix-platform-system"))
						Expect(configMap.GetLabels()).To(Equal(promiseCommonLabels))
						Expect(configMap.Data).To(HaveKey("destinationSelectors"))
						space := regexp.MustCompile(`\s+`)
						destinationSelectors := space.ReplaceAllString(configMap.Data["destinationSelectors"], " ")
						Expect(strings.TrimSpace(destinationSelectors)).To(Equal(`- matchlabels: environment: dev source: promise`))
					})

					promiseResourcesName.Namespace = ""
					By("creates a role for the pipeline service account", func() {
						Expect(resources[2]).To(BeAssignableToTypeOf(&rbacv1.ClusterRole{}))
						role := resources[2].(*rbacv1.ClusterRole)
						Expect(role.GetLabels()).To(Equal(promiseCommonLabels))
						Expect(role.Rules).To(ConsistOf(
							rbacv1.PolicyRule{
								Verbs:     []string{"get", "list", "update", "create", "patch"},
								APIGroups: []string{"platform.kratix.io"},
								Resources: []string{"promises", "promises/status", "works"},
							},
						))
						Expect(role.GetLabels()).To(Equal(promiseCommonLabels))
					})

					By("associates the new role with the new service account", func() {
						Expect(resources[3]).To(BeAssignableToTypeOf(&rbacv1.ClusterRoleBinding{}))
						binding := resources[3].(*rbacv1.ClusterRoleBinding)
						Expect(binding.RoleRef.Name).To(Equal("promise-with-workflow-promise-configure-first-pipeline"))
						Expect(binding.Subjects).To(HaveLen(1))
						Expect(binding.Subjects[0]).To(Equal(rbacv1.Subject{
							Kind:      "ServiceAccount",
							Namespace: "kratix-platform-system",
							Name:      "promise-with-workflow-promise-configure-first-pipeline",
						}))
						Expect(binding.GetLabels()).To(Equal(promiseCommonLabels))
					})

					By("not requeueing while the job is in flight", func() {
						Expect(result).To(Equal(ctrl.Result{}))
						Expect(err).NotTo(HaveOccurred())
					})

					By("finishing the creation once the job is finished", func() {
						setReconcileConfigureWorkflowToReturnFinished()
						markPromiseWorkflowAsCompleted(fakeK8sClient, promise)
						result, err = t.reconcileUntilCompletion(reconciler, promise)

						Expect(err).NotTo(HaveOccurred())
						Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))
					})

					By("not creating a Work for the empty static dependencies", func() {
						works := &v1alpha1.WorkList{}
						Expect(fakeK8sClient.List(ctx, works)).To(Succeed())
						Expect(works.Items).To(BeEmpty())
					})
				})
			})

			When("it contains only dependencies, no API/workflows", func() {
				BeforeEach(func() {
					promise = promiseFromFile(promiseWithOnlyDepsPath)
					promiseName = types.NamespacedName{
						Name:      promise.GetName(),
						Namespace: promise.GetNamespace(),
					}
					promiseCommonLabels = map[string]string{
						"kratix.io/promise-name": promise.GetName(),
					}
					promiseResourcesName = types.NamespacedName{
						Name:      promise.GetName() + "-promise-pipeline",
						Namespace: "kratix-platform-system",
					}

					Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					promise.UID = "1234abcd"
					Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())
				})

				It("re-reconciles until completion", func() {
					result, err := t.reconcileUntilCompletion(reconciler, promise)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))

					crds, err := fakeApiExtensionsClient.CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(crds.Items).To(BeEmpty())
					works := &v1alpha1.WorkList{}
					Expect(fakeK8sClient.List(ctx, works)).To(Succeed())
					Expect(works.Items).To(HaveLen(1))
					Expect(works.Items[0].Spec.WorkloadGroups[0].Workloads).To(HaveLen(1))
					Expect(inCompressedContents(works.Items[0].Spec.WorkloadGroups[0].Workloads[0].Content, []byte("redisoperator"))).To(BeTrue())
					Expect(works.Items[0].Spec.WorkloadGroups[0].DestinationSelectors).To(HaveLen(1))
					Expect(works.Items[0].Spec.WorkloadGroups[0].DestinationSelectors[0]).To(Equal(
						v1alpha1.WorkloadGroupScheduling{
							MatchLabels: map[string]string{
								"environment": "dev",
							},
							Source: "promise",
						},
					))
				})
			})
		})

		When("the promise is being deleted", func() {
			When("the Promise has no delete workflow", func() {
				BeforeEach(func() {
					promise = promiseFromFile(promiseWithWorkflowPath)
					promiseName = types.NamespacedName{
						Name:      promise.GetName(),
						Namespace: promise.GetNamespace(),
					}
					promiseCommonLabels = map[string]string{
						"kratix.io/promise-name": promise.GetName(),
					}

					Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					promise.UID = "1234abcd"
					Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())

					setReconcileConfigureWorkflowToReturnFinished()
					markPromiseWorkflowAsCompleted(fakeK8sClient, promise)
					result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
						funcs: []func(client.Object) error{
							autoMarkCRDAsEstablished,
						},
					})

					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))
				})

				It("sets the finalizers on the Promise", func() {
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					Expect(promise.Finalizers).To(ConsistOf(
						"kratix.io/dynamic-controller-dependant-resources-cleanup",
						"kratix.io/dependencies-cleanup",
						"kratix.io/resource-request-cleanup",
						"kratix.io/api-crd-cleanup",
						"kratix.io/workflows-cleanup",
					))
				})

				It("deletes all resources for the promise workflow and dynamic controller", func() {
					//check resources all exist before deletion
					crds, err := fakeApiExtensionsClient.CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(crds.Items).To(HaveLen(1))

					clusterRoles := &rbacv1.ClusterRoleList{}
					clusterRoleBindings := &rbacv1.ClusterRoleBindingList{}
					jobs := &batchv1.JobList{}
					works := &v1alpha1.WorkList{}
					configMaps := &v1.ConfigMapList{}
					serviceAccounts := &v1.ServiceAccountList{}
					Expect(fakeK8sClient.List(ctx, clusterRoles)).To(Succeed())
					Expect(fakeK8sClient.List(ctx, clusterRoleBindings)).To(Succeed())
					Expect(fakeK8sClient.List(ctx, works)).To(Succeed())
					Expect(fakeK8sClient.List(ctx, jobs)).To(Succeed())
					Expect(fakeK8sClient.List(ctx, serviceAccounts)).To(Succeed())
					Expect(clusterRoles.Items).To(HaveLen(1))
					Expect(clusterRoleBindings.Items).To(HaveLen(1))
					Expect(works.Items).To(BeEmpty())
					Expect(jobs.Items).To(BeEmpty())
					Expect(serviceAccounts.Items).To(BeEmpty())

					//Delete
					Expect(fakeK8sClient.Delete(ctx, promise)).To(Succeed())
					result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{errorBudget: 5})
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
					Expect(managerRestarted).To(BeTrue())

					//Check they are all gone
					Expect(fakeK8sClient.List(ctx, jobs)).To(Succeed())
					Expect(jobs.Items).To(BeEmpty())

					crds, err = fakeApiExtensionsClient.CustomResourceDefinitions().List(ctx, metav1.ListOptions{})
					Expect(err).NotTo(HaveOccurred())
					Expect(crds.Items).To(BeEmpty())
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(MatchError(ContainSubstring("not found")))
					Expect(fakeK8sClient.List(ctx, clusterRoles)).To(Succeed())
					Expect(fakeK8sClient.List(ctx, clusterRoleBindings)).To(Succeed())
					Expect(fakeK8sClient.List(ctx, works)).To(Succeed())
					Expect(fakeK8sClient.List(ctx, configMaps)).To(Succeed())
					Expect(fakeK8sClient.List(ctx, serviceAccounts)).To(Succeed())
					Expect(clusterRoles.Items).To(BeEmpty())
					Expect(clusterRoleBindings.Items).To(BeEmpty())
					Expect(works.Items).To(BeEmpty())
					Expect(configMaps.Items).To(BeEmpty())
					Expect(serviceAccounts.Items).To(BeEmpty())
				})

				When("a resource request exists", func() {
					It("deletes them", func() {
						yamlFile, err := os.ReadFile(resourceRequestPath)
						Expect(err).ToNot(HaveOccurred())

						requestedResource := &unstructured.Unstructured{}
						Expect(yaml.Unmarshal(yamlFile, requestedResource)).To(Succeed())
						Expect(fakeK8sClient.Create(ctx, requestedResource)).To(Succeed())
						resNameNamespacedName := types.NamespacedName{
							Name:      requestedResource.GetName(),
							Namespace: requestedResource.GetNamespace(),
						}
						Expect(fakeK8sClient.Get(ctx, resNameNamespacedName, requestedResource)).To(Succeed())

						Expect(fakeK8sClient.Delete(ctx, promise)).To(Succeed())
						result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{errorBudget: 5})
						Expect(err).NotTo(HaveOccurred())
						Expect(result).To(Equal(ctrl.Result{}))
						Expect(managerRestarted).To(BeTrue())
						Expect(fakeK8sClient.Get(ctx, resNameNamespacedName, requestedResource)).To(MatchError(ContainSubstring("not found")))
					})
				})
			})

			When("the Promise has a delete workflow", func() {
				var err error

				BeforeEach(func() {
					promise = promiseFromFile(promiseWithDeleteWorkflowPath)
					promiseName = types.NamespacedName{
						Name:      promise.GetName(),
						Namespace: promise.GetNamespace(),
					}
					promiseCommonLabels = map[string]string{
						"kratix.io/promise-name": promise.GetName(),
					}

					promise.UID = "1234abcd"
					Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())

					setReconcileConfigureWorkflowToReturnFinished()
					result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
						funcs: []func(client.Object) error{
							autoMarkCRDAsEstablished,
						},
					})

					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))
				})

				It("sets the delete-workflows finalizer", func() {
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					Expect(promise.Finalizers).To(ConsistOf(
						"kratix.io/dynamic-controller-dependant-resources-cleanup",
						"kratix.io/dependencies-cleanup",
						"kratix.io/resource-request-cleanup",
						"kratix.io/api-crd-cleanup",
						"kratix.io/workflows-cleanup",
						"kratix.io/delete-workflows",
					))
				})

				It("requeues forever until the delete job finishes", func() {
					Expect(fakeK8sClient.Delete(ctx, promise)).To(Succeed())
					controller.SetReconcileDeleteWorkflow(func(w workflow.Opts) (bool, error) {
						reconcileDeleteOptsArg = w
						return true, nil
					})
					_, err = t.reconcileUntilCompletion(reconciler, promise)

					Expect(err).To(MatchError("reconcile loop detected"))
				})

				It("finishes the deletion once the job is finished", func() {
					Expect(fakeK8sClient.Delete(ctx, promise)).To(Succeed())
					_, err = t.reconcileUntilCompletion(reconciler, promise)
					controller.SetReconcileDeleteWorkflow(func(w workflow.Opts) (bool, error) {
						reconcileDeleteOptsArg = w
						return false, nil
					})

					setReconcileDeleteWorkflowToReturnFinished(promise)
					result, err := t.reconcileUntilCompletion(reconciler, promise)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
				})

				When("the Promise is updated to no longer have a delete workflow", func() {
					BeforeEach(func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						promise.Spec.Workflows.Promise.Delete = nil
						Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())

						setReconcileDeleteWorkflowToReturnFinished(promise)
						result, err := t.reconcileUntilCompletion(reconciler, promise)
						Expect(err).NotTo(HaveOccurred())
						Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))
					})

					It("removes the delete-workflows finalizer", func() {
						Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
						Expect(promise.Finalizers).To(ConsistOf(
							"kratix.io/dynamic-controller-dependant-resources-cleanup",
							"kratix.io/dependencies-cleanup",
							"kratix.io/resource-request-cleanup",
							"kratix.io/api-crd-cleanup",
							"kratix.io/workflows-cleanup",
						))
					})
				})
			})

			//TODO
			// - test https://github.com/syntasso/kratix/blob/dev/controllers/promise_controller_test.go#L65
		})

		When("the promise is being updated", func() {
			BeforeEach(func() {
				promise = promiseFromFile(promiseWithOnlyDepsPath)
				promiseName = types.NamespacedName{
					Name:      promise.GetName(),
					Namespace: promise.GetNamespace(),
				}
				promiseCommonLabels = map[string]string{
					"kratix.io/promise-name": promise.GetName(),
				}
				promiseResourcesName = types.NamespacedName{
					Name:      promise.GetName() + "-promise-pipeline",
					Namespace: "kratix-platform-system",
				}

				Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
				promise.UID = "1234abcd"
				Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())

				setReconcileConfigureWorkflowToReturnFinished()
				result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
					funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))
			})

			When("it contains static dependencies", func() {
				It("re-reconciles until completion", func() {
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					updatedPromise := promiseFromFile(promiseWithOnlyDepsUpdatedPath)
					promise.Spec = updatedPromise.Spec
					Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())

					setReconcileConfigureWorkflowToReturnFinished()
					result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
						funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
					})
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))

					By("updating the work", func() {
						works := &v1alpha1.WorkList{}
						Expect(fakeK8sClient.List(ctx, works)).To(Succeed())
						Expect(works.Items).To(HaveLen(1))
						Expect(works.Items[0].Spec.WorkloadGroups[0].Workloads).To(HaveLen(1))
						Expect(inCompressedContents(works.Items[0].Spec.WorkloadGroups[0].Workloads[0].Content, []byte("postgresoperator"))).To(BeTrue())
						Expect(works.Items[0].Spec.WorkloadGroups[0].DestinationSelectors).To(HaveLen(1))
						Expect(works.Items[0].Spec.WorkloadGroups[0].DestinationSelectors[0]).To(Equal(
							v1alpha1.WorkloadGroupScheduling{
								MatchLabels: map[string]string{
									"environment": "prod",
								},
								Source: "promise",
							},
						))
					})
				})
			})

			When("the static dependencies are removed", func() {
				It("re-reconciles until completion", func() {
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					promise.Spec.Dependencies = nil
					Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())

					setReconcileConfigureWorkflowToReturnFinished()
					result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
						funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
					})
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))

					By("deleting the work", func() {
						works := &v1alpha1.WorkList{}
						Expect(fakeK8sClient.List(ctx, works)).To(Succeed())
						Expect(works.Items).To(BeEmpty())
					})
				})
			})
		})

		When("the reconciliation interval is reached", func() {
			BeforeEach(func() {
				promise = createPromise(promiseWithWorkflowPath)
				setReconcileConfigureWorkflowToReturnFinished()

				result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
					funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
				})

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, promise)).To(Succeed())

				uPromise, err := promise.ToUnstructured()
				Expect(err).NotTo(HaveOccurred())

				resourceutil.SetCondition(uPromise, &clusterv1.Condition{
					Type:               resourceutil.ConfigureWorkflowCompletedCondition,
					Status:             v1.ConditionTrue,
					Message:            "Pipelines completed",
					Reason:             "PipelinesExecutedSuccessfully",
					LastTransitionTime: metav1.NewTime(time.Now().Add(-reconciler.ReconciliationInterval).Add(-time.Minute)),
				})
				Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())
			})

			It("re-runs the promise.configure workflow and sets the next reconciliation timestamp", func() {
				result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: promise.GetName(), Namespace: promise.GetNamespace()}})
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{}))

				Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())

				By("keeping promise as 'Available'", func() {
					Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusAvailable))
					cond, condErr := getCondition(promise, v1alpha1.PromiseAvailableConditionType)
					Expect(condErr).NotTo(HaveOccurred())
					assertPromiseAvailableCondition(cond)
				})

				By("adding the manual reconciliation label", func() {
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					Expect(promise.Labels[resourceutil.ManualReconciliationLabel]).To(Equal("true"))
				})

				By("running the pipelines", func() {
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, promise)).To(Succeed())

					uPromise, err := promise.ToUnstructured()
					Expect(err).NotTo(HaveOccurred())

					resourceutil.MarkConfigureWorkflowAsRunning(logr.Logger{}, uPromise)
					Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())

					result, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: promise.GetName(), Namespace: promise.GetNamespace()}})
					Expect(err).ToNot(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{}))
				})

				By("completing the pipelines", func() {
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, promise)).To(Succeed())

					uPromise, err := promise.ToUnstructured()
					Expect(err).NotTo(HaveOccurred())

					resourceutil.SetCondition(uPromise, &clusterv1.Condition{
						Type:               resourceutil.ConfigureWorkflowCompletedCondition,
						Status:             v1.ConditionTrue,
						Message:            "Pipeline completed",
						Reason:             "PipelineExecutedSuccessfully",
						LastTransitionTime: metav1.NewTime(time.Now()),
					})
					markWorkflowAsCompleted(uPromise)
					Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())

					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					Expect(promise.Status.Status).To(Equal(v1alpha1.PromiseStatusAvailable))
					cond, condErr := getCondition(promise, v1alpha1.PromiseAvailableConditionType)
					Expect(condErr).NotTo(HaveOccurred())
					assertPromiseAvailableCondition(cond)
				})

				By("firing an event to indicate the promise is available", func() {
					Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
						"Normal Available Promise is available")))
				})

				By("setting up the next reconciliation loop", func() {
					result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
						funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
					})

					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, promise)).To(Succeed())

					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))
				})
			})
		})

		When("promise labelled with ReconcileResources label", func() {
			var resReq *unstructured.Unstructured

			BeforeEach(func() {
				promise = createPromise(promiseWithWorkflowPath)
				setReconcileConfigureWorkflowToReturnFinished()
				_, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
					funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
				})
				Expect(err).NotTo(HaveOccurred())

				resReqBytes, err := os.ReadFile(resourceRequestPath)
				Expect(err).ToNot(HaveOccurred())

				resReq = &unstructured.Unstructured{}
				Expect(yaml.Unmarshal(resReqBytes, resReq)).To(Succeed())
				Expect(fakeK8sClient.Create(ctx, resReq)).To(Succeed())

				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
					Name:      resReq.GetName(),
					Namespace: resReq.GetNamespace(),
				}, resReq)).To(Succeed())
			})

			It("re runs all resource configure workflows", func() {
				Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
				promise.Labels[resourceutil.ReconcileResourcesLabel] = "true"
				Expect(fakeK8sClient.Update(context.TODO(), promise)).To(Succeed())

				_, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
					funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
				})
				Expect(err).NotTo(HaveOccurred())

				By("labelling manual reconciliation on resources", func() {
					updatedRR := &unstructured.Unstructured{}
					updatedRR.SetGroupVersionKind(schema.GroupVersionKind{
						Group:   "marketplace.kratix.io",
						Version: "v1alpha1",
						Kind:    "redis",
					})
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
						Name:      resReq.GetName(),
						Namespace: resReq.GetNamespace(),
					}, updatedRR)).To(Succeed())
					Expect(updatedRR.GetLabels()).To(HaveKeyWithValue(resourceutil.ManualReconciliationLabel, "true"))
				})

				By("removing ReconcileResources label from promise", func() {
					Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
					Expect(promise.Labels).NotTo(HaveKey(resourceutil.ReconcileResourcesLabel))
				})

				By("firing an event to indicate the resources are being reconciled", func() {
					Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
						"Normal ReconcilingResources Reconciling all resource requests")))
				})
			})
		})
	})

	Describe("Promise API", func() {
		When("the crd does not define additional printer columns", func() {
			It("uses the default printer columns", func() {
				promise = createPromise(promisePath)

				result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
					funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))

				crd, err := fakeApiExtensionsClient.CustomResourceDefinitions().Get(ctx, expectedCRDName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())
				Expect(crd.Spec.Versions).To(HaveLen(1))

				Expect(crd.Spec.Versions[0].AdditionalPrinterColumns).To(HaveLen(1))
				Expect(crd.Spec.Versions[0].AdditionalPrinterColumns[0].Name).To(Equal("status"))
				Expect(crd.Spec.Versions[0].AdditionalPrinterColumns[0].Type).To(Equal("string"))
				Expect(crd.Spec.Versions[0].AdditionalPrinterColumns[0].JSONPath).To(Equal(".status.message"))
			})
		})

		When("the crd defines additional printer columns", func() {
			It("uses the additional printer columns as defined", func() {
				promise = createPromise(promisePath)

				_, promiseCrd, err := promise.GetAPI()
				Expect(err).ToNot(HaveOccurred())
				promiseCrd.Spec.Versions[0].AdditionalPrinterColumns = []apiextensionsv1.CustomResourceColumnDefinition{
					{
						Name:     "MyField",
						Type:     "string",
						JSONPath: ".status.myfield",
					},
				}
				b, err := json.Marshal(promiseCrd)
				Expect(err).NotTo(HaveOccurred())
				rawCrd := &runtime.RawExtension{Raw: b}
				promise.Spec.API = rawCrd
				Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())

				result, err := t.reconcileUntilCompletion(reconciler, promise, &opts{
					funcs: []func(client.Object) error{autoMarkCRDAsEstablished},
				})

				Expect(err).NotTo(HaveOccurred())
				Expect(result).To(Equal(ctrl.Result{RequeueAfter: reconciler.ReconciliationInterval}))

				crd, err := fakeApiExtensionsClient.CustomResourceDefinitions().Get(ctx, expectedCRDName, metav1.GetOptions{})
				Expect(err).ToNot(HaveOccurred())

				Expect(crd.Spec.Versions[0].AdditionalPrinterColumns).To(HaveLen(1))
				Expect(crd.Spec.Versions[0].AdditionalPrinterColumns[0].Name).To(Equal("MyField"))
				Expect(crd.Spec.Versions[0].AdditionalPrinterColumns[0].Type).To(Equal("string"))
				Expect(crd.Spec.Versions[0].AdditionalPrinterColumns[0].JSONPath).To(Equal(".status.myfield"))
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

func getWork(namespace, promiseName, resourceName, pipelineName string) v1alpha1.Work {
	ExpectWithOffset(1, fakeK8sClient).NotTo(BeNil())
	works := v1alpha1.WorkList{}

	l := map[string]string{}
	l[v1alpha1.PromiseNameLabel] = promiseName
	l[v1alpha1.WorkTypeLabel] = v1alpha1.WorkTypeStaticDependency

	if pipelineName != "" {
		l[v1alpha1.PipelineNameLabel] = pipelineName
		l[v1alpha1.WorkTypeLabel] = v1alpha1.WorkTypePromise
	}

	if resourceName != "" {
		l[v1alpha1.ResourceNameLabel] = resourceName
		l[v1alpha1.WorkTypeLabel] = v1alpha1.WorkTypeResource
	}

	workSelectorLabel := labels.FormatLabels(l)
	selector, err := labels.Parse(workSelectorLabel)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	err = fakeK8sClient.List(context.Background(), &works, &client.ListOptions{
		LabelSelector: selector,
		Namespace:     namespace,
	})
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, works.Items).To(HaveLen(1))

	return works.Items[0]
}

func createPromise(promisePath string) *v1alpha1.Promise {
	promise := promiseFromFile(promisePath)
	promiseName = client.ObjectKeyFromObject(promise)

	promiseCommonLabels = map[string]string{
		"kratix.io/promise-name": promise.GetName(),
	}

	Expect(fakeK8sClient.Create(ctx, promise)).To(Succeed())
	Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
	promise.UID = "1234abcd"
	Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())
	Expect(fakeK8sClient.Get(ctx, promiseName, promise)).To(Succeed())
	return promise
}

func inCompressedContents(compressedContent string, content []byte) bool {
	decompressedContent, err := compression.DecompressContent([]byte(compressedContent))
	Expect(err).ToNot(HaveOccurred())
	return bytes.Contains(decompressedContent, content)
}

func markWorkflowAsCompleted(obj *unstructured.Unstructured) {
	resourceutil.SetCondition(obj, &clusterv1.Condition{
		Type:               resourceutil.ConfigureWorkflowCompletedCondition,
		Status:             v1.ConditionTrue,
		Message:            "Pipelines completed",
		Reason:             "PipelinesExecutedSuccessfully",
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func markPromiseWorkflowAsCompleted(client client.Client, p *v1alpha1.Promise) {
	p.Status.Conditions = []metav1.Condition{
		metav1.Condition{
			Type:               string(resourceutil.ConfigureWorkflowCompletedCondition),
			Status:             metav1.ConditionTrue,
			Message:            "Pipelines completed",
			Reason:             "PipelinesExecutedSuccessfully",
			LastTransitionTime: metav1.NewTime(time.Now()),
		},
	}
	Expect(client.Status().Update(ctx, p)).To(Succeed())
}

func getCondition(p *v1alpha1.Promise, condType string) (*metav1.Condition, error) {
	for _, cond := range p.Status.Conditions {
		if cond.Type == condType {
			return &cond, nil
		}
	}
	return nil, fmt.Errorf("cannot find condition of type %s", condType)
}

func assertPromiseAvailableCondition(cond *metav1.Condition) {
	ExpectWithOffset(1, cond.Type).To(Equal(v1alpha1.PromiseAvailableConditionType))
	ExpectWithOffset(1, cond.Status).To(Equal(metav1.ConditionTrue))
	ExpectWithOffset(1, cond.Message).To(Equal("Ready to fulfil resource requests"))
	ExpectWithOffset(1, cond.Reason).To(Equal(v1alpha1.PromiseAvailableConditionTrueReason))
	ExpectWithOffset(1, cond.LastTransitionTime).ToNot(BeNil())
}

func assertPromiseUnavailableCondition(cond *metav1.Condition) {
	ExpectWithOffset(1, cond.Type).To(Equal(v1alpha1.PromiseAvailableConditionType))
	ExpectWithOffset(1, cond.Status).To(Equal(metav1.ConditionFalse))
	ExpectWithOffset(1, cond.Message).To(Equal("Cannot fulfil resource requests"))
	ExpectWithOffset(1, cond.Reason).To(Equal(v1alpha1.PromiseAvailableConditionFalseReason))
	ExpectWithOffset(1, cond.LastTransitionTime).ToNot(BeNil())
}
