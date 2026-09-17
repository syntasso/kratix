package workflow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

var namespace = "kratix-platform-system"

var _ = Describe("Workflow Reconciler", func() {
	var promise v1alpha1.Promise
	var workflowPipelines []v1alpha1.PipelineJobResources
	var uPromise *unstructured.Unstructured
	var pipelines []v1alpha1.Pipeline
	var eventRecorder *events.FakeRecorder

	BeforeEach(func() {
		eventRecorder = events.NewFakeRecorder(1024)

		promise = v1alpha1.Promise{
			ObjectMeta: metav1.ObjectMeta{
				Name: "redis",
			},
			TypeMeta: metav1.TypeMeta{
				APIVersion: "platform.kratix.io/v1alpha1",
				Kind:       "Promise",
			},
			Status: v1alpha1.PromiseStatus{
				Kratix: v1alpha1.KratixPromiseStatus{
					Workflows: v1alpha1.WorkflowStatuses{"configure": {
						Pipelines: []v1alpha1.WorkflowPipelineStatus{
							{Name: "pipeline-1", Phase: v1alpha1.WorkflowPhasePending},
							{Name: "pipeline-2", Phase: v1alpha1.WorkflowPhasePending},
						},
					}},
				},
			},
		}

		pipelines = []v1alpha1.Pipeline{{
			Kind:       "Pipeline",
			APIVersion: "kratix.io/v1alpha1",
			ObjectMeta: metav1.ObjectMeta{
				Name: "pipeline-1",
			},
			Spec: v1alpha1.PipelineSpec{
				Containers: []v1alpha1.Container{
					{Name: "container-1", Image: "busybox"},
				},
			},
		}, {
			Kind:       "Pipeline",
			APIVersion: "kratix.io/v1alpha1",
			ObjectMeta: metav1.ObjectMeta{
				Name: "pipeline-2",
			},
			Spec: v1alpha1.PipelineSpec{
				Containers: []v1alpha1.Container{
					{Name: "container-1", Image: "busybox"},
				},
			},
		}}

		promise.Spec.Workflows.Promise.Configure = make([]unstructured.Unstructured, len(pipelines))
		for i, p := range pipelines {
			obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&p)
			Expect(err).NotTo(HaveOccurred())
			promise.Spec.Workflows.Promise.Configure[i] = unstructured.Unstructured{Object: obj}
		}

		Expect(fakeK8sClient.Create(ctx, &promise)).To(Succeed())
		Expect(fakeK8sClient.Status().Update(ctx, &promise)).To(Succeed())
	})

	Describe("ReconcileConfigure", func() {
		BeforeEach(func() {
			workflowPipelines, uPromise = setupTest(promise, pipelines)
		})

		DescribeTable("progress without retained Jobs", func(succeeded int, phase, trigger string, expectedPipeline int) {
			api, err := json.Marshal(fakeCRD)
			Expect(err).NotTo(HaveOccurred())
			promise.Spec.API = &runtime.RawExtension{Raw: api}
			rr := &unstructured.Unstructured{}
			rr.SetAPIVersion("mygroup.example/v1")
			rr.SetKind("TheKind")
			rr.SetName("request")
			rr.SetNamespace(namespace)
			rr.SetLabels(map[string]string{v1alpha1.PromiseNameLabel: promise.Name})
			Expect(fakeK8sClient.Create(ctx, rr)).To(Succeed())
			resources := make([]v1alpha1.PipelineJobResources, len(pipelines))
			for i, pipeline := range pipelines {
				var err error
				resources[i], err = pipeline.ForResource(&promise, v1alpha1.WorkflowActionConfigure, rr).Resources(nil)
				Expect(err).NotTo(HaveOccurred())
			}
			setParentPipelinesSucceeded(rr, resources, succeeded)
			if succeeded < len(resources) {
				Expect(resourceutil.MarkCurrentPipelineAs(phase, rr, logger, resources[succeeded].Job, "configure")).To(Succeed())
			} else {
				Expect(unstructured.SetNestedSlice(rr.Object, []any{map[string]any{
					"type": "ConfigureWorkflowCompleted", "status": "True",
					"reason":             resourceutil.PipelinesExecutedSuccessfully,
					"lastTransitionTime": metav1.Now().Format(time.RFC3339),
				}}, "status", "conditions")).To(Succeed())
			}
			Expect(fakeK8sClient.Status().Update(ctx, rr)).To(Succeed())
			switch trigger {
			case "interval":
				rr.SetLabels(labels.Merge(rr.GetLabels(), map[string]string{resourceutil.WorkflowRunFromStartLabel: "true"}))
				Expect(fakeK8sClient.Update(ctx, rr)).To(Succeed())
			case "spec":
				Expect(unstructured.SetNestedField(rr.Object, "changed", "spec", "value")).To(Succeed())
				Expect(fakeK8sClient.Update(ctx, rr)).To(Succeed())
				for i, pipeline := range pipelines {
					var err error
					resources[i], err = pipeline.ForResource(&promise, v1alpha1.WorkflowActionConfigure, rr).Resources(nil)
					Expect(err).NotTo(HaveOccurred())
				}
			case "running Job":
				resources[succeeded].Job.Status.Active = 1
				Expect(fakeK8sClient.Create(ctx, resources[succeeded].Job)).To(Succeed())
			}
			opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, rr, resources, "resource", 0, namespace)
			_, err = workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			if expectedPipeline < 0 {
				Expect(listJobs(namespace)).To(BeEmpty())
				return
			}
			Expect(listJobs(namespace)).To(ConsistOf(HaveField("Name", resources[expectedPipeline].Job.Name)))
			Expect(resourceutil.GetCurrentPipelinePhase(rr, resources[expectedPipeline].Job, "configure")).To(Equal(v1alpha1.WorkflowPhaseRunning))
			if trigger != "running Job" {
				Expect(resourceutil.GetConfigureWorkflowCompletedConditionStatus(rr)).To(Equal(v1.ConditionFalse))
			}
			createdJob := resources[expectedPipeline].Job.Name
			resetWorkflowPipelineJobs(resources)
			_, err = workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(listJobs(namespace)).To(ConsistOf(HaveField("Name", createdJob)))
		},
			Entry("advances past a succeeded pipeline", 1, "Pending", "", 1),
			Entry("recreates a missing running Job", 1, "Running", "", 1),
			Entry("waits for the running Job", 1, "Running", "running Job", 1),
			Entry("restarts after a spec change during a run", 1, "Running", "spec", 0),
			Entry("restarts after a spec change following failure", 1, "Failed", "spec", 0),
			Entry("restarts after a spec change", 2, "", "spec", 0),
			Entry("restarts after the reconciliation interval", 2, "", "interval", 0),
			Entry("stays completed without a new run", 2, "", "", -1),
			Entry("preserves failure until a new run", 1, "Failed", "", -1),
		)

		DescribeTable("recreates the current run even when an earlier Job remains", func(action v1alpha1.Action, failed bool) {
			resource, err := pipelines[0].ForPromise(&promise, action).Resources(nil)
			Expect(err).NotTo(HaveOccurred())
			resources := []v1alpha1.PipelineJobResources{resource}
			key := string(action)
			reconcileWorkflow := workflow.ReconcileConfigure
			if action == v1alpha1.WorkflowActionDelete {
				reconcileWorkflow = workflow.ReconcileDelete
			}
			Expect(fakeK8sClient.Create(ctx, resource.Job)).To(Succeed())
			if failed {
				markJobAsFailed(resource.Job.Name)
			} else {
				markJobAsComplete(resource.Job.Name)
			}
			resetWorkflowPipelineJobs(resources)
			Expect(resourceutil.ResetPipelineStatusToPending(uPromise, resources, key)).To(Succeed())
			Expect(resourceutil.MarkCurrentPipelineAsRunning(uPromise, logger, resource.Job, key)).To(Succeed())
			Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())
			resetWorkflowPipelineJobs(resources)
			opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, resources, "promise", 5, namespace)
			requeue, err := reconcileWorkflow(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(requeue).To(BeTrue())
			Expect(listJobs(namespace)).To(HaveLen(2))
			Expect(resourceutil.GetCurrentPipelinePhase(uPromise, resource.Job, key)).To(Equal(v1alpha1.WorkflowPhaseRunning))
			markJobAsComplete(resource.Job.Name)
			_, err = reconcileWorkflow(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(resourceutil.GetCurrentPipelinePhase(uPromise, resource.Job, key)).To(Equal(v1alpha1.WorkflowPhaseSucceeded))
		},
			Entry("configure with an earlier success", v1alpha1.WorkflowActionConfigure, false),
			Entry("configure with an earlier failure", v1alpha1.WorkflowActionConfigure, true),
			Entry("delete with an earlier success", v1alpha1.WorkflowActionDelete, false),
			Entry("delete with an earlier failure", v1alpha1.WorkflowActionDelete, true),
		)

		DescribeTable("completes with zero retained Jobs", func(action v1alpha1.Action, key, workflowType string) {
			resources := make([]v1alpha1.PipelineJobResources, len(pipelines))
			for i, pipeline := range pipelines {
				factory := pipeline.ForPromise(&promise, action)
				factory.WorkflowType = v1alpha1.Type(workflowType)
				var err error
				resources[i], err = factory.Resources(nil)
				Expect(err).NotTo(HaveOccurred())
			}
			reconcileWorkflow := workflow.ReconcileConfigure
			if action == v1alpha1.WorkflowActionDelete {
				resources = resources[:1]
				reconcileWorkflow = workflow.ReconcileDelete
			}
			originalStatus, _, err := unstructured.NestedMap(uPromise.Object, "status")
			Expect(err).NotTo(HaveOccurred())
			opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, resources, workflowType, 0, namespace)
			opts.WorkflowKey = key
			for _, resource := range resources {
				_, err := reconcileWorkflow(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(listJobs(namespace)).To(ConsistOf(HaveField("Name", resource.Job.Name)))
				jobName := resource.Job.Name
				resetWorkflowPipelineJobs(resources)
				_, err = reconcileWorkflow(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(listJobs(namespace)).To(ConsistOf(HaveField("Name", jobName)))
				markJobAsComplete(jobName)
				latest := &v1alpha1.Promise{}
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(uPromise), latest)).To(Succeed())
				latest.Status.Message = "Pipeline output"
				Expect(fakeK8sClient.Status().Update(ctx, latest)).To(Succeed())
				_, err = reconcileWorkflow(opts)
				Expect(apierrors.IsConflict(err)).To(BeTrue())
				Expect(listJobs(namespace)).To(HaveLen(1))
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(uPromise), uPromise)).To(Succeed())
				_, err = reconcileWorkflow(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(listJobs(namespace)).To(BeEmpty())
				Expect(resourceutil.GetStatus(uPromise, "message")).To(Equal("Pipeline output"))
			}
			requeue, err := reconcileWorkflow(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(requeue).To(BeFalse())
			Expect(listJobs(namespace)).To(BeEmpty())
			if key != "configure" {
				Expect(uPromise.Object["status"].(map[string]any)["conditions"]).To(Equal(originalStatus["conditions"]))
				configure, _, err := unstructured.NestedMap(uPromise.Object, "status", "kratix", "workflows", "configure")
				Expect(err).NotTo(HaveOccurred())
				originalConfigure, _, err := unstructured.NestedMap(originalStatus, "kratix", "workflows", "configure")
				Expect(err).NotTo(HaveOccurred())
				Expect(configure).To(Equal(originalConfigure))
			}
		},
			Entry("configure", v1alpha1.WorkflowActionConfigure, "configure", "promise"),
			Entry("delete", v1alpha1.WorkflowActionDelete, "delete", "promise"),
			Entry("embedded configure", v1alpha1.WorkflowActionConfigure, "example", "promise-example"),
			Entry("embedded delete", v1alpha1.WorkflowActionDelete, "example-delete", "promise-example"),
		)

		When("list of pipeline resources are empty", func() {
			It("does not panic", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, nil, "promise", 5, namespace)

				Expect(func() {
					passiveRequeue, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(passiveRequeue).To(BeFalse())
				}).NotTo(Panic())
			})
		})

		When("no pipeline for the workflow was executed", func() {
			var opts workflow.Opts
			BeforeEach(func() {
				promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase = v1alpha1.WorkflowPhasePending
				Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())

				opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeTrue())
			})

			Context("on the first reconciliation", func() {
				It("marks the pipeline as running in the kratix status", func() {
					updatedPromise := &v1alpha1.Promise{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, updatedPromise)).To(Succeed())

					Expect(updatedPromise.Status.Kratix.Workflows["configure"].Pipelines).To(HaveLen(2))
					Expect(updatedPromise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal("Running"))
					Expect(updatedPromise.Status.Kratix.Workflows["configure"].Pipelines[1].Phase).To(Equal("Pending"))
				})

				Context("on the second reconciliation", func() {
					BeforeEach(func() {
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())
					})

					It("creates a new job with the first pipeline job spec", func() {
						jobList := listJobs(namespace)
						Expect(jobList).To(HaveLen(1))
						Expect(jobList[0].Name).To(Equal(workflowPipelines[0].Job.Name))
					})

					It("fires an event for the new pipeline", func() {
						Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
							"Normal PipelineStarted Configure Pipeline started: pipeline-1")))
					})
				})
			})
		})

		When("a suspended pipeline is resumed by removing the suspend label", func() {
			It("starts from the suspended pipeline", func() {
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
				promise.Labels = map[string]string{}
				Expect(fakeK8sClient.Update(ctx, &promise)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
				promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase = v1alpha1.WorkflowPhaseSuspended
				promise.Status.Kratix.Workflows["configure"].Pipelines[0].Message = "waiting"
				promise.Status.Kratix.Workflows["configure"].Pipelines[1].Phase = v1alpha1.WorkflowPhasePending
				Expect(fakeK8sClient.Status().Update(ctx, &promise)).To(Succeed())

				completedJob := workflowPipelines[0].Job.DeepCopy()
				completedJob.Status.Conditions = append(completedJob.Status.Conditions, batchv1.JobCondition{
					Type:   batchv1.JobComplete,
					Status: v1.ConditionTrue,
				})
				Expect(fakeK8sClient.Create(ctx, completedJob)).To(Succeed())

				uPromise, err := promise.ToUnstructured()
				Expect(err).NotTo(HaveOccurred())
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)

				By("resuming the suspended pipeline directly to running", func() {
					passiveRequeue, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(passiveRequeue).To(BeTrue())

					updatedPromise := &v1alpha1.Promise{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, updatedPromise)).To(Succeed())
					Expect(updatedPromise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal(v1alpha1.WorkflowPhaseRunning))
					Expect(updatedPromise.Status.Kratix.Workflows["configure"].Pipelines[0].Message).To(BeEmpty())
					Expect(updatedPromise.Status.Kratix.Workflows["configure"].Pipelines[1].Phase).To(Equal(v1alpha1.WorkflowPhasePending))
					jobs := listJobs(namespace)
					Expect(jobs).To(HaveLen(1))
					Expect(findByName(jobs, workflowPipelines[0].Job.GetName())).To(BeTrue())
				})
			})
		})

		When("the status update that resumes a suspended pipeline hits a conflict", func() {
			It("does not start the pipeline a second time on the next reconcile", func() {
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
				promise.Labels = map[string]string{}
				Expect(fakeK8sClient.Update(ctx, &promise)).To(Succeed())
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
				promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase = v1alpha1.WorkflowPhaseSuspended
				promise.Status.Kratix.Workflows["configure"].Pipelines[1].Phase = v1alpha1.WorkflowPhasePending
				Expect(fakeK8sClient.Status().Update(ctx, &promise)).To(Succeed())

				completedJob := workflowPipelines[0].Job.DeepCopy()
				completedJob.Status.Conditions = append(completedJob.Status.Conditions, batchv1.JobCondition{
					Type:   batchv1.JobComplete,
					Status: v1.ConditionTrue,
				})
				Expect(fakeK8sClient.Create(ctx, completedJob)).To(Succeed())

				conflictOnce := true
				conflictingClient := interceptor.NewClient(fakeK8sClient.(client.WithWatch), interceptor.Funcs{
					SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string,
						obj client.Object, opts ...client.SubResourceUpdateOption) error {
						if conflictOnce && obj.GetName() == promise.Name {
							conflictOnce = false
							return apierrors.NewConflict(
								schema.GroupResource{Group: "platform.kratix.io", Resource: "promises"},
								promise.Name, fmt.Errorf("the object has been modified"))
						}
						return c.Status().Update(ctx, obj, opts...)
					},
				})

				uPromise, err := promise.ToUnstructured()
				Expect(err).NotTo(HaveOccurred())

				opts := workflow.NewOpts(ctx, conflictingClient, eventRecorder, logger, uPromise,
					workflowPipelines, "promise", 5, namespace)
				_, err = workflow.ReconcileConfigure(opts)
				Expect(err).To(HaveOccurred())

				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
				uPromise, err = promise.ToUnstructured()
				Expect(err).NotTo(HaveOccurred())
				opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise,
					workflowPipelines, "promise", 5, namespace)
				_, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())

				updatedPromise := &v1alpha1.Promise{}
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, updatedPromise)).To(Succeed())
				Expect(updatedPromise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal(v1alpha1.WorkflowPhaseRunning))
				Expect(countEvents(eventRecorder, "PipelineStarted")).To(Equal(1))
			})
		})

		When("the service account does exist", func() {
			When("the service account does not have the kratix promise label", func() {
				It("should not add the kratix label to the service account", func() {
					Expect(fakeK8sClient.Create(ctx, &v1.ServiceAccount{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "redis-promise-configure-pipeline-1",
							Namespace: namespace,
						},
					})).NotTo(HaveOccurred())

					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					_, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					sa := &v1.ServiceAccount{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis-promise-configure-pipeline-1", Namespace: namespace}, sa)).To(Succeed())
					Expect(sa.GetLabels()).To(BeEmpty())
				})
			})

			When("the service account does have the kratix promise label", func() {
				It("should update the service account", func() {
					Expect(fakeK8sClient.Create(ctx, &v1.ServiceAccount{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "redis-promise-configure-pipeline-1",
							Namespace: namespace,
							Labels: map[string]string{
								"kratix.io/promise-name": "redis",
							},
						},
					})).NotTo(HaveOccurred())

					Expect(workflowPipelines[0].GetObjects()[0]).To(BeAssignableToTypeOf(&v1.ServiceAccount{}))
					workflowPipelines[0].GetObjects()[0].SetLabels(map[string]string{
						"kratix.io/promise-name": "redis",
						"new-labels":             "new-labels",
					})
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					_, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					sa := &v1.ServiceAccount{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis-promise-configure-pipeline-1", Namespace: namespace}, sa)).To(Succeed())
					Expect(sa.GetLabels()).To(HaveKeyWithValue("new-labels", "new-labels"))
				})
			})
		})

		Describe("the creation of pipeline jobs", func() {
			var opts workflow.Opts

			BeforeEach(func() {
				opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
			})

			It("triggers one job after the other until all jobs completes", func() {
				By("correctly updating the resource status", func() {
					requeue := reconcile(opts, &promise)
					Expect(requeue).To(BeTrue())
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal("Running"))
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[1].Phase).To(Equal("Pending"))
				})

				By("creating a new job for the pipeline", func() {
					requeue := reconcile(opts, &promise)
					Expect(requeue).To(BeTrue())
					Expect(listJobs(namespace)).To(HaveLen(1))
				})

				By("not creating a new job if the previous job is not completed", func() {
					requeue := reconcile(opts, &promise)
					Expect(requeue).To(BeTrue())
					Expect(listJobs(namespace)).To(HaveLen(1))
				})

				job := listJobs(namespace)[0]
				markJobAsComplete(job.Name)

				By("updating the status once the job is completed", func() {
					requeue := reconcile(opts, &promise)
					Expect(requeue).To(BeTrue())
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal("Succeeded"))
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].LastTransitionTime).NotTo(BeZero())
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[1].Phase).To(Equal("Pending"))
				})

				By("updating the next pipeline status to", func() {
					requeue := reconcile(opts, &promise)
					Expect(requeue).To(BeTrue())
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal("Succeeded"))
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].LastTransitionTime).NotTo(BeZero())
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[1].Phase).To(Equal("Running"))
				})

				By("creating a job for the next pipeline", func() {
					requeue := reconcile(opts, &promise)
					Expect(requeue).To(BeTrue())
					Expect(listJobs(namespace)).To(HaveLen(2))
				})

				job = listJobs(namespace)[1]
				markJobAsComplete(job.Name)

				By("updating the status once the job is completed", func() {
					requeue := reconcile(opts, &promise)
					Expect(requeue).To(BeTrue())
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal("Succeeded"))
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].LastTransitionTime).NotTo(BeZero())
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[1].Phase).To(Equal("Succeeded"))
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[1].LastTransitionTime).NotTo(BeZero())
				})

				By("not triggering more jobs once all pipelines are completed", func() {
					requeue := reconcile(opts, &promise)
					Expect(requeue).To(BeFalse())
					Expect(listJobs(namespace)).To(HaveLen(2))
				})
			})

			When("there's a job in flight", func() {
				BeforeEach(func() {
					requeue := reconcile(opts, &promise)
					Expect(requeue).To(BeTrue())
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal("Running"))
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].LastTransitionTime).NotTo(BeZero())
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[1].Phase).To(Equal("Pending"))

					Expect(reconcile(opts, &promise)).To(BeTrue())
					Expect(listJobs(namespace)).To(HaveLen(1))
				})

				When("the manual reconciliation label is added", func() {
					It("suspends the current job", func() {
						uPromise.SetLabels(map[string]string{
							"kratix.io/manual-reconciliation": "true",
						})

						By("cancelling the current job", func() {
							requeue := reconcile(opts, &promise)
							Expect(requeue).To(BeTrue())

							jobs := listJobs(namespace)
							Expect(jobs).To(HaveLen(1))
							Expect(*jobs[0].Spec.Suspend).To(BeTrue())
						})

						jobs := listJobs(namespace)
						jobs[0].Status.Conditions = append(jobs[0].Status.Conditions, batchv1.JobCondition{
							Type:   batchv1.JobSuspended,
							Status: v1.ConditionTrue,
						})
						Expect(fakeK8sClient.Status().Update(ctx, &jobs[0])).To(Succeed())
						resetWorkflowPipelineJobs(workflowPipelines)

						By("creating a new job for the pipeline", func() {
							Expect(reconcile(opts, &promise)).To(BeTrue())
							jobs := listJobs(namespace)
							Expect(jobs).To(HaveLen(2))

							Expect(jobs[1].Spec.Suspend).To(BeNil())
							Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal("Running"))
							Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].LastTransitionTime).NotTo(BeZero())
							Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[1].Phase).To(Equal("Pending"))
							Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].LastTransitionTime).NotTo(BeZero())
						})
					})
				})
			})

			When("the running job has failed", func() {
				BeforeEach(func() {
					fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)
					promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase = v1alpha1.WorkflowPhaseRunning
					promise.Status.Kratix.Workflows["configure"].Pipelines[0].Job = workflowPipelines[0].Job.Name
					Expect(fakeK8sClient.Status().Update(ctx, &promise)).To(Succeed())

					uPromise, err := promise.ToUnstructured()
					Expect(err).NotTo(HaveOccurred())
					opts.SetParentObject(uPromise)

					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					markJobAsFailed(workflowPipelines[0].Job.Name)

					Expect(reconcile(opts, &promise)).To(BeTrue())
				})

				It("marks the pipeline as failed", func() {
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal("Failed"))
					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].LastTransitionTime).ToNot(BeZero())
				})

				It("updates the Promise status", func() {
					Expect(promise.Status.Conditions).To(HaveLen(2))
					configureWorkflowCond := apimeta.FindStatusCondition(promise.Status.Conditions, string(resourceutil.ConfigureWorkflowCompletedCondition))
					Expect(configureWorkflowCond.Message).To(Equal("A Configure Pipeline has failed: pipeline-1"))
					Expect(configureWorkflowCond.Reason).To(Equal("ConfigureWorkflowFailed"))
					Expect(string(configureWorkflowCond.Status)).To(Equal("False"))

					reconciledCond := apimeta.FindStatusCondition(promise.Status.Conditions, "Reconciled")
					Expect(reconciledCond.Message).To(Equal("Failing"))
					Expect(reconciledCond.Reason).To(Equal("ConfigureWorkflowFailed"))
					Expect(string(reconciledCond.Status)).To(Equal("False"))
				})

				It("does not create any new Jobs on the next reconciliation", func() {
					previousTransition := promise.Status.Kratix.Workflows["configure"].Pipelines[0].LastTransitionTime
					Expect(reconcile(opts, &promise)).To(BeTrue())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(1))

					Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].LastTransitionTime).To(Equal(previousTransition))
				})

				It("publishes an event", func() {
					Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
						"Warning ConfigureWorkflowFailed A promise/configure Pipeline has failed: pipeline-1")))
				})
			})
		})

		When("there are jobs for this workflow", func() {
			BeforeEach(func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
				_, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				_, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(listJobs(namespace)).To(HaveLen(1))
			})

			Context("and the job has failed", func() {
				var passiveRequeue bool
				var err error

				Context("for a core workflow", func() {
					BeforeEach(func() {
						jobs := listJobs(namespace)
						Expect(jobs).To(HaveLen(1))
						markJobAsFailed(jobs[0].Name)
						newWorkflowPipelines, uPromise := setupTest(promise, pipelines)
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, newWorkflowPipelines, "promise", 5, namespace)
						passiveRequeue, err = workflow.ReconcileConfigure(opts)
					})

					When("the parent is later manually reconciled", func() {
						var newWorkflowPipelines []v1alpha1.PipelineJobResources

						BeforeEach(func() {
							labelPromiseForManualReconciliation("redis")
							newWorkflowPipelines, uPromise = setupTest(promise, pipelines)
							setParentPipelinesSucceeded(uPromise, newWorkflowPipelines, 0)
							opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, newWorkflowPipelines, "promise", 5, namespace)
							passiveRequeue, err = workflow.ReconcileConfigure(opts)
							Expect(passiveRequeue).To(BeTrue())
							Expect(err).NotTo(HaveOccurred())
						})

						It("re-triggers the first pipeline in the workflow", func() {
							Expect(err).NotTo(HaveOccurred())
							jobList := listJobs(namespace)
							Expect(jobList).To(HaveLen(2))
							Expect(findByName(jobList, newWorkflowPipelines[0].Job.GetName())).To(BeTrue())
						})

						It("marks the configure workflow as running again", func() {
							updatedPromise := &v1alpha1.Promise{}
							Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, updatedPromise)).To(Succeed())

							condition := apimeta.FindStatusCondition(updatedPromise.Status.Conditions, string(resourceutil.ConfigureWorkflowCompletedCondition))
							Expect(condition).NotTo(BeNil())
							Expect(condition.Status).To(Equal(metav1.ConditionFalse))
							Expect(condition.Reason).To(Equal(resourceutil.PipelinesInProgressReason))
						})
					})

					When("the reconciliation is triggered and the previously failing job succeeds", func() {
						It("updates the promise status successfully", func() {
							Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
							Expect(promise.Status.Kratix.Workflows["configure"].Pipelines[0].Phase).To(Equal(v1alpha1.WorkflowPhaseFailed))

							// Trigger the workflow via the manual reconciliation, running the pipeline from the start
							labelPromiseForManualReconciliation("redis")
							newWorkflowPipelines, uPromise := setupTest(promise, pipelines)
							setParentPipelinesSucceeded(uPromise, newWorkflowPipelines, 0)
							opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, newWorkflowPipelines, "promise", 5, namespace)
							passiveRequeue, err = workflow.ReconcileConfigure(opts)
							Expect(passiveRequeue).To(BeTrue())
							Expect(err).NotTo(HaveOccurred())

							Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
							Expect(countPromisePipelinesInPhase(&promise, v1alpha1.WorkflowPhaseFailed)).To(Equal(0))

							// Mark the job created by the first pipeline as complete
							markJobAsComplete(newWorkflowPipelines[0].Job.Name)
							passiveRequeue, err = workflow.ReconcileConfigure(opts)
							Expect(passiveRequeue).To(BeTrue())
							Expect(err).NotTo(HaveOccurred())
							Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
							Expect(countPromisePipelinesInPhase(&promise, v1alpha1.WorkflowPhaseSucceeded)).To(Equal(1))
						})
					})
				})

			})
		})

		When("the promise spec is updated", func() {
			var updatedWorkflowPipeline []v1alpha1.PipelineJobResources

			BeforeEach(func() {
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &promise)).To(Succeed())

				promise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{{
					MatchLabels: map[string]string{"app": "redis"},
				}}
				Expect(fakeK8sClient.Update(ctx, &promise)).To(Succeed())

				updatedWorkflowPipeline, uPromise = setupTest(promise, pipelines)
				Expect(updatedWorkflowPipeline[0].Job.Name).NotTo(Equal(workflowPipelines[0].Job.Name))
				Expect(updatedWorkflowPipeline[1].Job.Name).NotTo(Equal(workflowPipelines[1].Job.Name))
			})

			When("there are no jobs for the promise at this spec", func() {
				It("triggers the first pipeline in the workflow", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
					passiveRequeue, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(1))
					Expect(passiveRequeue).To(BeTrue())

					Expect(findByName(jobList, updatedWorkflowPipeline[0].Job.Name)).To(BeTrue())
				})
			})

			When("there are jobs for the promise at this spec", func() {
				var originalWorkflowPipelines []v1alpha1.PipelineJobResources

				BeforeEach(func() {
					// Run the original pipeline jobs to completion, so they exist in the
					// history of jobs
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[1].Job)).To(Succeed())
					markJobAsComplete(workflowPipelines[0].Job.Name)
					markJobAsComplete(workflowPipelines[1].Job.Name)

					// Run the updated-spec jobs to completion, so they're the most recent
					Expect(fakeK8sClient.Create(ctx, updatedWorkflowPipeline[0].Job)).To(Succeed())
					Expect(fakeK8sClient.Create(ctx, updatedWorkflowPipeline[1].Job)).To(Succeed())
					markJobAsComplete(updatedWorkflowPipeline[0].Job.Name)
					markJobAsComplete(updatedWorkflowPipeline[1].Job.Name)

					// Update the promise back to its original spec
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &promise)).To(Succeed())
					promise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{}
					Expect(fakeK8sClient.Update(ctx, &promise)).To(Succeed())

					originalWorkflowPipelines, uPromise = setupTest(promise, pipelines)
				})

				Context("but the most recent job does not match the current promise spec", func() {
					It("re-runs all pipelines in the workflow", func() {
						// Reconcile with the *original* pipelines and promise spec
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, originalWorkflowPipelines, "promise", 5, namespace)
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						// Expect the original 2 jobs, the updated 2 jobs, and the first job
						// from re-running the first pipeline again on this reconciliation
						jobList := listJobs(namespace)
						Expect(jobList).To(HaveLen(5))

						markJobAsComplete(originalWorkflowPipelines[0].Job.Name)
						setParentPipelinesSucceeded(uPromise, originalWorkflowPipelines, 1)

						passiveRequeue, err = workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())
						jobList = listJobs(namespace)
						Expect(jobList).To(HaveLen(6))
					})
				})
			})

			When("there is a job in progress for this promise at a previous spec", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
				})

				It("does not create a new job", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
					passiveRequeue, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(1))
					Expect(passiveRequeue).To(BeTrue())
				})

				It("waits for the previous job to complete without suspending it", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
					_, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())

					job := &batchv1.Job{}
					Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: workflowPipelines[0].Job.Name}, job)).To(Succeed())

					Expect(job.Spec.Suspend).To(BeNil())
				})

				When("the outdated job completes", func() {
					var jobList []batchv1.Job

					BeforeEach(func() {
						markJobAsComplete(workflowPipelines[0].Job.Name)
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						jobList = listJobs(namespace)
						Expect(passiveRequeue).To(BeTrue())
						Expect(findByName(jobList, workflowPipelines[0].Job.Name)).To(BeTrue())
					})

					It("triggers the first pipeline in the workflow at the new spec", func() {
						Expect(findByName(jobList, updatedWorkflowPipeline[0].Job.Name)).To(BeTrue())
					})

					It("never triggers the next job in the outdated workflow", func() {
						Expect(findByName(jobList, workflowPipelines[1].Job.Name)).To(BeFalse())
					})
				})

				When("manual reconciliation is also requested", func() {
					It("suspends the running job", func() {
						uPromise.SetLabels(map[string]string{
							"kratix.io/manual-reconciliation": "true",
						})
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						job := &batchv1.Job{}
						Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: workflowPipelines[0].Job.Name}, job)).To(Succeed())
						Expect(job.Spec.Suspend).NotTo(BeNil())
						Expect(*job.Spec.Suspend).To(BeTrue())
					})
				})

				When("the workflow-run-from-start label is also set", func() {
					var runningJobName string

					BeforeEach(func() {
						runningJobName = workflowPipelines[0].Job.Name
						labelPromiseWithWorkflowRestart("redis")
						workflowPipelines, uPromise = setupTest(promise, pipelines)
					})

					It("waits for the running job to complete", func() {
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						By("not creating any new jobs")
						Expect(listJobs(namespace)).To(HaveLen(1))

						By("not suspending the running job")
						job := &batchv1.Job{}
						Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: runningJobName}, job)).To(Succeed())
						Expect(job.Spec.Suspend).To(BeNil())
					})

					It("restarts from the first pipeline once the running job completes", func() {
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
						_, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())

						markJobAsComplete(runningJobName)

						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						jobList := listJobs(namespace)
						Expect(jobList).To(HaveLen(2))
						Expect(findByName(jobList, updatedWorkflowPipeline[0].Job.Name)).To(BeTrue())
					})
				})
			})
		})

		Context("promise workflows", func() {
			var opts workflow.Opts
			BeforeEach(func() {
				opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)

				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeTrue())

				markJobAsComplete(workflowPipelines[0].Job.Name)
				setParentPipelinesSucceeded(uPromise, workflowPipelines, 1)

				passiveRequeue, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeTrue())
			})

			When("there are still pipelines to execute", func() {
				It("doesn't delete the promise scheduling config map", func() {
					configMap := &v1.ConfigMap{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
						Name: "destination-selectors-redis", Namespace: namespace},
						configMap,
					)).To(Succeed())
				})
			})
		})

		Context("resource workflow", func() {
			var resource *unstructured.Unstructured
			var opts workflow.Opts

			BeforeEach(func() {
				resource = &unstructured.Unstructured{}
				resource.SetName("resource-2")
				resource.SetNamespace(namespace)
				resource.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   fakeCRD.Spec.Group,
					Version: fakeCRD.Spec.Versions[0].Name,
					Kind:    fakeCRD.Spec.Names.Kind,
				})

				Expect(fakeK8sClient.Create(ctx, resource)).To(Succeed())

				opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, resource, workflowPipelines, "resource", 5, namespace)
				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeTrue())
			})

			It("sets the status of the resource when the pipelines are still in progress", func() {
				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeTrue())

				updatedResource := &unstructured.Unstructured{}
				updatedResource.SetGroupVersionKind(resource.GroupVersionKind())
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(resource), updatedResource)).To(Succeed())
				Expect(resourceutil.CountPipelinesInPhase(updatedResource, v1alpha1.WorkflowPhaseSucceeded)).To(Equal(int64(0)))
				Expect(resourceutil.CountPipelinesInPhase(updatedResource, v1alpha1.WorkflowPhaseFailed)).To(Equal(int64(0)))

				//second reconciliation creates the pipeline and updates the status to pending with the correct conditions
				passiveRequeue, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeTrue())

				updatedResource = &unstructured.Unstructured{}
				updatedResource.SetGroupVersionKind(resource.GroupVersionKind())
				Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(resource), updatedResource)).To(Succeed())

				Expect(updatedResource.Object["status"]).To(SatisfyAll(
					HaveKeyWithValue("message", "Pending"),
					HaveKeyWithValue("conditions", Not(BeNil())),
				))

				conditions, found, err := unstructured.NestedSlice(updatedResource.Object, "status", "conditions")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				Expect(conditions[0]).To(SatisfyAll(
					HaveKeyWithValue("type", "ConfigureWorkflowCompleted"),
					HaveKeyWithValue("status", "False"),
					HaveKeyWithValue("message", "Pipelines are still in progress"),
					HaveKeyWithValue("reason", resourceutil.PipelinesInProgressReason),
					HaveKeyWithValue("lastTransitionTime", Not(BeEmpty())),
				))
			})

			It("fires an event for the new pipeline", func() {
				// The status update will trigger a reconciliation in a real cluster.
				// Simulate that here by calling ReconcileConfigure again.
				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeTrue())

				Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
					"Normal PipelineStarted Configure Pipeline started: pipeline-1")))
			})

			When("a suspended resource pipeline is resumed", func() {
				It("resets status.message and Reconciled condition", func() {
					updatedResource := &unstructured.Unstructured{}
					updatedResource.SetGroupVersionKind(resource.GroupVersionKind())
					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(resource), updatedResource)).To(Succeed())

					resourceutil.MarkConfigureWorkflowAsRunning(logger, updatedResource)
					resourceutil.MarkReconciledSuspended(updatedResource)
					resourceutil.SetStatus(updatedResource, logger, "message", "To-be-updated")
					Expect(fakeK8sClient.Status().Update(ctx, updatedResource)).To(Succeed())

					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(resource), updatedResource)).To(Succeed())
					opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, updatedResource, workflowPipelines, "resource", 5, namespace)
					passiveRequeue, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(passiveRequeue).To(BeTrue())

					Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(resource), updatedResource)).To(Succeed())
					Expect(resourceutil.GetStatus(updatedResource, "message")).To(Equal("Pending"))
					reconciled := resourceutil.GetCondition(updatedResource, resourceutil.ReconciledCondition)
					Expect(reconciled).NotTo(BeNil())
					Expect(reconciled.Message).To(Equal("Pending"))
					Expect(reconciled.Reason).To(Equal("WorkflowPending"))
				})
			})
		})

		When("the number of old workflows exceeds the number of jobs to keep ", func() {
			It("deletes the oldest jobs", func() {
				var updatedPromise v1alpha1.Promise
				numberOfJobLimit := 7
				for i := range numberOfJobLimit + 1 {
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &updatedPromise)).To(Succeed())
					updatedPromise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{{
						MatchLabels: map[string]string{"app": fmt.Sprintf("redis-%d", i)},
					}}
					Expect(fakeK8sClient.Update(ctx, &updatedPromise)).To(Succeed())

					updatedWorkflowPipeline, uPromise := setupTest(updatedPromise, pipelines)
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", numberOfJobLimit, namespace)
					for j := range 2 {
						setParentPipelinesSucceeded(uPromise, updatedWorkflowPipeline, j)
						_, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						markJobAsComplete(updatedWorkflowPipeline[j].Job.Name)
					}
					setParentPipelinesSucceeded(uPromise, updatedWorkflowPipeline, 2)
				}
				updatedWorkflowPipeline, uPromise := setupTest(updatedPromise, pipelines)
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", numberOfJobLimit, namespace)
				_, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())

				jobList := listJobs(namespace)
				Expect(jobList).To(HaveLen(numberOfJobLimit * len(updatedWorkflowPipeline)))
			})
		})

		When("a pipeline keeps failing", func() {
			numberOfJobsToKeep := 2

			// Runs a pipeline, fails it, and reconciles again so the failure is
			// observed; mirrors what the periodic reconcile does once it re-runs a
			// workflow whose previous run failed. Returns the failed job's name.
			failPipelineRun := func() string {
				GinkgoHelper()

				labelPromiseForManualReconciliation(promise.Name)
				newWorkflowPipelines, uPromise := setupTest(promise, pipelines)
				setParentPipelinesSucceeded(uPromise, newWorkflowPipelines, 0)
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise,
					newWorkflowPipelines, "promise", numberOfJobsToKeep, namespace)

				_, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())

				jobName := newWorkflowPipelines[0].Job.GetName()
				markJobAsFailed(jobName)

				_, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())

				return jobName
			}

			It("deletes the oldest failed jobs", func() {
				var mostRecentJobName string
				for range numberOfJobsToKeep + 2 {
					mostRecentJobName = failPipelineRun()
				}

				jobList := listJobs(namespace)
				Expect(jobList).To(HaveLen(numberOfJobsToKeep))
				Expect(findByName(jobList, mostRecentJobName)).To(BeTrue())
			})

			It("does not delete works belonging to pipelines that no longer exist", func() {
				createFakeWorks([]v1alpha1.Pipeline{{
					ObjectMeta: metav1.ObjectMeta{Name: "removed-pipeline"},
				}}, promise.Name)

				failPipelineRun()

				works := v1alpha1.WorkList{}
				Expect(fakeK8sClient.List(ctx, &works)).To(Succeed())
				Expect(works.Items).To(HaveLen(1))
			})
		})

		When("the last pipeline never finishes and the workflow keeps restarting", func() {
			numberOfJobsToKeep := 2
			restartsOverTheLimit := numberOfJobsToKeep + 2

			runWorkflowLeavingTheLastPipelineRunning := func() {
				GinkgoHelper()

				workflowPipelines, uPromise := setupTest(promise, pipelines)
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise,
					workflowPipelines, "promise", numberOfJobsToKeep, namespace)

				By("running the first pipeline to success", func() {
					reconcileConfigure(opts)
					markJobAsComplete(workflowPipelines[0].Job.GetName())
					setParentPipelinesSucceeded(uPromise, workflowPipelines, 1)
				})

				By("starting the last pipeline, which never finishes", func() {
					reconcileConfigure(opts)
				})
			}

			suspendTheRunningPipeline := func() {
				GinkgoHelper()

				labelPromiseForManualReconciliation(promise.Name)
				workflowPipelines, uPromise := setupTest(promise, pipelines)
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise,
					workflowPipelines, "promise", numberOfJobsToKeep, namespace)

				By("suspending the job that is still running", func() {
					reconcileConfigure(opts)
					markJobsAsSuspended()
				})
			}

			It("keeps no more than numberOfJobsToKeep jobs per pipeline", func() {
				runWorkflowLeavingTheLastPipelineRunning()

				for range restartsOverTheLimit {
					suspendTheRunningPipeline()
					runWorkflowLeavingTheLastPipelineRunning()
				}

				Expect(jobNamesForPipeline("pipeline-1")).To(HaveLen(numberOfJobsToKeep))
				Expect(jobNamesForPipeline("pipeline-2")).To(HaveLen(numberOfJobsToKeep))
			})
		})

		When("a pipeline no longer exists in the workflow", func() {
			numberOfJobsToKeep := 2

			It("deletes the oldest jobs left behind by that pipeline", func() {
				removedPipeline := v1alpha1.Pipeline{
					ObjectMeta: metav1.ObjectMeta{Name: "removed-pipeline"},
					Spec: v1alpha1.PipelineSpec{
						Containers: []v1alpha1.Container{{Name: "container-1", Image: "busybox"}},
					},
				}

				for i := range numberOfJobsToKeep + 2 {
					resources, err := removedPipeline.ForPromise(&promise, v1alpha1.WorkflowActionConfigure).Resources(nil)
					Expect(err).NotTo(HaveOccurred())
					resources.Job.SetName(fmt.Sprintf("removed-pipeline-job-%d", i))
					resources.Job.SetCreationTimestamp(nextTimestamp())
					Expect(fakeK8sClient.Create(ctx, resources.Job)).To(Succeed())
					markJobAsComplete(resources.Job.GetName())
				}

				newWorkflowPipelines, uPromise := setupTest(promise, pipelines)
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise,
					newWorkflowPipelines, "promise", numberOfJobsToKeep, namespace)

				_, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())

				Expect(jobNamesForPipeline("removed-pipeline")).To(ConsistOf(
					"removed-pipeline-job-2", "removed-pipeline-job-3"))
			})
		})

		When("all pipelines have executed", func() {
			var updatedWorkflows []v1alpha1.PipelineJobResources

			BeforeEach(func() {
				Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
				Expect(fakeK8sClient.Create(ctx, workflowPipelines[1].Job)).To(Succeed())
				markJobAsComplete(workflowPipelines[0].Job.Name)
				markJobAsComplete(workflowPipelines[1].Job.Name)

				createFakeWorks(pipelines, promise.Name)

				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
				setParentPipelinesSucceeded(uPromise, workflowPipelines, 2)
				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeFalse())

				updatedPipelines := []v1alpha1.Pipeline{{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pipeline-new-name",
					},
					Spec: v1alpha1.PipelineSpec{
						Containers: []v1alpha1.Container{
							{Name: "container-1", Image: "busybox"},
						},
					},
				}}

				updatedWorkflows, uPromise = setupTest(promise, updatedPipelines)
				Expect(fakeK8sClient.Create(ctx, updatedWorkflows[0].Job)).To(Succeed())
				markJobAsComplete(updatedWorkflows[0].Job.Name)

				opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflows, "promise", 5, namespace)
				setParentPipelinesSucceeded(uPromise, updatedWorkflows, 1)
				passiveRequeue, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeFalse())

				createFakeWorks(updatedPipelines, promise.Name)
				createFakeWorks(pipelines, "not-redis")
				createStaticDependencyWork(promise.Name)
			})

			It("cleans up any leftover works from previous runs", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflows, "promise", 5, namespace)
				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeFalse())

				works := v1alpha1.WorkList{}
				Expect(fakeK8sClient.List(ctx, &works)).To(Succeed())

				ids := []string{}
				for _, work := range works.Items {
					ids = append(ids,
						fmt.Sprintf(
							"%s/%s/%s",
							work.GetLabels()["kratix.io/work-type"],
							work.GetLabels()["kratix.io/promise-name"],
							work.GetLabels()["kratix.io/pipeline-name"],
						),
					)
				}
				Expect(ids).To(ConsistOf([]string{
					"promise/redis/pipeline-new-name",
					"promise/not-redis/pipeline-2",
					"promise/not-redis/pipeline-1",
					"static-dependency/redis/",
				}))
			})
		})

		When("the manual reconciliation label exists in the parent resource", func() {
			When("there are no jobs in progress", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[1].Job)).To(Succeed())
					markJobAsComplete(workflowPipelines[0].Job.Name)
					markJobAsComplete(workflowPipelines[1].Job.Name)
					setParentPipelinesSucceeded(uPromise, workflowPipelines, 2)

					Expect(listJobs(namespace)).To(HaveLen(2))

					labelPromiseForManualReconciliation("redis")

					workflowPipelines, uPromise = setupTest(promise, pipelines)
					setParentPipelinesSucceeded(uPromise, workflowPipelines, 1)
				})

				It("re-triggers all the pipelines in the workflow", func() {
					var jobs []batchv1.Job
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					By("re-triggering the first pipeline on the next reconciliation", func() {
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())
						assertPromisePipelinesSucceeded("redis", 0)
						passiveRequeue, err = workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(3))
						Expect(jobs[0].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[0].Name))
						Expect(jobs[1].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[1].Name))
						Expect(jobs[2].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[0].Name))
					})

					By("firing an event for the re-triggered pipeline", func() {
						Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
							"Normal PipelineStarted Configure Pipeline started: pipeline-1")))
					})

					By("removing the label from the parent after the first reconciliation", func() {
						promise := v1alpha1.Promise{}
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &promise)).To(Succeed())
						Expect(promise.GetLabels()).NotTo(HaveKey("kratix.io/manual-reconciliation"))
					})

					By("handling cases where label gets added mid-flight on the first pipeline", func() {
						labelPromiseForManualReconciliation("redis")

						workflowPipelines, uPromise = setupTest(promise, pipelines)
						opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					})

					By("waiting for the first pipeline to complete", func() {
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(3))
					})

					markJobAsComplete(jobs[2].Name)

					By("removing the manual reconciliation label again", func() {
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						promise := v1alpha1.Promise{}
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &promise)).To(Succeed())
						Expect(promise.GetLabels()).NotTo(HaveKey("kratix.io/manual-reconciliation"))
					})

					By("restarting from pipeline 0, respecting the label added mid-flight", func() {
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(4))
						Expect(jobs[3].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[0].Name))
					})

					By("waiting for the first pipeline to complete again", func() {
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(4))
					})

					markJobAsComplete(jobs[3].Name)
					By("triggering the second pipeline when the previous completes", func() {
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())
						assertPromisePipelinesSucceeded("redis", 1)
						passiveRequeue, err = workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(5))
						Expect(jobs[4].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[1].Name))
					})

					By("waiting for the second pipeline to complete", func() {
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(5))
					})

					By("marking it all as completed once the last pipeline completes", func() {
						markJobAsComplete(jobs[4].Name)
						passiveRequeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())
						assertPromisePipelinesSucceeded("redis", 2)

						passiveRequeue, err = workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeFalse())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(5))
					})
				})
			})

			When("there is a job in progress", func() {
				var passiveRequeue bool

				BeforeEach(func() {
					var err error

					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					markJobAsComplete(workflowPipelines[0].Job.Name)

					// Create the second job, but don't mark it as complete
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[1].Job)).To(Succeed())

					Expect(listJobs(namespace)).To(HaveLen(2))

					labelPromiseForManualReconciliation("redis")

					workflowPipelines, uPromise = setupTest(promise, pipelines)

					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)

					passiveRequeue, err = workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
				})

				It("suspends the current job", func() {
					jobs := resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
					Expect(jobs).To(HaveLen(2))
					Expect(jobs[0].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[0].Name))
					Expect(jobs[1].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[1].Name))
					Expect(jobs[1].Spec.Suspend).NotTo(BeNil())
					Expect(*jobs[1].Spec.Suspend).To(BeTrue())
				})

				It("aborts the reconciliation loop", func() {
					Expect(passiveRequeue).To(BeTrue())
				})

				It("does not remove the manual reconciliation label from the parent", func() {
					promise := v1alpha1.Promise{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &promise)).To(Succeed())
					Expect(promise.GetLabels()).To(HaveKey("kratix.io/manual-reconciliation"))
				})
			})
		})

		When("the workflow restart label exists", func() {
			BeforeEach(func() {
				Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
				Expect(fakeK8sClient.Create(ctx, workflowPipelines[1].Job)).To(Succeed())
				markJobAsComplete(workflowPipelines[0].Job.Name)
				markJobAsComplete(workflowPipelines[1].Job.Name)

				setParentPipelinesSucceeded(uPromise, workflowPipelines, 2)

				labelPromiseWithWorkflowRestart("redis")
				workflowPipelines, uPromise = setupTest(promise, pipelines)
				setParentPipelinesSucceeded(uPromise, workflowPipelines, 2)
			})

			It("restarts the workflow from the first pipeline and removes the label", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)

				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeTrue())
				assertPromisePipelinesSucceeded("redis", 0)

				updatedPromise := v1alpha1.Promise{}
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &updatedPromise)).To(Succeed())
				Expect(updatedPromise.GetLabels()).NotTo(HaveKey(resourceutil.WorkflowRunFromStartLabel))

				jobs := resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
				Expect(jobs).To(HaveLen(3))
				Expect(jobs[2].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[0].Name))
			})
		})
	})

	Describe("ReconcileConfigure with user-configured permissions", func() {
		var initialRole *rbacv1.Role
		var initialRoleBindings *rbacv1.RoleBindingList
		var pipelineNamespaceRoleBinding *rbacv1.RoleBinding
		var specificNamespaceRoleBinding *rbacv1.RoleBinding
		var initialClusterRoles *rbacv1.ClusterRoleList
		var initialClusterRoleBindings *rbacv1.ClusterRoleBindingList
		var specificNamespaceClusterRole *rbacv1.ClusterRole
		var allNamespaceClusterRole *rbacv1.ClusterRole

		BeforeEach(func() {
			pipelines[0].Spec.RBAC.Permissions = []v1alpha1.Permission{
				{
					PolicyRule: rbacv1.PolicyRule{
						Verbs:     []string{"list"},
						APIGroups: []string{"v5"},
						Resources: []string{"configmaps"},
					},
				},
				{
					ResourceNamespace: "specific-namespace",
					PolicyRule: rbacv1.PolicyRule{
						Verbs:     []string{"list"},
						APIGroups: []string{"v7"},
						Resources: []string{"secrets"},
					},
				},
				{
					ResourceNamespace: "*",
					PolicyRule: rbacv1.PolicyRule{
						Verbs:     []string{"list"},
						APIGroups: []string{"v2"},
						Resources: []string{"pods"},
					},
				},
			}

			_, uPromise = setupAndReconcileUntilPipelinesCompleted(promise, pipelines, eventRecorder)

			//Collect resources for later validation
			roles := &rbacv1.RoleList{}
			Expect(fakeK8sClient.List(ctx, roles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

			Expect(roles.Items).To(HaveLen(1))
			initialRole = &roles.Items[0]

			initialClusterRoles = &rbacv1.ClusterRoleList{}
			Expect(fakeK8sClient.List(ctx, initialClusterRoles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

			for _, cr := range initialClusterRoles.Items {
				if ns, ok := cr.Labels[v1alpha1.UserPermissionResourceNamespaceLabel]; ok && ns == "specific-namespace" {
					specificNamespaceClusterRole = &cr
				} else if ns == "kratix_all_namespaces" {
					allNamespaceClusterRole = &cr
				}
			}

			initialRoleBindings = &rbacv1.RoleBindingList{}
			Expect(fakeK8sClient.List(ctx, initialRoleBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

			for _, rb := range initialRoleBindings.Items {
				if rb.Namespace == "specific-namespace" {
					specificNamespaceRoleBinding = &rb
				} else {
					pipelineNamespaceRoleBinding = &rb
				}
			}

			initialClusterRoleBindings = &rbacv1.ClusterRoleBindingList{}
			Expect(fakeK8sClient.List(ctx, initialClusterRoleBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())
		})

		When("the pipeline is reconciled", func() {
			It("creates the rbac resources", func() {
				By("creating the role in the pipeline namespace")
				Expect(initialRole.Rules).To(ConsistOf(
					rbacv1.PolicyRule{
						Verbs:     []string{"list"},
						APIGroups: []string{"v5"},
						Resources: []string{"configmaps"},
					},
				))
				Expect(initialRole.GetNamespace()).To(Equal(namespace))
				Expect(initialRole.GetName()).To(MatchRegexp(`^redis-promise-configure-pipeline-1-\b\w{5}\b$`))

				By("creating a cluster role for each set of specific- and all-namespace permissions")
				Expect(initialClusterRoles.Items).To(HaveLen(2))

				Expect(initialClusterRoles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v7"),
								"Resources": ConsistOf("secrets"),
							}),
						),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": MatchRegexp(`^redis-promise-configure-pipeline-1-specific-namespace-\b\w{5}\b$`),
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v2"),
								"Resources": ConsistOf("pods"),
							}),
						),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": MatchRegexp(`^redis-promise-configure-pipeline-1-kratix-all-namespaces-\b\w{5}\b$`),
						}),
					}),
				))

				By("creating the role binding for the pipeline- and specific-namespace permissions")
				Expect(initialRoleBindings.Items).To(HaveLen(2))

				Expect(initialRoleBindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(specificNamespaceRoleBinding.GetName()),
							"Namespace": Equal("specific-namespace"),
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialRole.GetName()),
							"Kind": Equal("Role"),
						}),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(pipelineNamespaceRoleBinding.GetName()),
							"Namespace": Equal(namespace),
						}),
					}),
				))

				By("creating the cluster role binding for all-namespace permissions")
				Expect(initialClusterRoleBindings.Items).To(HaveLen(1))
				clusterRoleBinding := &initialClusterRoleBindings.Items[0]

				Expect(clusterRoleBinding.GetName()).To(MatchRegexp(`^redis-promise-configure-pipeline-1-kratix-platform-system-\b\w{5}\b$`))
				Expect(clusterRoleBinding.RoleRef.Name).To(Equal(allNamespaceClusterRole.GetName()))
				Expect(clusterRoleBinding.RoleRef.Kind).To(Equal("ClusterRole"))
				Expect(clusterRoleBinding.Subjects).To(HaveLen(1))
				Expect(clusterRoleBinding.Subjects[0].Kind).To(Equal("ServiceAccount"))
				Expect(clusterRoleBinding.Subjects[0].Name).To(Equal("redis-promise-configure-pipeline-1"))
				Expect(clusterRoleBinding.Subjects[0].Namespace).To(Equal(namespace))
			})
		})

		When("the pipeline is re-reconciled with the same user-configured permissions", func() {
			BeforeEach(func() {
				forceManualReconciliation(promise, pipelines, eventRecorder)
			})

			It("retains the role", func() {
				roles := &rbacv1.RoleList{}
				Expect(fakeK8sClient.List(ctx, roles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(roles.Items).To(HaveLen(1))
				Expect(roles.Items[0].GetName()).To(Equal(initialRole.GetName()))
				Expect(roles.Items[0].GetNamespace()).To(Equal(initialRole.GetNamespace()))
				Expect(roles.Items[0].Rules).To(HaveLen(1))
				Expect(roles.Items[0].Rules).To(ConsistOf(
					rbacv1.PolicyRule{
						Verbs:     []string{"list"},
						APIGroups: []string{"v5"},
						Resources: []string{"configmaps"},
					},
				))
			})

			It("retains the cluster roles", func() {
				clusterRoles := &rbacv1.ClusterRoleList{}
				Expect(fakeK8sClient.List(ctx, clusterRoles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoles.Items).To(HaveLen(2))
				Expect(clusterRoles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v7"),
								"Resources": ConsistOf("secrets"),
							}),
						),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(allNamespaceClusterRole.GetName()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v2"),
								"Resources": ConsistOf("pods"),
							}),
						),
					}),
				))
			})

			It("retains the role bindings", func() {
				roleBindings := &rbacv1.RoleBindingList{}
				Expect(fakeK8sClient.List(ctx, roleBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(roleBindings.Items).To(HaveLen(2))
				Expect(roleBindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(specificNamespaceRoleBinding.GetName()),
							"Namespace": Equal("specific-namespace"),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(pipelineNamespaceRoleBinding.GetName()),
							"Namespace": Equal(namespace),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialRole.GetName()),
							"Kind": Equal("Role"),
						}),
					}),
				))
			})

			It("retains the cluster role binding", func() {
				clusterRoleBindings := &rbacv1.ClusterRoleBindingList{}
				Expect(fakeK8sClient.List(ctx, clusterRoleBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoleBindings.Items).To(HaveLen(1))
				Expect(clusterRoleBindings.Items[0].GetName()).To(Equal(initialClusterRoleBindings.Items[0].GetName()))
				Expect(clusterRoleBindings.Items[0].RoleRef.Name).To(Equal(allNamespaceClusterRole.GetName()))
				Expect(clusterRoleBindings.Items[0].RoleRef.Kind).To(Equal("ClusterRole"))
				Expect(clusterRoleBindings.Items[0].Subjects).To(HaveLen(1))
				Expect(clusterRoleBindings.Items[0].Subjects[0].Kind).To(Equal("ServiceAccount"))
				Expect(clusterRoleBindings.Items[0].Subjects[0].Name).To(Equal("redis-promise-configure-pipeline-1"))
				Expect(clusterRoleBindings.Items[0].Subjects[0].Namespace).To(Equal(namespace))
			})
		})

		When("all pipeline permissions are removed", func() {
			BeforeEach(func() {
				pipelines[0].Spec.RBAC.Permissions = nil
				forceManualReconciliation(promise, pipelines, eventRecorder)
			})

			It("removes the outdated rbac resources", func() {
				roles := &rbacv1.RoleList{}
				Expect(fakeK8sClient.List(ctx, roles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(roles.Items).To(BeEmpty())

				clusterRoles := &rbacv1.ClusterRoleList{}
				Expect(fakeK8sClient.List(ctx, clusterRoles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoles.Items).To(BeEmpty())

				roleBindings := &rbacv1.RoleBindingList{}
				Expect(fakeK8sClient.List(ctx, roleBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(roleBindings.Items).To(BeEmpty())

				clusterRoleBindings := &rbacv1.ClusterRoleBindingList{}
				Expect(fakeK8sClient.List(ctx, clusterRoleBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoleBindings.Items).To(BeEmpty())
			})
		})

		When("deleting pipeline scoped permissions", func() {
			BeforeEach(func() {
				pipelines[0].Spec.RBAC.Permissions = []v1alpha1.Permission{
					// Deleted the pipeline scoped permission: list v5 configmaps
					{
						ResourceNamespace: "specific-namespace",
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v7"},
							Resources: []string{"secrets"},
						},
					},
					{
						ResourceNamespace: "*",
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v2"},
							Resources: []string{"pods"},
						},
					},
				}
				forceManualReconciliation(promise, pipelines, eventRecorder)
			})

			It("removes the user provided pipeline scoped role", func() {
				roles := &rbacv1.RoleList{}
				Expect(fakeK8sClient.List(ctx, roles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(roles.Items).To(BeEmpty())
			})

			It("removes the user provided pipeline scoped role binding and retains the role binding for the specific namespace", func() {
				bindings := &rbacv1.RoleBindingList{}
				Expect(fakeK8sClient.List(ctx, bindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(bindings.Items).To(HaveLen(1))
				Expect(bindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(specificNamespaceRoleBinding.GetName()),
							"Namespace": Equal("specific-namespace"),
						}),
					}),
				))
			})

			It("retains the cluster role for the all- and specific- namespace", func() {
				clusterRoles := &rbacv1.ClusterRoleList{}
				Expect(fakeK8sClient.List(ctx, clusterRoles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoles.Items).To(HaveLen(2))
				Expect(clusterRoles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v7"),
								"Resources": ConsistOf("secrets"),
							}),
						),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(allNamespaceClusterRole.GetName()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v2"),
								"Resources": ConsistOf("pods"),
							}),
						),
					}),
				))

			})

			It("retains the cluster role binding for the all-namespace", func() {
				clusterRoleBindings := &rbacv1.ClusterRoleBindingList{}
				Expect(fakeK8sClient.List(ctx, clusterRoleBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoleBindings.Items).To(HaveLen(1))
				Expect(clusterRoleBindings.Items[0].GetName()).To(Equal(initialClusterRoleBindings.Items[0].GetName()))
				Expect(clusterRoleBindings.Items[0].RoleRef.Name).To(Equal(allNamespaceClusterRole.GetName()))
				Expect(clusterRoleBindings.Items[0].RoleRef.Kind).To(Equal("ClusterRole"))
				Expect(clusterRoleBindings.Items[0].Subjects).To(HaveLen(1))
				Expect(clusterRoleBindings.Items[0].Subjects[0].Kind).To(Equal("ServiceAccount"))
				Expect(clusterRoleBindings.Items[0].Subjects[0].Name).To(Equal("redis-promise-configure-pipeline-1"))
				Expect(clusterRoleBindings.Items[0].Subjects[0].Namespace).To(Equal(namespace))
			})
		})

		When("adding a specific-namespace scoped permission", func() {
			BeforeEach(func() {
				namespaceScopedPermission := v1alpha1.Permission{
					ResourceNamespace: "specific-namespace",
					PolicyRule: rbacv1.PolicyRule{
						Verbs:     []string{"list"},
						APIGroups: []string{"v1"},
						Resources: []string{"jobs"},
					},
				}
				pipelines[0].Spec.RBAC.Permissions = append(pipelines[0].Spec.RBAC.Permissions, namespaceScopedPermission)
				forceManualReconciliation(promise, pipelines, eventRecorder)
			})

			It("adds the new permission while retaining the existing permissions", func() {
				By("retaining the pipeline scoped permission role")
				roles := &rbacv1.RoleList{}
				Expect(fakeK8sClient.List(ctx, roles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(roles.Items).To(HaveLen(1))
				Expect(roles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(initialRole.GetName()),
							"Namespace": Equal(initialRole.GetNamespace()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v5"),
								"Resources": ConsistOf("configmaps"),
							}),
						),
					}),
				))

				By("retaining the role bindings for the pipeline scoped role and namespace scoped role")
				bindings := &rbacv1.RoleBindingList{}
				Expect(fakeK8sClient.List(ctx, bindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(bindings.Items).To(HaveLen(2))
				Expect(bindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(pipelineNamespaceRoleBinding.GetName()),
							"Namespace": Equal(namespace),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialRole.GetName()),
							"Kind": Equal("Role"),
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(specificNamespaceRoleBinding.GetName()),
							"Namespace": Equal("specific-namespace"),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
					}),
				))

				By("extending the namespace specific cluster role's rules to include the new permission")
				clusterRoles := &rbacv1.ClusterRoleList{}
				Expect(fakeK8sClient.List(ctx, clusterRoles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoles.Items).To(HaveLen(2))
				Expect(clusterRoles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v7"),
								"Resources": ConsistOf("secrets"),
							}),
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v1"),
								"Resources": ConsistOf("jobs"),
							}),
						),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(allNamespaceClusterRole.GetName()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v2"),
								"Resources": ConsistOf("pods"),
							}),
						),
					}),
				))

				By("retaining the all namespace cluster role binding")
				clusterBindings := &rbacv1.ClusterRoleBindingList{}
				Expect(fakeK8sClient.List(ctx, clusterBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterBindings.Items).To(HaveLen(1))
				Expect(clusterBindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialClusterRoleBindings.Items[0].GetName()),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(allNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
						"Subjects": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Kind":      Equal("ServiceAccount"),
								"Name":      Equal("redis-promise-configure-pipeline-1"),
								"Namespace": Equal(namespace),
							}),
						),
					}),
				))
			})
		})

		When("adding a pipeline-scoped permission", func() {
			BeforeEach(func() {
				pipelineScopedPermission := v1alpha1.Permission{
					PolicyRule: rbacv1.PolicyRule{
						Verbs:     []string{"list"},
						APIGroups: []string{"v1"},
						Resources: []string{"jobs"},
					},
				}
				pipelines[0].Spec.RBAC.Permissions = append(pipelines[0].Spec.RBAC.Permissions, pipelineScopedPermission)
				forceManualReconciliation(promise, pipelines, eventRecorder)
			})

			It("adds the new permission while retaining the existing permissions", func() {
				By("extending the pipeline scoped role's rules to include the new permission")
				roles := &rbacv1.RoleList{}
				Expect(fakeK8sClient.List(ctx, roles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(roles.Items).To(HaveLen(1))
				Expect(roles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v5"),
								"Resources": ConsistOf("configmaps"),
							}),
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v1"),
								"Resources": ConsistOf("jobs"),
							}),
						),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(initialRole.GetName()),
							"Namespace": Equal(initialRole.GetNamespace()),
						}),
					}),
				))

				By("retaining the role bindings for the pipeline scoped role and namespace scoped role")
				bindings := &rbacv1.RoleBindingList{}
				Expect(fakeK8sClient.List(ctx, bindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(bindings.Items).To(HaveLen(2))
				Expect(bindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(pipelineNamespaceRoleBinding.GetName()),
							"Namespace": Equal(namespace),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialRole.GetName()),
							"Kind": Equal("Role"),
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(specificNamespaceRoleBinding.GetName()),
							"Namespace": Equal("specific-namespace"),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
					}),
				))

				By("retaining the cluster roles for the all- and specific- namespaces")
				clusterRoles := &rbacv1.ClusterRoleList{}
				Expect(fakeK8sClient.List(ctx, clusterRoles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoles.Items).To(HaveLen(2))
				Expect(clusterRoles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v7"),
								"Resources": ConsistOf("secrets"),
							}),
						),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(allNamespaceClusterRole.GetName()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v2"),
								"Resources": ConsistOf("pods"),
							}),
						),
					}),
				))

				By("retaining the all namespace cluster role binding")
				clusterBindings := &rbacv1.ClusterRoleBindingList{}
				Expect(fakeK8sClient.List(ctx, clusterBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterBindings.Items).To(HaveLen(1))
				Expect(clusterBindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialClusterRoleBindings.Items[0].GetName()),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(allNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
						"Subjects": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Kind":      Equal("ServiceAccount"),
								"Name":      Equal("redis-promise-configure-pipeline-1"),
								"Namespace": Equal(namespace),
							}),
						),
					}),
				))
			})
		})

		When("changing the rule set on an all-namespace permission", func() {
			BeforeEach(func() {
				pipelines[0].Spec.RBAC.Permissions = []v1alpha1.Permission{
					{
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v5"},
							Resources: []string{"configmaps"},
						},
					},
					{
						ResourceNamespace: "specific-namespace",
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v7"},
							Resources: []string{"secrets"},
						},
					},
					{
						ResourceNamespace: "*",
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v1"},
							Resources: []string{"jobs"},
						},
					},
				}
				forceManualReconciliation(promise, pipelines, eventRecorder)
			})

			It("updates the all-namespace cluster role and cluster role binding while retaining the other permissions", func() {
				By("updating the cluster role for the all-namespace permission")
				clusterRoles := &rbacv1.ClusterRoleList{}
				Expect(fakeK8sClient.List(ctx, clusterRoles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoles.Items).To(HaveLen(2))
				Expect(clusterRoles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v7"),
								"Resources": ConsistOf("secrets"),
							}),
						),
					}),
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(allNamespaceClusterRole.GetName()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v1"),
								"Resources": ConsistOf("jobs"),
							}),
						),
					}),
				))

				By("retaining the role bindings for the pipeline scoped role and namespace-scoped cluster role")
				bindings := &rbacv1.RoleBindingList{}
				Expect(fakeK8sClient.List(ctx, bindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(bindings.Items).To(HaveLen(2))
				Expect(bindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(specificNamespaceRoleBinding.GetName()),
							"Namespace": Equal("specific-namespace"),
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialRole.GetName()),
							"Kind": Equal("Role"),
						}),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(pipelineNamespaceRoleBinding.GetName()),
							"Namespace": Equal(namespace),
						}),
					}),
				))

				By("retaining the cluster role binding for the all-namespace permission")
				clusterBindings := &rbacv1.ClusterRoleBindingList{}
				Expect(fakeK8sClient.List(ctx, clusterBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterBindings.Items).To(HaveLen(1))
				Expect(clusterBindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialClusterRoleBindings.Items[0].GetName()),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(allNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
						"Subjects": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Kind":      Equal("ServiceAccount"),
								"Name":      Equal("redis-promise-configure-pipeline-1"),
								"Namespace": Equal(namespace),
							}),
						),
					}),
				))

				By("retaining the role for the pipeline namespace")
				roles := &rbacv1.RoleList{}
				Expect(fakeK8sClient.List(ctx, roles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(roles.Items).To(HaveLen(1))
				Expect(roles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(initialRole.GetName()),
							"Namespace": Equal(initialRole.GetNamespace()),
						}),
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v5"),
								"Resources": ConsistOf("configmaps"),
							}),
						),
					}),
				))
			})
		})

		When("changing an all-namespace permission to a pipeline scoped permission", func() {
			BeforeEach(func() {
				pipelines[0].Spec.RBAC.Permissions = []v1alpha1.Permission{
					{
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v5"},
							Resources: []string{"configmaps"},
						},
					},
					{
						ResourceNamespace: "specific-namespace",
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v7"},
							Resources: []string{"secrets"},
						},
					},
					// Below has changed from all-namespace to pipeline scoped
					{
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v2"},
							Resources: []string{"pods"},
						},
					},
				}
				forceManualReconciliation(promise, pipelines, eventRecorder)
			})

			It("updates the permissions correctly", func() {
				By("removing the cluster role binding for the all-namespace permission")
				clusterBindings := &rbacv1.ClusterRoleBindingList{}
				Expect(fakeK8sClient.List(ctx, clusterBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterBindings.Items).To(BeEmpty())

				By("removing the all-namespace cluster role")
				clusterRoles := &rbacv1.ClusterRoleList{}
				Expect(fakeK8sClient.List(ctx, clusterRoles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoles.Items).To(HaveLen(1))
				Expect(clusterRoles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v7"),
								"Resources": ConsistOf("secrets"),
							}),
						),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
						}),
					}),
				))

				By("retaining the role binding for the pipeline scoped role and specific namespace cluster role")
				bindings := &rbacv1.RoleBindingList{}
				Expect(fakeK8sClient.List(ctx, bindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(bindings.Items).To(HaveLen(2))
				Expect(bindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(specificNamespaceRoleBinding.GetName()),
						}),
					}),
					MatchFields(IgnoreExtras, Fields{
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialRole.GetName()),
							"Kind": Equal("Role"),
						}),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(pipelineNamespaceRoleBinding.GetName()),
						}),
					}),
				))

				By("updating the pipeline scoped role")
				roles := &rbacv1.RoleList{}
				Expect(fakeK8sClient.List(ctx, roles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(roles.Items).To(HaveLen(1))
				Expect(roles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v2"),
								"Resources": ConsistOf("pods"),
							}),
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v5"),
								"Resources": ConsistOf("configmaps"),
							}),
						),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(initialRole.GetName()),
							"Namespace": Equal(initialRole.GetNamespace()),
						}),
					}),
				))
			})
		})

		When("changing a namespace scoped permission to an all-namespace permission", func() {
			BeforeEach(func() {
				pipelines[0].Spec.RBAC.Permissions = []v1alpha1.Permission{
					{
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v5"},
							Resources: []string{"configmaps"},
						},
					},
					// This has been updated from specific-namespace to *
					{
						ResourceNamespace: "*",
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v7"},
							Resources: []string{"secrets"},
						},
					},
					{
						ResourceNamespace: "*",
						PolicyRule: rbacv1.PolicyRule{
							Verbs:     []string{"list"},
							APIGroups: []string{"v2"},
							Resources: []string{"pods"},
						},
					},
				}
				forceManualReconciliation(promise, pipelines, eventRecorder)
			})

			It("updates the permissions correctly", func() {
				By("updating the cluster role for the all-namespace permission")
				clusterRoles := &rbacv1.ClusterRoleList{}
				Expect(fakeK8sClient.List(ctx, clusterRoles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterRoles.Items).To(HaveLen(1))
				Expect(clusterRoles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v2"),
								"Resources": ConsistOf("pods"),
							}),
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v7"),
								"Resources": ConsistOf("secrets"),
							}),
						),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(allNamespaceClusterRole.GetName()),
						}),
					}),
				))

				By("retaining the role and role binding for the pipeline scoped role, and deleting the namespace-specific role binding")
				roles := &rbacv1.RoleList{}
				Expect(fakeK8sClient.List(ctx, roles, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(roles.Items).To(HaveLen(1))
				Expect(roles.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"Rules": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Verbs":     ConsistOf("list"),
								"APIGroups": ConsistOf("v5"),
								"Resources": ConsistOf("configmaps"),
							}),
						),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(initialRole.GetName()),
							"Namespace": Equal(initialRole.GetNamespace()),
						}),
					}),
				))

				bindings := &rbacv1.RoleBindingList{}
				Expect(fakeK8sClient.List(ctx, bindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(bindings.Items).To(HaveLen(1))
				Expect(bindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialRole.Name),
							"Kind": Equal("Role"),
						}),
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name":      Equal(initialRole.GetName()),
							"Namespace": Equal(namespace),
						}),
					}),
				))

				By("retaining the cluster role binding for the all-namespace permission")
				clusterBindings := &rbacv1.ClusterRoleBindingList{}
				Expect(fakeK8sClient.List(ctx, clusterBindings, userPermissionPipelineLabels(promise, pipelines[0]))).To(Succeed())

				Expect(clusterBindings.Items).To(HaveLen(1))
				Expect(clusterBindings.Items).To(ConsistOf(
					MatchFields(IgnoreExtras, Fields{
						"ObjectMeta": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(initialClusterRoleBindings.Items[0].GetName()),
						}),
						"RoleRef": MatchFields(IgnoreExtras, Fields{
							"Name": Equal(allNamespaceClusterRole.GetName()),
							"Kind": Equal("ClusterRole"),
						}),
						"Subjects": ConsistOf(
							MatchFields(IgnoreExtras, Fields{
								"Kind":      Equal("ServiceAccount"),
								"Name":      Equal("redis-promise-configure-pipeline-1"),
								"Namespace": Equal(namespace),
							}),
						),
					}),
				))
			})
		})
	})

	Describe("ReconcileDelete", func() {
		BeforeEach(func() {
			pipelines = []v1alpha1.Pipeline{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "pipeline-1",
				},
				Spec: v1alpha1.PipelineSpec{
					Containers: []v1alpha1.Container{
						{Name: "container-1", Image: "busybox"},
					},
				},
			}, {
				ObjectMeta: metav1.ObjectMeta{
					Name: "pipeline-2",
				},
				Spec: v1alpha1.PipelineSpec{
					Containers: []v1alpha1.Container{
						{Name: "container-1", Image: "busybox"},
					},
				},
			}}

			workflowPipelines, uPromise = setupTest(promise, pipelines)
			for i, pipeline := range pipelines {
				generated, err := pipeline.ForPromise(&promise, v1alpha1.WorkflowActionDelete).Resources(nil)
				workflowPipelines[i] = generated
				Expect(err).NotTo(HaveOccurred())
			}
			Expect(resourceutil.ResetPipelineStatusToPending(uPromise, workflowPipelines[:1], "delete")).To(Succeed())
			Expect(resourceutil.MarkCurrentPipelineAsRunning(uPromise, logger, workflowPipelines[0].Job, "delete")).To(Succeed())
			Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())
		})

		When("there are no pipelines to reconcile", func() {
			It("considers the workflow as completed", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, nil, []v1alpha1.PipelineJobResources{}, "promise", 5, namespace)
				requeue, err := workflow.ReconcileDelete(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeue).To(BeFalse())
			})
		})

		When("there are pipelines to reconcile", func() {
			It("reconciles the first pipeline", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
				requeue, err := workflow.ReconcileDelete(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeue).To(BeTrue())
				jobList := listJobs(namespace)
				Expect(jobList).To(HaveLen(1))

				Expect(findByName(jobList, workflowPipelines[0].Job.Name)).To(BeTrue())

				By("not returning completed until the job is marked as completed", func() {
					requeue, err := workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(requeue).To(BeTrue())
				})

				By("firing an event", func() {
					Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
						"Normal PipelineStarted Delete Pipeline started: pipeline-1")))
				})
			})

			It("records delete pipelines alongside configure pipelines", func() {
				Expect(unstructured.SetNestedSlice(uPromise.Object, []any{
					map[string]any{"name": "configure-pipe", "phase": string(v1alpha1.WorkflowPhaseSucceeded)},
				}, "status", "kratix", "workflows", "configure", "pipelines")).To(Succeed())
				Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())

				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
				_, err := workflow.ReconcileDelete(opts)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: uPromise.GetName(), Namespace: uPromise.GetNamespace()}, uPromise)).To(Succeed())
				pipelines, _, _ := unstructured.NestedSlice(uPromise.Object, "status", "kratix", "workflows", "delete", "pipelines")
				Expect(pipelines).To(HaveLen(1))
				Expect(pipelines).NotTo(ContainElement(HaveKeyWithValue("name", "configure-pipe")))
			})

			It("initialises the pipeline status to Running so the status-writer can update it", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
				_, err := workflow.ReconcileDelete(opts)
				Expect(err).NotTo(HaveOccurred())

				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: uPromise.GetName(), Namespace: uPromise.GetNamespace()}, uPromise)).To(Succeed())
				pipelines, _, _ := unstructured.NestedSlice(uPromise.Object, "status", "kratix", "workflows", "delete", "pipelines")
				Expect(pipelines).To(ContainElement(SatisfyAll(
					HaveKeyWithValue("name", workflowPipelines[0].Name),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseRunning),
				)))
			})

			When("the job is in progress", func() {
				var passiveRequeue bool
				BeforeEach(func() {
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					var err error
					passiveRequeue, err = workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())
				})

				It("doesn't create a new job", func() {
					Expect(listJobs(namespace)).To(HaveLen(1))
				})

				It("returns true", func() {
					Expect(passiveRequeue).To(BeTrue())
				})

				When("a new manual reconciliation request is made", func() {
					It("cancels the current job (allowing a new job to be queued up)", func() {
						uPromise.SetLabels(map[string]string{
							"kratix.io/manual-reconciliation": "true",
						})
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
						passiveRequeue, err := workflow.ReconcileDelete(opts)

						Expect(err).NotTo(HaveOccurred())
						Expect(passiveRequeue).To(BeTrue())
						Expect(listJobs(namespace)).To(HaveLen(1))
						Expect(*listJobs(namespace)[0].Spec.Suspend).To(BeTrue())
					})
				})
			})

			When("the first pipeline is completed", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					markJobAsComplete(workflowPipelines[0].Job.Name)
				})

				It("considers the workflow as completed", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					_, err := workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())
					requeue, err := workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(requeue).To(BeFalse())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(1))

					Expect(findByName(jobList, workflowPipelines[0].Job.Name)).To(BeTrue())
				})
			})

			When("the pipeline job completes having set the workflow-suspended label", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					markJobAsCompleteWithSuspend(workflowPipelines[0].Job.Name, uPromise)
				})

				It("does not signal that the delete workflow is complete", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					requeue, err := workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(requeue).To(BeTrue())
				})
			})

			When("the pipeline job completes but the retryAfter interval has elapsed and the suspend label was removed without a new job being created", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					markJobAsComplete(workflowPipelines[0].Job.Name)
					Expect(unstructured.SetNestedSlice(uPromise.Object, []any{
						map[string]any{"name": workflowPipelines[0].Name, "phase": v1alpha1.WorkflowPhaseSuspended, "hash": workflowPipelines[0].Job.Labels[v1alpha1.KratixResourceHashLabel], "attempts": int64(1)},
					}, "status", "kratix", "workflows", "delete", "pipelines")).To(Succeed())
				})

				It("does not consider the delete workflow complete", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					requeue, err := workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(requeue).To(BeTrue())
				})

				It("resumes the pipeline in place, preserving existing retry attempts", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					_, err := workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())

					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: uPromise.GetName(), Namespace: uPromise.GetNamespace()}, uPromise)).To(Succeed())
					pipelines, _, _ := unstructured.NestedSlice(uPromise.Object, "status", "kratix", "workflows", "delete", "pipelines")
					Expect(pipelines).To(ContainElement(SatisfyAll(
						HaveKeyWithValue("name", workflowPipelines[0].Name),
						HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseRunning),
						HaveKeyWithValue("attempts", int64(1)),
					)))
				})
			})

			When("the pipeline fails", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					markJobAsFailed(workflowPipelines[0].Job.Name)
				})

				It("returns an error", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					requeue, err := workflow.ReconcileDelete(opts)
					Expect(err).To(MatchError(workflow.ErrDeletePipelineFailed))
					Expect(requeue).To(BeFalse())
				})

				When("the resource is later manually reconciled", func() {
					var (
						newWorkflowPipelines []v1alpha1.PipelineJobResources
						err                  error
					)

					BeforeEach(func() {
						labelPromiseForManualReconciliation("redis")
						newWorkflowPipelines, uPromise = setupTest(promise, pipelines)
						for i, pipeline := range pipelines {
							newWorkflowPipelines[i], err = pipeline.ForPromise(&promise, v1alpha1.WorkflowActionDelete).Resources(nil)
							Expect(err).NotTo(HaveOccurred())
						}
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, newWorkflowPipelines, "promise", 5, namespace)
						passiveRequeue, err := workflow.ReconcileDelete(opts)
						Expect(passiveRequeue).To(BeTrue())
						Expect(err).NotTo(HaveOccurred())
					})

					It("re-triggers the pipeline in the workflow", func() {
						Expect(err).NotTo(HaveOccurred())
						jobList := listJobs(namespace)
						Expect(jobList).To(HaveLen(2))
						Expect(findByName(jobList, workflowPipelines[0].Job.Name)).To(BeTrue())
						Expect(findByName(jobList, newWorkflowPipelines[0].Job.GetName())).To(BeTrue())
					})

					It("deletes the label", func() {
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: uPromise.GetName()}, uPromise)).To(Succeed())
						Expect(uPromise.GetLabels()).NotTo(HaveKey(resourceutil.ManualReconciliationLabel))
					})

					It("fires an event", func() {
						Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
							"Normal PipelineStarted Delete Pipeline started: pipeline-1")))
					})
				})
			})

			When("there are running configure pipeline", func() {
				var deleteWorkflowPipelines []v1alpha1.PipelineJobResources
				var opts workflow.Opts

				BeforeEach(func() {
					deleteWorkflowPipelines = nil
					configure, err := pipelines[0].ForPromise(&promise, v1alpha1.WorkflowActionConfigure).Resources(nil)
					Expect(err).NotTo(HaveOccurred())
					workflowPipelines[0] = configure
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())

					generatedResources, err := pipelines[0].ForPromise(&promise, v1alpha1.WorkflowActionDelete).Resources(nil)
					Expect(err).NotTo(HaveOccurred())
					generatedResources.Job.SetCreationTimestamp(nextTimestamp())
					deleteWorkflowPipelines = append(deleteWorkflowPipelines, generatedResources)

					opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, deleteWorkflowPipelines, "promise", 5, namespace)
					requeue, err := workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(requeue).To(BeTrue())
					Expect(listJobs(namespace)).To(HaveLen(1))
				})

				It("waits for configure pipeline to finish before starting delete pipeline", func() {
					Expect(findByName(listJobs(namespace), workflowPipelines[0].Job.Name)).To(BeTrue())
				})

				When("the configure pipeline completes", func() {
					It("creates the delete pipeline", func() {
						markJobAsComplete(workflowPipelines[0].Job.Name)

						requeue, err := workflow.ReconcileDelete(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeTrue())

						jobList := listJobs(namespace)
						Expect(jobList).To(HaveLen(2))
						Expect(findByName(jobList, deleteWorkflowPipelines[0].Job.Name)).To(BeTrue())
					})
				})

				When("the configure pipeline fails", func() {
					It("creates the delete pipeline", func() {
						markJobAsFailed(workflowPipelines[0].Job.Name)

						requeue, err := workflow.ReconcileDelete(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeTrue())

						jobList := listJobs(namespace)
						Expect(jobList).To(HaveLen(2))
						Expect(findByName(jobList, deleteWorkflowPipelines[0].Job.Name)).To(BeTrue())
					})
				})
			})
		})
	})
})

