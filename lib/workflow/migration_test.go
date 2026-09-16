package workflow_test

// Tests for the one-off upgrade migration in migration.go. Delete this whole file
// when the migration is retired.

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/workflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
)

var _ = Describe("Workflow status migration", func() {
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

		workflowPipelines, uPromise = setupTest(promise, pipelines)
	})

	// setupResourceRequestWithPreKeyedStatus creates a resource request holding the
	// status a real apiserver serves after an upgrade: the flat entries are pruned
	// against the keyed schema, so their names and phases are already gone.
	setupResourceRequestWithPreKeyedStatus := func(name string, action v1alpha1.Action) (*unstructured.Unstructured, []v1alpha1.PipelineJobResources) {
		api, err := json.Marshal(fakeCRD)
		Expect(err).NotTo(HaveOccurred())
		promise.Spec.API = &runtime.RawExtension{Raw: api}

		rr := &unstructured.Unstructured{}
		rr.SetAPIVersion("mygroup.example/v1")
		rr.SetKind("TheKind")
		rr.SetName(name)
		rr.SetNamespace(namespace)
		rr.SetLabels(map[string]string{v1alpha1.PromiseNameLabel: promise.Name})
		Expect(fakeK8sClient.Create(ctx, rr)).To(Succeed())

		resources := make([]v1alpha1.PipelineJobResources, len(pipelines))
		for i, pipeline := range pipelines {
			resources[i], err = pipeline.ForResource(&promise, action, rr).Resources(nil)
			Expect(err).NotTo(HaveOccurred())
			resources[i].Job.SetCreationTimestamp(nextTimestamp())
		}

		prunedByTheApiserver := []any{map[string]any{}, map[string]any{}}
		Expect(unstructured.SetNestedSlice(rr.Object, prunedByTheApiserver, "status", "kratix", "workflows", "pipelines")).To(Succeed())
		Expect(fakeK8sClient.Status().Update(ctx, rr)).To(Succeed())
		return rr, resources
	}

	DescribeTable("rebuilds pre-keyed status from the remaining Jobs", func(retainJob bool, expectedPipeline int) {
		// The apiserver prunes the old flat entries against the keyed schema, so
		// empty objects are what a real cluster serves after an upgrade.
		prunedByTheApiserver := []any{map[string]any{}, map[string]any{}}
		unstructured.RemoveNestedField(uPromise.Object, "status", "kratix", "workflows", "configure")
		Expect(unstructured.SetNestedSlice(uPromise.Object, prunedByTheApiserver, "status", "kratix", "workflows", "pipelines")).To(Succeed())
		Expect(unstructured.SetNestedField(uPromise.Object, int64(2), "status", "kratix", "workflows", "suspendedGeneration")).To(Succeed())
		Expect(unstructured.SetNestedField(uPromise.Object, "2026-01-01T00:00:00Z", "status", "kratix", "workflows", "lastSuccessfulConfigureWorkflowTime")).To(Succeed())
		if retainJob {
			Expect(fakeK8sClient.Create(ctx, workflowPipelines[0].Job)).To(Succeed())
			markJobAsComplete(workflowPipelines[0].Job.Name)
		}
		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, workflowPipelines, "promise", 0, namespace)
		requeue, err := workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(requeue).To(BeTrue())
		workflows, _, err := unstructured.NestedMap(uPromise.Object, "status", "kratix", "workflows")
		Expect(err).NotTo(HaveOccurred())
		Expect(workflows).NotTo(HaveKey("pipelines"))
		Expect(workflows).NotTo(HaveKey("suspendedGeneration"))
		Expect(workflows).NotTo(HaveKey("lastSuccessfulConfigureWorkflowTime"))
		for _, job := range listJobs(namespace) {
			Expect(fakeK8sClient.Delete(ctx, &job)).To(Succeed())
		}
		resetWorkflowPipelineJobs(workflowPipelines)
		_, err = workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(listJobs(namespace)).To(ConsistOf(HaveField("Name", workflowPipelines[expectedPipeline].Job.Name)))
	},
		Entry("carries on past a pipeline whose Job is still there", true, 1),
		Entry("runs from the start when no Job is left", false, 0),
	)

	// Resource requests are the path that matters on upgrade. A Promise never
	// sees the pre-keyed fields, because WorkflowStatuses.UnmarshalJSON drops
	// them while decoding.
	It("rebuilds a resource request's status from the Jobs that are left", func() {
		rr, resources := setupResourceRequestWithPreKeyedStatus("upgraded-request", v1alpha1.WorkflowActionConfigure)

		// An earlier, failed run of the same pipeline. Only the newest Job counts.
		earlierJob := resources[0].Job.DeepCopy()
		earlierJob.SetName(resources[0].Job.GetName() + "-earlier")
		Expect(fakeK8sClient.Create(ctx, earlierJob)).To(Succeed())
		markJobAsFailed(earlierJob.GetName())

		resources[0].Job.SetCreationTimestamp(nextTimestamp())
		Expect(fakeK8sClient.Create(ctx, resources[0].Job)).To(Succeed())
		markJobAsComplete(resources[0].Job.Name)

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, rr, resources, "resource", 5, namespace)
		requeue, err := workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(requeue).To(BeTrue())

		workflows, _, err := unstructured.NestedMap(rr.Object, "status", "kratix", "workflows")
		Expect(err).NotTo(HaveOccurred())
		Expect(workflows).NotTo(HaveKey("pipelines"))

		rebuilt, found, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "configure", "pipelines")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(rebuilt).To(HaveLen(2))
		Expect(rebuilt[0]).To(SatisfyAll(
			HaveKeyWithValue("name", resources[0].Name),
			HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseSucceeded),
			HaveKeyWithValue("job", resources[0].Job.Name),
			HaveKeyWithValue("hash", resources[0].Job.Labels[v1alpha1.KratixResourceHashLabel]),
		))
		Expect(rebuilt[1]).To(SatisfyAll(
			HaveKeyWithValue("name", resources[1].Name),
			HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
		))

		// The rebuilt status is trusted, so the finished pipeline does not run again.
		_, err = workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(listJobs(namespace)).To(ConsistOf(
			HaveField("Name", earlierJob.Name),
			HaveField("Name", resources[0].Job.Name),
			HaveField("Name", resources[1].Job.Name),
		))
	})

	It("keeps a pipeline that failed before the upgrade failed", func() {
		rr, resources := setupResourceRequestWithPreKeyedStatus("failed-request", v1alpha1.WorkflowActionConfigure)
		Expect(fakeK8sClient.Create(ctx, resources[0].Job)).To(Succeed())
		markJobAsFailed(resources[0].Job.Name)

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, rr, resources, "resource", 5, namespace)
		_, err := workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())

		rebuilt, _, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "configure", "pipelines")
		Expect(err).NotTo(HaveOccurred())
		Expect(rebuilt[0]).To(HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseFailed))

		// The failure is preserved, so nothing runs again until someone asks.
		_, err = workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(listJobs(namespace)).To(ConsistOf(HaveField("Name", resources[0].Job.Name)))
	})

	// The controllers call MigrateStatus before checking for pipelines, so a
	// Promise that defines none must not panic on Resources[0].
	It("removes the pre-keyed status when there are no pipelines to rebuild", func() {
		rr, _ := setupResourceRequestWithPreKeyedStatus("pipelineless-request", v1alpha1.WorkflowActionConfigure)

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, rr, nil, "resource", 5, namespace)
		removed, err := workflow.MigrateStatus(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(BeTrue())

		workflows, _, err := unstructured.NestedMap(rr.Object, "status", "kratix", "workflows")
		Expect(err).NotTo(HaveOccurred())
		Expect(workflows).NotTo(HaveKey("pipelines"))
	})

	It("rebuilds a deleting resource request's status under the delete key", func() {
		rr, resources := setupResourceRequestWithPreKeyedStatus("deleting-request", v1alpha1.WorkflowActionDelete)
		resources = resources[:1]

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, rr, resources, "resource", 5, namespace)
		requeue, err := workflow.ReconcileDelete(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(requeue).To(BeTrue())

		workflows, _, err := unstructured.NestedMap(rr.Object, "status", "kratix", "workflows")
		Expect(err).NotTo(HaveOccurred())
		Expect(workflows).NotTo(HaveKey("pipelines"))
		Expect(workflows).NotTo(HaveKey("configure"))
		Expect(workflows).To(HaveKey("delete"))

		rebuilt, found, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "delete", "pipelines")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(rebuilt).To(ConsistOf(SatisfyAll(
			HaveKeyWithValue("name", resources[0].Name),
			HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
		)))
	})
})
