package workflow_test

import (
	"fmt"
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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var namespace = "kratix-platform-system"

var _ = Describe("Workflow Reconciler", func() {
	var promise v1alpha1.Promise
	var workflowPipelines []v1alpha1.PipelineJobResources
	var uPromise *unstructured.Unstructured
	var pipelines []v1alpha1.Pipeline
	var eventRecorder *record.FakeRecorder

	BeforeEach(func() {
		eventRecorder = record.NewFakeRecorder(1024)

		promise = v1alpha1.Promise{
			ObjectMeta: metav1.ObjectMeta{
				Name: "redis",
			},
			TypeMeta: metav1.TypeMeta{
				APIVersion: "platform.kratix.io/v1alpha1",
				Kind:       "Promise",
			},
		}

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

		Expect(fakeK8sClient.Create(ctx, &promise)).To(Succeed())
	})

	Describe("ReconcileConfigure", func() {
		BeforeEach(func() {
			workflowPipelines, uPromise = setupTest(promise, pipelines)
		})

		When("no pipeline for the workflow was executed", func() {
			BeforeEach(func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
				abort, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(abort).To(BeTrue())
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

		When("there are jobs for this workflow", func() {
			BeforeEach(func() {
				j := workflowPipelines[0].Job
				Expect(fakeK8sClient.Create(ctx, j)).To(Succeed())
			})

			Context("and the job is in progress", func() {
				var abort bool

				BeforeEach(func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					var err error
					abort, err = workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
				})

				It("doesn't create a new job", func() {
					Expect(listJobs(namespace)).To(HaveLen(1))
				})

				It("returns true", func() {
					Expect(abort).To(BeTrue())
				})

				When("a new manual reconciliation request is made", func() {
					It("cancels the current job (allowing a new job to be queued up)", func() {
						uPromise.SetLabels(map[string]string{
							"kratix.io/manual-reconciliation": "true",
						})

						resourceutil.SetStatus(uPromise, logger, "workflowsSucceeded", int64(0), "workflowsFailed", int64(0))
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
						abort, err := workflow.ReconcileConfigure(opts)

						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())
						Expect(listJobs(namespace)).To(HaveLen(1))
						Expect(*listJobs(namespace)[0].Spec.Suspend).To(BeTrue())
					})
				})
			})

			Context("and the job is completed", func() {
				BeforeEach(func() {
					markJobAsComplete(workflowPipelines[0].Job.Name)
				})

				It("triggers the next pipeline in the workflow", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					abort, err := workflow.ReconcileConfigure(opts)
					Expect(abort).To(BeTrue())
					Expect(err).NotTo(HaveOccurred())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(2))
					Expect(findByName(jobList, workflowPipelines[1].Job.Name)).To(BeTrue())

					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
					Expect(promise.Status.WorkflowsSucceeded).To(Equal(int64(1)))
					Expect(promise.Status.WorkflowsFailed).To(Equal(int64(0)))
				})

				When("there are no more pipelines to run", func() {
					BeforeEach(func() {
						j := workflowPipelines[1].Job
						Expect(fakeK8sClient.Create(ctx, j)).To(Succeed())
						markJobAsComplete(j.Name)
					})

					It("returns true (representing all pipelines completed)", func() {
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(listJobs(namespace)).To(HaveLen(2))
						Expect(abort).To(BeFalse())
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
						Expect(promise.Status.WorkflowsSucceeded).To(Equal(int64(2)))
						Expect(promise.Status.WorkflowsFailed).To(Equal(int64(0)))
					})
				})
			})

			Context("and the job has failed", func() {
				var abort bool
				var err error

				Context("and the SkipConditions flag is not set", func() {
					BeforeEach(func() {
						markJobAsFailed(workflowPipelines[0].Job.Name)
						newWorkflowPipelines, uPromise := setupTest(promise, pipelines)
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, newWorkflowPipelines, "promise", 5, namespace)
						abort, err = workflow.ReconcileConfigure(opts)
					})

					It("does not create any new Jobs on the next reconciliation", func() {
						// Expect only the failed job to exist
						jobList := listJobs(namespace)
						Expect(jobList).To(HaveLen(1))
						job := jobList[0]
						Expect(job.Labels["kratix.io/pipeline-name"]).To(Equal(workflowPipelines[0].Job.Labels["kratix.io/pipeline-name"]))
					})

					It("halts the workflow by not requeuing", func() {
						Expect(abort).To(BeTrue())
						Expect(err).NotTo(HaveOccurred())
					})

					It("updates the Promise status", func() {
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
						Expect(promise.Status.Conditions).To(HaveLen(2))
						configureWorkflowCond := apimeta.FindStatusCondition(promise.Status.Conditions, string(resourceutil.ConfigureWorkflowCompletedCondition))
						Expect(configureWorkflowCond.Message).To(Equal("A Configure Pipeline has failed: pipeline-1"))
						Expect(configureWorkflowCond.Reason).To(Equal("ConfigureWorkflowFailed"))
						Expect(string(configureWorkflowCond.Status)).To(Equal("False"))

						reconciledCond := apimeta.FindStatusCondition(promise.Status.Conditions, "Reconciled")
						Expect(reconciledCond.Message).To(Equal("Failing"))
						Expect(reconciledCond.Reason).To(Equal("ConfigureWorkflowFailed"))
						Expect(string(reconciledCond.Status)).To(Equal("False"))

						Expect(promise.Status.WorkflowsFailed).To(Equal(int64(1)))
						Expect(promise.Status.WorkflowsSucceeded).To(Equal(int64(0)))
					})

					It("publishes an event", func() {
						Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
							"Warning ConfigureWorkflowFailed A promise/configure Pipeline has failed: pipeline-1")))
					})

					When("the parent is later manually reconciled", func() {
						var newWorkflowPipelines []v1alpha1.PipelineJobResources

						BeforeEach(func() {
							labelPromiseForManualReconciliation("redis")
							newWorkflowPipelines, uPromise = setupTest(promise, pipelines)
							opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, newWorkflowPipelines, "promise", 5, namespace)
							abort, err = workflow.ReconcileConfigure(opts)
							Expect(abort).To(BeTrue())
							Expect(err).NotTo(HaveOccurred())
						})

						It("re-triggers the first pipeline in the workflow", func() {
							Expect(err).NotTo(HaveOccurred())
							jobList := listJobs(namespace)
							Expect(jobList).To(HaveLen(2))
							Expect(findByName(jobList, newWorkflowPipelines[0].Job.GetName())).To(BeTrue())
						})
					})

					When("the reconciliation is triggered and the previously failing job succeeds", func() {
						It("updates the promise status successfully", func() {
							Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
							Expect(promise.Status.WorkflowsFailed).To(Equal(int64(1)))
							Expect(promise.Status.WorkflowsSucceeded).To(Equal(int64(0)))

							// Trigger the workflow via the manual reconciliation, running the pipeline from the start
							labelPromiseForManualReconciliation("redis")
							newWorkflowPipelines, uPromise := setupTest(promise, pipelines)
							opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, newWorkflowPipelines, "promise", 5, namespace)
							abort, err = workflow.ReconcileConfigure(opts)
							Expect(abort).To(BeTrue())
							Expect(err).NotTo(HaveOccurred())

							Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
							Expect(promise.Status.WorkflowsFailed).To(Equal(int64(0)))

							// Mark the job created by the first pipeline as complete
							markJobAsComplete(newWorkflowPipelines[0].Job.Name)
							abort, err = workflow.ReconcileConfigure(opts)
							Expect(abort).To(BeTrue())
							Expect(err).NotTo(HaveOccurred())
							Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
							Expect(promise.Status.WorkflowsSucceeded).To(Equal(int64(1)))
						})
					})
				})

				When("the SkipConditions flag is set", func() {
					var existingConditions []metav1.Condition

					BeforeEach(func() {
						markJobAsFailed(workflowPipelines[0].Job.Name)
						newWorkflowPipelines, uPromise := setupTest(promise, pipelines)
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
						existingConditions = promise.Status.Conditions

						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, newWorkflowPipelines, "promise", 5, namespace)
						opts.SkipConditions = true
						abort, err = workflow.ReconcileConfigure(opts)
					})

					It("does not update the Promise status", func() {
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &promise)).To(Succeed())
						Expect(promise.Status.Conditions).To(Equal(existingConditions))
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
					abort, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(1))
					Expect(abort).To(BeTrue())

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

				Context("but they are not the most recent", func() {
					It("re-runs all pipelines in the workflow", func() {
						// Reconcile with the *original* pipelines and promise spec
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, originalWorkflowPipelines, "promise", 5, namespace)
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())

						// Expect the original 2 jobs, the updated 2 jobs, and the first job
						// from re-running the first pipeline again on this reconciliation
						jobList := listJobs(namespace)
						Expect(jobList).To(HaveLen(5))

						markJobAsComplete(originalWorkflowPipelines[0].Job.Name)

						abort, err = workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())
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
					abort, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(1))
					Expect(abort).To(BeTrue())
				})

				It("suspends the previous job", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
					_, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())

					job := &batchv1.Job{}
					Expect(fakeK8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: workflowPipelines[0].Job.Name}, job)).To(Succeed())

					Expect(job.Spec.Suspend).NotTo(BeNil())
					Expect(*job.Spec.Suspend).To(BeTrue())
				})

				When("the outdated job completes", func() {
					var jobList []batchv1.Job

					BeforeEach(func() {
						markJobAsComplete(workflowPipelines[0].Job.Name)
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						jobList = listJobs(namespace)
						Expect(abort).To(BeTrue())
						Expect(findByName(jobList, workflowPipelines[0].Job.Name)).To(BeTrue())
					})

					It("triggers the first pipeline in the workflow at the new spec", func() {
						Expect(findByName(jobList, updatedWorkflowPipeline[0].Job.Name)).To(BeTrue())
					})

					It("never triggers the next job in the outdated workflow", func() {
						Expect(findByName(jobList, workflowPipelines[1].Job.Name)).To(BeFalse())
					})
				})
			})
		})

		Context("promise workflows", func() {
			var opts workflow.Opts
			BeforeEach(func() {
				opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)

				abort, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(abort).To(BeTrue())

				markJobAsComplete(workflowPipelines[0].Job.Name)

				abort, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(abort).To(BeTrue())
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
				abort, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(abort).To(BeTrue())
			})

			It("sets the status of the resource when the pipelines are still in progress", func() {
				updatedResource := &unstructured.Unstructured{}
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
					HaveKeyWithValue("reason", "PipelinesInProgress"),
					HaveKeyWithValue("lastTransitionTime", Not(BeEmpty())),
				))

				Expect(resourceutil.GetWorkflowsCounterStatus(updatedResource, "workflowsSucceeded")).To(Equal(int64((0))))
				Expect(resourceutil.GetWorkflowsCounterStatus(updatedResource, "workflowsFailed")).To(Equal(int64(0)))
			})

			It("fires an event for the new pipeline", func() {
				// The status update will trigger a reconciliation in a real cluster.
				// Simulate that here by calling ReconcileConfigure again.
				abort, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(abort).To(BeTrue())

				Eventually(eventRecorder.Events).Should(Receive(ContainSubstring(
					"Normal PipelineStarted Configure Pipeline started: pipeline-1")))
			})
		})

		When("there are more old workflow execution than configured in workflow options ", func() {
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
						_, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						markJobAsComplete(updatedWorkflowPipeline[j].Job.Name)
					}
				}
				updatedWorkflowPipeline, uPromise := setupTest(updatedPromise, pipelines)
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", numberOfJobLimit, namespace)
				_, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())

				jobList := listJobs(namespace)
				Expect(jobList).To(HaveLen(numberOfJobLimit * len(updatedWorkflowPipeline)))
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
				abort, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(abort).To(BeFalse())

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
				abort, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(abort).To(BeFalse())

				createFakeWorks(updatedPipelines, promise.Name)
				createFakeWorks(pipelines, "not-redis")
				createStaticDependencyWork(promise.Name)
			})

			It("cleans up any leftover works from previous runs", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflows, "promise", 5, namespace)
				abort, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(abort).To(BeFalse())

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

					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					abort, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(abort).To(BeFalse())

					Expect(listJobs(namespace)).To(HaveLen(2))

					labelPromiseForManualReconciliation("redis")

					workflowPipelines, uPromise = setupTest(promise, pipelines)
				})

				It("re-triggers all the pipelines in the workflow", func() {
					var jobs []batchv1.Job
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					By("re-triggering the first pipeline on the next reconciliation", func() {
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())

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
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(3))
					})

					markJobAsComplete(jobs[2].Name)

					By("removing the manual reconciliation label again", func() {
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())

						promise := v1alpha1.Promise{}
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &promise)).To(Succeed())
						Expect(promise.GetLabels()).NotTo(HaveKey("kratix.io/manual-reconciliation"))
					})

					By("restarting from pipeline 0, respecting the label added mid-flight", func() {
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(4))
						Expect(jobs[3].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[0].Name))
					})

					By("waiting for the first pipeline to complete again", func() {
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(4))
					})

					markJobAsComplete(jobs[3].Name)
					By("triggering the second pipeline when the previous completes", func() {
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(5))
						Expect(jobs[4].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[1].Name))
					})

					By("waiting for the second pipeline to complete", func() {
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(5))
					})

					By("marking it all as completed once the last pipeline completes", func() {
						markJobAsComplete(jobs[4].Name)
						abort, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeFalse())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(5))
					})
				})
			})

			When("there is a job in progress", func() {
				var abort bool

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

					abort, err = workflow.ReconcileConfigure(opts)
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
					Expect(abort).To(BeTrue())
				})

				It("does not remove the manual reconciliation label from the parent", func() {
					promise := v1alpha1.Promise{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &promise)).To(Succeed())
					Expect(promise.GetLabels()).To(HaveKey("kratix.io/manual-reconciliation"))
				})
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

			When("the job is in progress", func() {
				var abort bool
				BeforeEach(func() {
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
					var err error
					abort, err = workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())
				})

				It("doesn't create a new job", func() {
					Expect(listJobs(namespace)).To(HaveLen(1))
				})

				It("returns true", func() {
					Expect(abort).To(BeTrue())
				})

				When("a new manual reconciliation request is made", func() {
					It("cancels the current job (allowing a new job to be queued up)", func() {
						uPromise.SetLabels(map[string]string{
							"kratix.io/manual-reconciliation": "true",
						})
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 5, namespace)
						abort, err := workflow.ReconcileDelete(opts)

						Expect(err).NotTo(HaveOccurred())
						Expect(abort).To(BeTrue())
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
					requeue, err := workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(requeue).To(BeFalse())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(1))

					Expect(findByName(jobList, workflowPipelines[0].Job.Name)).To(BeTrue())
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
						opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, newWorkflowPipelines, "promise", 5, namespace)
						abort, err := workflow.ReconcileDelete(opts)
						Expect(abort).To(BeTrue())
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

	return workflowPipelines, uPromise
}

func setupAndReconcileUntilPipelinesCompleted(promise v1alpha1.Promise, pipelines []v1alpha1.Pipeline, eventRecorder record.EventRecorder) ([]v1alpha1.PipelineJobResources, *unstructured.Unstructured) {
	updatedWorkflowPipeline, uPromise := setupTest(promise, pipelines)
	opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, updatedWorkflowPipeline, "promise", 5, namespace)
	_, err := workflow.ReconcileConfigure(opts)
	Expect(err).NotTo(HaveOccurred())

	markJobAsComplete(updatedWorkflowPipeline[0].Job.Name)
	_, err = workflow.ReconcileConfigure(opts)
	Expect(err).NotTo(HaveOccurred())

	markJobAsComplete(updatedWorkflowPipeline[1].Job.Name)

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

func markJobAsComplete(name string) {
	markJobAs(batchv1.JobComplete, name)
}

func markJobAsFailed(name string) {
	markJobAs(batchv1.JobFailed, name)
}

func markJobAs(conditionType batchv1.JobConditionType, name string) {
	job := &batchv1.Job{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
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
	default:
		Fail("unsupported condition type")
	}

	Expect(fakeK8sClient.Status().Update(ctx, job)).To(Succeed())
}

func listJobs(namespace string) []batchv1.Job {
	jobList := &batchv1.JobList{}
	err := fakeK8sClient.List(ctx, jobList, client.InNamespace(namespace))
	Expect(err).NotTo(HaveOccurred())
	return jobList.Items
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

func forceManualReconciliation(promise v1alpha1.Promise, pipelines []v1alpha1.Pipeline, eventRecorder record.EventRecorder) {
	labelPromiseForManualReconciliation(promise.GetName())
	resources, uPromise := setupTest(promise, pipelines)
	opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, resources, "promise", 5, namespace)
	_, err := workflow.ReconcileConfigure(opts)
	Expect(err).NotTo(HaveOccurred())
}