func createFakeWorks(pipelines []v1alpha1.Pipeline, promiseName string) {
	for _, pipeline := range pipelines {
		work := v1alpha1.Work{}
		work.Name = fmt.Sprintf("work-%s", uuid.New().String()[0:5])
		work.Namespace = namespace
		work.Spec.PromiseName = promiseName
		work.Labels = resourceutil.GetWorkLabels(promiseName, "", "", pipeline.Name, v1alpha1.WorkTypePromise)
		Expect(fakeK8sClient.Create(ctx, &work)).To(Succeed())
	}
}

func createStaticDependencyWork(promiseName string) {
	work := v1alpha1.Work{}
	work.Name = fmt.Sprintf("static-deps-%s", uuid.New().String()[0:5])
	work.Spec.PromiseName = promiseName
	work.Namespace = namespace
	work.Labels = resourceutil.GetWorkLabels(promiseName, "", "", "", v1alpha1.WorkTypeStaticDependency)
	Expect(fakeK8sClient.Create(ctx, &work)).To(Succeed())
}

func countEvents(recorder *events.FakeRecorder, reason string) int {
	count := 0
	for {
		select {
		case event := <-recorder.Events:
			if strings.Contains(event, reason) {
				count++
			}
		default:
			return count
		}
	}
}

func setupTest(promise v1alpha1.Promise, pipelines []v1alpha1.Pipeline) ([]v1alpha1.PipelineJobResources, *unstructured.Unstructured) {
	var err error
	p := v1alpha1.Promise{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, &p)).To(Succeed())

	uPromise, err := p.ToUnstructured()
	Expect(err).NotTo(HaveOccurred())

	resourceutil.MarkConfigureWorkflowAsRunning(logger, uPromise)
	Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())

	var workflowPipelines []v1alpha1.PipelineJobResources
	for _, p := range pipelines {
		generatedResources, err := p.ForPromise(&promise, v1alpha1.WorkflowActionConfigure).Resources(nil)
		Expect(err).NotTo(HaveOccurred())
		generatedResources.Job.SetCreationTimestamp(nextTimestamp())
		workflowPipelines = append(workflowPipelines, generatedResources)
	}

	statuses, _, err := unstructured.NestedSlice(uPromise.Object, "status", "kratix", "workflows", "configure", "pipelines")
	Expect(err).NotTo(HaveOccurred())
	for i, entry := range statuses {
		status := entry.(map[string]any)
		if i < len(workflowPipelines) && status["hash"] == nil {
			status["hash"] = workflowPipelines[i].Job.Labels[v1alpha1.KratixResourceHashLabel]
		}
	}
	Expect(unstructured.SetNestedSlice(uPromise.Object, statuses, "status", "kratix", "workflows", "configure", "pipelines")).To(Succeed())
	Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())

	return workflowPipelines, uPromise
}

