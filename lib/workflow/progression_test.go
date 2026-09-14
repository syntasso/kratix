package workflow_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Configure workflow progression", func() {
	var promise v1alpha1.Promise
	var pipelines []v1alpha1.Pipeline
	var workflowPipelines []v1alpha1.PipelineJobResources
	var uPromise *unstructured.Unstructured
	var opts workflow.Opts

	BeforeEach(func() {
		promise, pipelines = promiseWithTwoConfigurePipelines()
		Expect(fakeK8sClient.Create(ctx, &promise)).To(Succeed())
		workflowPipelines, uPromise = setupTest(promise, pipelines)
		opts = workflow.NewOpts(ctx, fakeK8sClient, events.NewFakeRecorder(1024), logger, uPromise,
			workflowPipelines, "promise", 5, namespace)
	})

	When("the status says a pipeline succeeded and its Job is gone", func() {
		It("runs the next pipeline instead of starting again", func() {
			recordPipelines(uPromise,
				succeededPipeline(workflowPipelines[0]),
				pendingPipeline(workflowPipelines[1]))

			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			jobs := listJobs(namespace)
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Name).To(Equal(workflowPipelines[1].Job.Name))

			Expect(recordedPipelines(uPromise)[0].Phase).To(Equal(v1alpha1.WorkflowPhaseSucceeded))
			Expect(recordedPipelines(uPromise)[1].Phase).To(Equal(v1alpha1.WorkflowPhaseRunning))
		})
	})

	When("every pipeline has succeeded", func() {
		BeforeEach(func() {
			recordPipelines(uPromise,
				succeededPipeline(workflowPipelines[0]),
				succeededPipeline(workflowPipelines[1]))
		})

		It("creates no Jobs", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeFalse())
			Expect(listJobs(namespace)).To(BeEmpty())
		})

		It("runs from the first pipeline again when another run is asked for", func() {
			labelPromiseForManualReconciliation(promise.GetName())
			Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(&promise), uPromise)).To(Succeed())

			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			jobs := listJobs(namespace)
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Name).To(Equal(workflowPipelines[0].Job.Name))

			Expect(recordedPipelines(uPromise)[0].Phase).To(Equal(v1alpha1.WorkflowPhaseRunning))
			Expect(recordedPipelines(uPromise)[1].Phase).To(Equal(v1alpha1.WorkflowPhasePending))
		})
	})

	When("the Job of the running pipeline is still going", func() {
		It("does not create a second Job", func() {
			recordPipelines(uPromise,
				runningPipeline(workflowPipelines[0]),
				pendingPipeline(workflowPipelines[1]))
			Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())

			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(listJobs(namespace)).To(HaveLen(1))
		})
	})

	When("the status says a pipeline is running but its Job is gone", func() {
		It("runs the pipeline again", func() {
			recordPipelines(uPromise,
				runningPipeline(workflowPipelines[0]),
				pendingPipeline(workflowPipelines[1]))

			_, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())

			jobs := listJobs(namespace)
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Name).To(Equal(workflowPipelines[0].Job.Name))
		})
	})

	When("the object's spec changed while a pipeline was running", func() {
		It("runs that pipeline again rather than crediting the finished run with the new spec", func() {
			runningAtOldSpec := runningPipeline(workflowPipelines[0])
			runningAtOldSpec.Hash = "an-older-spec"
			recordPipelines(uPromise, runningAtOldSpec, pendingPipeline(workflowPipelines[1]))

			Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
			markJobAsComplete(workflowPipelines[0].Job.Name)

			_, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())

			Expect(recordedPipelines(uPromise)[0].Phase).To(Equal(v1alpha1.WorkflowPhaseRunning))
			Expect(recordedPipelines(uPromise)[0].Hash).To(Equal(workflow.RunHash(workflowPipelines[0])))
			Expect(recordedPipelines(uPromise)[1].Phase).To(Equal(v1alpha1.WorkflowPhasePending))
		})
	})

	When("the object's spec changed since the pipelines succeeded", func() {
		It("runs every pipeline again", func() {
			recordPipelines(uPromise,
				withHash(succeededPipeline(workflowPipelines[0]), "an-older-spec"),
				withHash(succeededPipeline(workflowPipelines[1]), "an-older-spec"))

			_, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())

			jobs := listJobs(namespace)
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Name).To(Equal(workflowPipelines[0].Job.Name))
		})
	})

	When("a Job failed but the object has nothing recorded yet", func() {
		It("runs the pipeline rather than reporting a failure it has no record of", func() {
			Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
			markJobAsFailed(workflowPipelines[0].Job.Name)

			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(recordedPipelines(uPromise)[0].Phase).To(Equal(v1alpha1.WorkflowPhaseRunning))
		})
	})

	When("the recorded run finished but a Job of an earlier run is the only one left", func() {
		It("waits for its own run instead of taking the earlier Job as its own", func() {
			earlierRun := workflowPipelines[0].Job.DeepCopy()
			earlierRun.SetName(earlierRun.GetName() + "-earlier")
			Expect(fakeK8sClient.Create(ctx, earlierRun)).To(Succeed())
			markJobAsComplete(earlierRun.GetName())

			recordPipelines(uPromise,
				runningPipeline(workflowPipelines[0]),
				pendingPipeline(workflowPipelines[1]))

			_, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())

			Expect(recordedPipelines(uPromise)[0].Phase).To(Equal(v1alpha1.WorkflowPhaseRunning))
			Expect(recordedPipelines(uPromise)[1].Phase).To(Equal(v1alpha1.WorkflowPhasePending))
		})
	})

	When("the object's spec changed after a pipeline failed", func() {
		It("runs the pipeline for the new spec instead of reporting the old failure again", func() {
			failedAtOldSpec := runningPipeline(workflowPipelines[0])
			failedAtOldSpec.Phase = v1alpha1.WorkflowPhaseFailed
			failedAtOldSpec.Hash = "an-older-spec"
			recordPipelines(uPromise, failedAtOldSpec, pendingPipeline(workflowPipelines[1]))

			Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
			markJobAsFailed(workflowPipelines[0].Job.Name)

			_, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())

			Expect(recordedPipelines(uPromise)[0].Phase).To(Equal(v1alpha1.WorkflowPhaseRunning))
			Expect(recordedPipelines(uPromise)[0].Hash).To(Equal(workflow.RunHash(workflowPipelines[0])))
		})
	})

	When("the Job of the running pipeline failed", func() {
		It("stops the workflow and records the failure", func() {
			recordPipelines(uPromise,
				runningPipeline(workflowPipelines[0]),
				pendingPipeline(workflowPipelines[1]))
			Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
			markJobAsFailed(workflowPipelines[0].Job.Name)

			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(recordedPipelines(uPromise)[0].Phase).To(Equal(v1alpha1.WorkflowPhaseFailed))
			Expect(recordedPipelines(uPromise)[1].Phase).To(Equal(v1alpha1.WorkflowPhasePending))
			Expect(listJobs(namespace)).To(HaveLen(1))
		})
	})
})

