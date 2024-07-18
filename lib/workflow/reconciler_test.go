package workflow_test

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var namespace = "kratix-platform-system"

var _ = Describe("Workflow Reconciler", func() {
	var promise v1alpha1.Promise
	var workflowPipelines []v1alpha1.PipelineJobResources
	var uPromise *unstructured.Unstructured
	var pipelines []v1alpha1.Pipeline

	BeforeEach(func() {
		promise = v1alpha1.Promise{
			ObjectMeta: metav1.ObjectMeta{
				Name: "redis",
			},
			TypeMeta: metav1.TypeMeta{
				APIVersion: "platform.kratix.io/v1alpha1",
				Kind:       "Promise",
			},
		}

		Expect(fakeK8sClient.Create(ctx, &promise)).To(Succeed())

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

	Describe("ReconcileConfigure", func() {
		When("no pipeline for the workflow was executed", func() {
			It("creates a new job with the first pipeline job spec", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
				requeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeue).To(BeTrue())
				jobList := listJobs(namespace)
				Expect(jobList).To(HaveLen(1))
				Expect(jobList[0].Name).To(Equal(workflowPipelines[0].Job.Name))
			})
		})

		When("the service account does exist", func() {
			When("the service account does not have the kratix label", func() {
				It("should not add the kratix label to the service account", func() {
					Expect(fakeK8sClient.Create(ctx, &v1.ServiceAccount{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "redis-promise-configure-pipeline-1",
							Namespace: namespace,
						},
					})).NotTo(HaveOccurred())

					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
					_, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					sa := &v1.ServiceAccount{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis-promise-configure-pipeline-1", Namespace: namespace}, sa)).To(Succeed())
					Expect(sa.GetLabels()).To(BeEmpty())
				})
			})
		})

		When("there are jobs for this workflow", func() {
			BeforeEach(func() {
				j := workflowPipelines[0].Job
				Expect(fakeK8sClient.Create(ctx, j)).To(Succeed())
			})

			Context("and the job is in progress", func() {
				var requeue bool

				BeforeEach(func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
					var err error
					requeue, err = workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
				})

				It("doesn't create a new job", func() {
					Expect(listJobs(namespace)).To(HaveLen(1))
				})

				It("returns true", func() {
					Expect(requeue).To(BeTrue())
				})

				When("a new manual reconciliation request is made", func() {
					It("cancels the current job (allowing a new job to be queued up)", func() {
						uPromise.SetLabels(map[string]string{
							"kratix.io/manual-reconciliation": "true",
						})
						opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
						requeue, err := workflow.ReconcileConfigure(opts)

						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeTrue())
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
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
					requeue, err := workflow.ReconcileConfigure(opts)
					Expect(requeue).To(BeTrue())
					Expect(err).NotTo(HaveOccurred())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(2))
					Expect(findByName(jobList, workflowPipelines[1].Job.Name)).To(BeTrue())
				})

				When("there are no more pipelines to run", func() {
					BeforeEach(func() {
						j := workflowPipelines[1].Job
						Expect(fakeK8sClient.Create(ctx, j)).To(Succeed())
						markJobAsComplete(j.Name)
					})

					It("returns true (representing all pipelines completed)", func() {
						opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(listJobs(namespace)).To(HaveLen(2))
						Expect(requeue).To(BeFalse())
					})
				})
			})

			Context("and the job has failed", func() {
				var requeue bool
				var err error

				BeforeEach(func() {
					markJobAsFailed(workflowPipelines[0].Job.Name)
					newWorkflowPipelines, uPromise := setupTest(promise, pipelines)
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, newWorkflowPipelines, "promise")
					requeue, err = workflow.ReconcileConfigure(opts)
				})

				It("does not create any new Jobs on the next reconciliation", func() {
					// Expect only the failed job to exist
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(1))
					job := jobList[0]
					Expect(job.Labels["kratix.io/pipeline-name"]).To(Equal(workflowPipelines[0].Job.Labels["kratix.io/pipeline-name"]))
				})

				It("halts the workflow by not requeuing", func() {
					Expect(requeue).To(BeFalse())
					Expect(err).NotTo(HaveOccurred())
				})

				When("the parent is later manually reconciled", func() {
					var newWorkflowPipelines []v1alpha1.PipelineJobResources

					BeforeEach(func() {
						labelPromiseForManualReconciliation("redis")
						newWorkflowPipelines, uPromise = setupTest(promise, pipelines)
						opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, newWorkflowPipelines, "promise")
						requeue, err = workflow.ReconcileConfigure(opts)
					})

					It("re-triggers the first pipeline in the workflow", func() {
						Expect(err).NotTo(HaveOccurred())
						jobList := listJobs(namespace)
						Expect(jobList).To(HaveLen(2))
						Expect(findByName(jobList, newWorkflowPipelines[0].Job.GetName())).To(BeTrue())
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
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "promise")
					requeue, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(1))
					Expect(requeue).To(BeTrue())

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
						opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, originalWorkflowPipelines, "promise")
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeTrue())

						// Expect the original 2 jobs, the updated 2 jobs, and the first job
						// from re-running the first pipeline again on this reconciliation
						jobList := listJobs(namespace)
						Expect(jobList).To(HaveLen(5))

						markJobAsComplete(originalWorkflowPipelines[0].Job.Name)

						requeue, err = workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeTrue())
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
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "promise")
					requeue, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					jobList := listJobs(namespace)
					Expect(len(jobList)).To(Equal(1))
					Expect(requeue).To(BeTrue())
				})

				It("suspends the previous job", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "promise")
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
						opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "promise")
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						jobList = listJobs(namespace)
						Expect(requeue).To(BeTrue())
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
				opts = workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")

				requeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeue).To(BeTrue())

				markJobAsComplete(workflowPipelines[0].Job.Name)

				requeue, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeue).To(BeTrue())
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

			When("all pipelines are executed", func() {
				BeforeEach(func() {
					markJobAsComplete(workflowPipelines[1].Job.Name)
					requeue, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(requeue).To(BeFalse())
				})

				It("deletes the promise scheduling configmap", func() {
					configMap := &v1.ConfigMap{}
					err := fakeK8sClient.Get(ctx, types.NamespacedName{Name: "destination-selectors-redis", Namespace: namespace}, configMap)
					Expect(err).To(HaveOccurred())
					Expect(errors.IsNotFound(err)).To(BeTrue())
				})

			})
		})

		When("there are more than 5 old workflow execution", func() {
			It("deletes the oldest jobs", func() {
				var updatedPromise v1alpha1.Promise
				for i := range 6 {
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &updatedPromise)).To(Succeed())
					updatedPromise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{{
						MatchLabels: map[string]string{"app": fmt.Sprintf("redis-%d", i)},
					}}
					Expect(fakeK8sClient.Update(ctx, &updatedPromise)).To(Succeed())

					updatedWorkflowPipeline, uPromise := setupTest(updatedPromise, pipelines)
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "promise")
					for j := range 2 {
						_, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						markJobAsComplete(updatedWorkflowPipeline[j].Job.Name)
					}
				}
				updatedWorkflowPipeline, uPromise := setupTest(updatedPromise, pipelines)
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "promise")
				_, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())

				jobList := listJobs(namespace)
				Expect(len(jobList)).To(Equal(5 * len(updatedWorkflowPipeline)))
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

				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
				requeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeue).To(BeFalse())

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

				opts = workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflows, "promise")
				requeue, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeue).To(BeFalse())

				createFakeWorks(updatedPipelines, promise.Name)
				createFakeWorks(pipelines, "not-redis")
				createStaticDependencyWork(promise.Name)
			})

			It("cleans up any leftover works from previous runs", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflows, "promise")
				requeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeue).To(BeFalse())

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

					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
					requeue, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(requeue).To(BeFalse())

					Expect(listJobs(namespace)).To(HaveLen(2))

					labelPromiseForManualReconciliation("redis")

					workflowPipelines, uPromise = setupTest(promise, pipelines)
				})

				It("re-triggers all the pipelines in the workflow", func() {
					var jobs []batchv1.Job
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
					By("re-triggering the first pipeline on the next reconciliation", func() {
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeFalse())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(3))
						Expect(jobs[0].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[0].Name))
						Expect(jobs[1].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[1].Name))
						Expect(jobs[2].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[0].Name))
					})

					By("removing the label from the parent after the first reconcilation", func() {
						promise := v1alpha1.Promise{}
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &promise)).To(Succeed())
						Expect(promise.GetLabels()).NotTo(HaveKey("kratix.io/manual-reconciliation"))
					})

					By("handling cases where label gets added mid-flight on the first pipeline", func() {
						labelPromiseForManualReconciliation("redis")

						workflowPipelines, uPromise = setupTest(promise, pipelines)
						opts = workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
					})

					By("waiting for the first pipeline to complete", func() {
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(3))
					})

					markJobAsComplete(jobs[2].Name)

					By("removing the manual reconciliation label again", func() {
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeFalse())

						promise := v1alpha1.Promise{}
						Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &promise)).To(Succeed())
						Expect(promise.GetLabels()).NotTo(HaveKey("kratix.io/manual-reconciliation"))
					})

					By("restarting from pipeline 0, respecting the label added mid-flight", func() {
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(4))
						Expect(jobs[3].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[0].Name))
					})

					By("waiting for the first pipeline to complete again", func() {
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(4))
					})

					markJobAsComplete(jobs[3].Name)
					By("triggering the second pipeline when the previous completes", func() {
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(5))
						Expect(jobs[4].GetLabels()).To(HaveKeyWithValue("kratix.io/pipeline-name", workflowPipelines[1].Name))
					})

					By("waiting for the second pipeline to complete", func() {
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeTrue())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(5))
					})

					By("marking it all as completed once the last pipeline completes", func() {
						markJobAsComplete(jobs[4].Name)
						requeue, err := workflow.ReconcileConfigure(opts)
						Expect(err).NotTo(HaveOccurred())
						Expect(requeue).To(BeFalse())

						jobs = resourceutil.SortJobsByCreationDateTime(listJobs(namespace), true)
						Expect(jobs).To(HaveLen(5))
					})
				})
			})

			When("there is a job in progress", func() {
				var requeue bool

				BeforeEach(func() {
					var err error

					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					markJobAsComplete(workflowPipelines[0].Job.Name)

					// Create the second job, but don't mark it as complete
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[1].Job)).To(Succeed())

					Expect(listJobs(namespace)).To(HaveLen(2))

					labelPromiseForManualReconciliation("redis")

					workflowPipelines, uPromise = setupTest(promise, pipelines)

					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")

					requeue, err = workflow.ReconcileConfigure(opts)
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

				It("requeues", func() {
					Expect(requeue).To(BeTrue())
				})

				It("does not remove the manual reconciliation label from the parent", func() {
					promise := v1alpha1.Promise{}
					Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: "redis"}, &promise)).To(Succeed())
					Expect(promise.GetLabels()).To(HaveKey("kratix.io/manual-reconciliation"))
				})
			})
		})
	})

	Describe("ReconcileDelete", func() {
		When("there are no pipelines to reconcile", func() {
			It("considers the workflow as completed", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, nil, []v1alpha1.PipelineJobResources{}, "promise")
				requeue, err := workflow.ReconcileDelete(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(requeue).To(BeFalse())
			})
		})

		When("there are pipelines to reconcile", func() {
			It("reconciles the first pipeline", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
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
			})

			When("the first pipeline is completed", func() {
				BeforeEach(func() {
					Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
					markJobAsComplete(workflowPipelines[0].Job.Name)
				})

				It("considers the workflow as completed", func() {
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
					requeue, err := workflow.ReconcileDelete(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(requeue).To(BeFalse())
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(1))

					Expect(findByName(jobList, workflowPipelines[0].Job.Name)).To(BeTrue())
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
		work.Labels = map[string]string{}
		resourceutil.SetPromiseWorkLabels(work.Labels, promiseName, pipeline.Name)
		Expect(fakeK8sClient.Create(ctx, &work)).To(Succeed())
	}
}

func createStaticDependencyWork(promiseName string) {
	work := v1alpha1.Work{}
	work.Name = fmt.Sprintf("static-deps-%s", uuid.New().String()[0:5])
	work.Spec.PromiseName = promiseName
	work.Namespace = namespace
	work.Labels = map[string]string{}
	resourceutil.SetPromiseWorkLabels(work.Labels, promiseName, "")
	Expect(fakeK8sClient.Create(ctx, &work)).To(Succeed())
}

func setupTest(promise v1alpha1.Promise, pipelines []v1alpha1.Pipeline) ([]v1alpha1.PipelineJobResources, *unstructured.Unstructured) {
	var err error
	p := v1alpha1.Promise{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.GetName()}, &p)).To(Succeed())
	uPromise, err := p.ToUnstructured()
	Expect(err).NotTo(HaveOccurred())

	resourceutil.MarkPipelineAsRunning(logger, uPromise)
	Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())

	jobs := []*batchv1.Job{}
	otherResources := [][]client.Object{}
	for _, p := range pipelines {
		generatedResources, err := p.ForPromise(&promise, v1alpha1.WorkflowActionConfigure).Resources(nil)
		Expect(err).NotTo(HaveOccurred())
		jobs = append(jobs, generatedResources.Job)
		otherResources = append(otherResources, generatedResources.RequiredResources)
	}

	workflowPipelines := []v1alpha1.PipelineJobResources{}
	for i, j := range jobs {
		j.SetCreationTimestamp(nextTimestamp())
		workflowPipelines = append(workflowPipelines, v1alpha1.PipelineJobResources{
			Name:              j.GetLabels()["kratix.io/pipeline-name"],
			Job:               j,
			RequiredResources: otherResources[i],
		})
	}

	return workflowPipelines, uPromise
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