func setupAndReconcileUntilPipelinesCompleted(promise v1alpha1.Promise, pipelines []v1alpha1.Pipeline, eventRecorder events.EventRecorder) ([]v1alpha1.PipelineJobResources, *unstructured.Unstructured) {
	updatedWorkflowPipeline, uPromise := setupTest(promise, pipelines)
	opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
	_, err := workflow.ReconcileConfigure(opts)
	Expect(err).NotTo(HaveOccurred())

	markJobAsComplete(updatedWorkflowPipeline[0].Job.Name)
	setParentPipelinesSucceeded(uPromise, updatedWorkflowPipeline, 1)
	_, err = workflow.ReconcileConfigure(opts)
	Expect(err).NotTo(HaveOccurred())

	markJobAsComplete(updatedWorkflowPipeline[1].Job.Name)
	setParentPipelinesSucceeded(uPromise, updatedWorkflowPipeline, 2)

	return updatedWorkflowPipeline, uPromise
}

func labelPromiseForManualReconciliation(name string) {
	promise := &v1alpha1.Promise{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: name}, promise)).To(Succeed())
	promise.SetLabels(labels.Merge(promise.GetLabels(), map[string]string{
		"kratix.io/manual-reconciliation": "true",
	}))
	Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())
}

func labelPromiseWithWorkflowRestart(name string) {
	promise := &v1alpha1.Promise{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: name}, promise)).To(Succeed())
	promise.SetLabels(labels.Merge(promise.GetLabels(), map[string]string{
		resourceutil.WorkflowRunFromStartLabel: "true",
	}))
	Expect(fakeK8sClient.Update(ctx, promise)).To(Succeed())
}