func recordPipelines(parent *unstructured.Unstructured, statuses ...v1alpha1.WorkflowPipelineStatus) {
	GinkgoHelper()
	Expect(resourceutil.SetPipelineStatuses(parent, string(v1alpha1.WorkflowActionConfigure), statuses)).To(Succeed())
	Expect(fakeK8sClient.Status().Update(ctx, parent)).To(Succeed())
}

func recordedPipelines(parent *unstructured.Unstructured) []v1alpha1.WorkflowPipelineStatus {
	GinkgoHelper()
	Expect(fakeK8sClient.Get(ctx, client.ObjectKeyFromObject(parent), parent)).To(Succeed())
	statuses, err := resourceutil.GetPipelineStatuses(parent, string(v1alpha1.WorkflowActionConfigure))
	Expect(err).NotTo(HaveOccurred())
	return statuses
}

func succeededPipeline(pipeline v1alpha1.PipelineJobResources) v1alpha1.WorkflowPipelineStatus {
	return v1alpha1.WorkflowPipelineStatus{
		Name:               pipeline.Name,
		Phase:              v1alpha1.WorkflowPhaseSucceeded,
		Hash:               workflow.RunHash(pipeline),
		LastTransitionTime: metav1.Now(),
	}
}

func runningPipeline(pipeline v1alpha1.PipelineJobResources) v1alpha1.WorkflowPipelineStatus {
	status := succeededPipeline(pipeline)
	status.Phase = v1alpha1.WorkflowPhaseRunning
	status.Job = pipeline.Job.GetName()
	return status
}

func pendingPipeline(pipeline v1alpha1.PipelineJobResources) v1alpha1.WorkflowPipelineStatus {
	return v1alpha1.WorkflowPipelineStatus{
		Name:               pipeline.Name,
		Phase:              v1alpha1.WorkflowPhasePending,
		LastTransitionTime: metav1.Now(),
	}
}

func withHash(status v1alpha1.WorkflowPipelineStatus, hash string) v1alpha1.WorkflowPipelineStatus {
	status.Hash = hash
	return status
}

// promiseWithTwoConfigurePipelines returns a Promise whose configure workflow
// has two pipelines, and the pipelines themselves.
func promiseWithTwoConfigurePipelines() (v1alpha1.Promise, []v1alpha1.Pipeline) {
	GinkgoHelper()
	promise := v1alpha1.Promise{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres"},
		TypeMeta: metav1.TypeMeta{
			APIVersion: "platform.kratix.io/v1alpha1",
			Kind:       "Promise",
		},
	}

	pipelines := []v1alpha1.Pipeline{
		newPipeline("pipeline-1"),
		newPipeline("pipeline-2"),
	}

	promise.Spec.Workflows.Promise.Configure = make([]unstructured.Unstructured, len(pipelines))
	for i, pipeline := range pipelines {
		object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pipeline)
		Expect(err).NotTo(HaveOccurred())
		promise.Spec.Workflows.Promise.Configure[i] = unstructured.Unstructured{Object: object}
	}

	return promise, pipelines
}

func newPipeline(name string) v1alpha1.Pipeline {
	return v1alpha1.Pipeline{
		Kind:       "Pipeline",
		APIVersion: "kratix.io/v1alpha1",
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.PipelineSpec{
			Containers: []v1alpha1.Container{{Name: "container-1", Image: "busybox"}},
		},
	}
}
