package workflow_test

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/pipeline"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var namespace = "kratix-platform-system"

var _ = Describe("ReconcileConfigure", func() {
	var promise v1alpha1.Promise
	var workflowPipelines []workflow.Pipeline
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

	When("no pipeline for the workflow was executed", func() {
		It("creates a new job with the first pipeline job spec", func() {
			opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "test")
			complete, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(complete).To(BeFalse())
			jobList := listJobs(namespace)
			Expect(jobList).To(HaveLen(1))
			Expect(jobList[0].Name).To(Equal(workflowPipelines[0].Job.Name))
		})
	})

	When("there are jobs for this workflow", func() {
		BeforeEach(func() {
			j := workflowPipelines[0].Job
			Expect(fakeK8sClient.Create(ctx, j)).To(Succeed())
		})

		Context("and the job is in progress", func() {
			var completed bool

			BeforeEach(func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "test")
				var err error
				completed, err = workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
			})

			It("doesn't create a new job", func() {
				Expect(listJobs(namespace)).To(HaveLen(1))
			})

			It("returns false", func() {
				Expect(completed).To(BeFalse())
			})
		})

		Context("and the job is completed", func() {
			BeforeEach(func() {
				markJobAsComplete(workflowPipelines[0].Job.Name)
			})

			It("triggers the next pipeline in the workflow", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "test")
				completed, err := workflow.ReconcileConfigure(opts)
				Expect(completed).To(BeFalse())
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
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "test")
					completed, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(listJobs(namespace)).To(HaveLen(2))
					Expect(completed).To(BeTrue())
				})
			})
		})
	})

	When("the promise spec is updated", func() {
		var updatedWorkflowPipeline []workflow.Pipeline

		BeforeEach(func() {
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
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "test")
				completed, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				jobList := listJobs(namespace)
				Expect(jobList).To(HaveLen(1))
				Expect(completed).To(BeFalse())

				Expect(findByName(jobList, updatedWorkflowPipeline[0].Job.Name)).To(BeTrue())
			})
		})

		When("there are jobs for the promise at this spec", func() {
			var originalWorkflowPipelines []workflow.Pipeline

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
				promise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{}
				Expect(fakeK8sClient.Update(ctx, &promise)).To(Succeed())

				originalWorkflowPipelines, uPromise = setupTest(promise, pipelines)
			})

			Context("but they are not the most recent", func() {
				It("re-runs all pipelines in the workflow", func() {
					// Reconcile with the *original* pipelines and promise spec
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, originalWorkflowPipelines, "test")
					completed, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(completed).To(BeFalse())

					// Expect the original 2 jobs, the updated 2 jobs, and the first job
					// from re-running the first pipeline again on this reconciliation
					jobList := listJobs(namespace)
					Expect(jobList).To(HaveLen(5))

					markJobAsComplete(originalWorkflowPipelines[0].Job.Name)

					completed, err = workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					Expect(completed).To(BeFalse())
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
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "test")
				completed, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				jobList := listJobs(namespace)
				Expect(len(jobList)).To(Equal(1))
				Expect(completed).To(BeFalse())
			})

			It("suspends the previous job", func() {
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "test")
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
					opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "test")
					completed, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					jobList = listJobs(namespace)
					Expect(completed).To(BeFalse())
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

			completed, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(completed).To(BeFalse())

			markJobAsComplete(workflowPipelines[0].Job.Name)

			completed, err = workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(completed).To(BeFalse())
		})

		When("there are still workflows to execute", func() {
			It("doesn't delete the promise scheduling config map", func() {
				configMap := &v1.ConfigMap{}
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
					Name: "destination-selectors-redis", Namespace: namespace},
					configMap,
				)).To(Succeed())
			})
		})

		When("all workflows are executed", func() {
			BeforeEach(func() {
				markJobAsComplete(workflowPipelines[1].Job.Name)
				completed, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(completed).To(BeTrue())
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
		It("deletes the oldests jobs", func() {
			var updatedPromise v1alpha1.Promise
			for i := range 6 {
				Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, &updatedPromise)).To(Succeed())
				updatedPromise.Spec.DestinationSelectors = []v1alpha1.PromiseScheduling{{
					MatchLabels: map[string]string{"app": fmt.Sprintf("redis-%d", i)},
				}}
				Expect(fakeK8sClient.Update(ctx, &updatedPromise)).To(Succeed())

				updatedWorkflowPipeline, uPromise := setupTest(updatedPromise, pipelines)
				opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "test")
				for j := range 2 {
					_, err := workflow.ReconcileConfigure(opts)
					Expect(err).NotTo(HaveOccurred())
					markJobAsComplete(updatedWorkflowPipeline[j].Job.Name)
				}
			}
			updatedWorkflowPipeline, uPromise := setupTest(updatedPromise, pipelines)
			opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflowPipeline, "test")
			_, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())

			jobList := listJobs(namespace)
			Expect(len(jobList)).To(Equal(5 * len(updatedWorkflowPipeline)))
		})
	})

	When("all pipelines have executed", func() {
		var updatedWorkflows []workflow.Pipeline

		BeforeEach(func() {
			Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
			Expect(fakeK8sClient.Create(ctx, workflowPipelines[1].Job)).To(Succeed())
			markJobAsComplete(workflowPipelines[0].Job.Name)
			markJobAsComplete(workflowPipelines[1].Job.Name)

			createFakeWorks(pipelines, promise.Name)

			opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, workflowPipelines, "promise")
			completed, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(completed).To(BeTrue())

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
			completed, err = workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(completed).To(BeTrue())

			createFakeWorks(updatedPipelines, promise.Name)
			createFakeWorks(pipelines, "not-redis")
			createStaticDependencyWork(promise.Name)
		})

		It("cleans up any leftover works from previous runs", func() {
			opts := workflow.NewOpts(ctx, fakeK8sClient, logger, uPromise, updatedWorkflows, "promise")
			completed, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(completed).To(BeTrue())

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

func setupTest(promise v1alpha1.Promise, pipelines []v1alpha1.Pipeline) ([]workflow.Pipeline, *unstructured.Unstructured) {
	var err error
	uPromise, err := promise.ToUnstructured()
	Expect(err).NotTo(HaveOccurred())

	jobs := []*batchv1.Job{}
	otherResources := [][]client.Object{}
	for _, p := range pipelines {
		generatedResources, err := pipeline.NewConfigurePromise(
			uPromise, p, promise.Name, nil, logger,
		)
		Expect(err).NotTo(HaveOccurred())
		jobs = append(jobs, generatedResources[4].(*batchv1.Job))
		otherResources = append(otherResources, generatedResources[0:4])
	}

	workflowPipelines := []workflow.Pipeline{}
	for i, j := range jobs {
		workflowPipelines = append(workflowPipelines, workflow.Pipeline{
			JobRequiredResources: otherResources[i],
			Job:                  j,
			Name:                 j.GetLabels()["kratix.io/pipeline-name"],
		})
	}
	return workflowPipelines, uPromise
}

var callCount = 0

func markJobAsComplete(name string) {
	callCount++
	//Fake library doesn't set timestamp, and we need it set for comparing age
	//of jobs. This ensures its set once, and only when its first created, and
	//that they differ by a large enough amont (time.Now() alone was not enough)

	job := &batchv1.Job{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, job)).To(Succeed())

	job.CreationTimestamp = metav1.NewTime(time.Now().Add(time.Duration(callCount) * time.Minute))
	Expect(fakeK8sClient.Update(ctx, job)).To(Succeed())

	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, job)).To(Succeed())

	job.Status.Conditions = []batchv1.JobCondition{
		{
			Type:   batchv1.JobComplete,
			Status: v1.ConditionTrue,
		},
	}
	job.Status.Succeeded = 1

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