func markJobAsComplete(name string) {
	markJobAs(batchv1.JobComplete, name)
}

func markJobAsFailed(name string) {
	markJobAs(batchv1.JobFailed, name)
}

// Kratix sets spec.suspend; in a cluster the Job controller then reports the
// Suspended condition.
func markJobsAsSuspended() {
	GinkgoHelper()
	for _, job := range listJobs(namespace) {
		if job.Spec.Suspend != nil && *job.Spec.Suspend && len(job.Status.Conditions) == 0 {
			markJobAs(batchv1.JobSuspended, job.GetName())
		}
	}
}

func reconcileConfigure(opts workflow.Opts) {
	GinkgoHelper()
	_, err := workflow.ReconcileConfigure(opts)
	Expect(err).NotTo(HaveOccurred())
}

func markJobAsCompleteWithSuspend(name string, parentObject *unstructured.Unstructured) {
	markJobAsComplete(name)
	parentObject.SetLabels(map[string]string{v1alpha1.WorkflowSuspendedLabel: "true"})
}

func markJobAs(conditionType batchv1.JobConditionType, name string) {
	job := &batchv1.Job{}
	ExpectWithOffset(1, fakeK8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, job)).To(Succeed())

	job.Status.Conditions = []batchv1.JobCondition{
		{
			Type:   conditionType,
			Status: v1.ConditionTrue,
		},
	}

	switch conditionType {
	case batchv1.JobComplete:
		job.Status.Succeeded = 1
	case batchv1.JobFailed:
		job.Status.Failed = 1
	case batchv1.JobSuspended:
	default:
		Fail("unsupported condition type")
	}

	ExpectWithOffset(1, fakeK8sClient.Status().Update(ctx, job)).To(Succeed())
}

func setParentPipelinesSucceeded(parent *unstructured.Unstructured, resources []v1alpha1.PipelineJobResources, succeeded int) {
	GinkgoHelper()
	pipelines := make([]any, 0, len(resources))
	for i, resource := range resources {
		phase := v1alpha1.WorkflowPhasePending
		if i < succeeded {
			phase = v1alpha1.WorkflowPhaseSucceeded
		}
		pipelines = append(pipelines, map[string]any{
			"name":               resource.Name,
			"hash":               resource.Job.Labels[v1alpha1.KratixResourceHashLabel],
			"phase":              phase,
			"lastTransitionTime": metav1.Now().Format(time.RFC3339),
		})
	}
	Expect(unstructured.SetNestedSlice(parent.Object, pipelines, "status", "kratix", "workflows", "configure", "pipelines")).To(Succeed())
	Expect(fakeK8sClient.Status().Update(ctx, parent)).To(Succeed())
}

func listJobs(namespace string) []batchv1.Job {
	jobList := &batchv1.JobList{}
	err := fakeK8sClient.List(ctx, jobList, client.InNamespace(namespace))
	Expect(err).NotTo(HaveOccurred())
	return jobList.Items
}

func jobNamesForPipeline(pipelineName string) []string {
	names := []string{}
	for _, job := range listJobs(namespace) {
		if job.GetLabels()[v1alpha1.PipelineNameLabel] == pipelineName {
			names = append(names, job.GetName())
		}
	}
	return names
}

func findByName(jobs []batchv1.Job, name string) bool {
	for _, j := range jobs {
		if j.Name == name {
			return true
		}
	}
	return false
}

var timestamp = metav1.NewTime(time.Now())

func nextTimestamp() metav1.Time {
	timestamp = metav1.NewTime(timestamp.Add(time.Minute))
	return timestamp
}

func userPermissionPipelineLabels(promise v1alpha1.Promise, pipeline v1alpha1.Pipeline) client.MatchingLabels {
	return client.MatchingLabels(labels.Set{
		"kratix.io/promise-name":    promise.GetName(),
		"kratix.io/pipeline-name":   pipeline.Name,
		"kratix.io/workflow-type":   "promise",
		"kratix.io/workflow-action": "configure",
	})
}

func forceManualReconciliation(promise v1alpha1.Promise, pipelines []v1alpha1.Pipeline, eventRecorder events.EventRecorder) {
	GinkgoHelper()
	labelPromiseForManualReconciliation(promise.GetName())
	resources, uPromise := setupTest(promise, pipelines)
	setParentPipelinesSucceeded(uPromise, resources, len(resources)-1)
	opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, resources, "promise", 5, namespace)
	passiveRequeue, err := workflow.ReconcileConfigure(opts)
	Expect(err).NotTo(HaveOccurred())
	if passiveRequeue {
		assertPromisePipelinesSucceeded(promise.GetName(), 0)
		_, err = workflow.ReconcileConfigure(opts)
	}
	Expect(err).NotTo(HaveOccurred())
}

func assertPromisePipelinesSucceeded(name string, succeeded int) {
	GinkgoHelper()
	promise := &v1alpha1.Promise{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: name}, promise)).To(Succeed())
	Expect(countPromisePipelinesInPhase(promise, v1alpha1.WorkflowPhaseSucceeded)).To(Equal(succeeded))
	Expect(countPromisePipelinesInPhase(promise, v1alpha1.WorkflowPhaseFailed)).To(Equal(0))
}

func countPromisePipelinesInPhase(promise *v1alpha1.Promise, phase string) int {
	count := 0
	for _, pipeline := range promise.Status.Kratix.Workflows["configure"].Pipelines {
		if pipeline.Phase == phase {
			count++
		}
	}
	return count
}

func reconcile(opts workflow.Opts, promise *v1alpha1.Promise) bool {
	GinkgoHelper()

	passiveRequeue, err := workflow.ReconcileConfigure(opts)
	Expect(err).NotTo(HaveOccurred())

	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, promise)).To(Succeed())
	uPromise, err := promise.ToUnstructured()
	Expect(err).NotTo(HaveOccurred())
	opts.SetParentObject(uPromise)

	return passiveRequeue
}

func resetWorkflowPipelineJobs(workflowPipelines []v1alpha1.PipelineJobResources) {
	for _, p := range workflowPipelines {
		p.Job.SetName(p.Job.Name + uuid.New().String()[0:5])
		p.Job.SetResourceVersion("")
	}
}
