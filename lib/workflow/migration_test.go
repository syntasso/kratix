package workflow_test

// Tests for the one-off upgrade migration in migration.go. Delete this file with it.

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
	var pipelines []v1alpha1.Pipeline
	var eventRecorder *events.FakeRecorder

	BeforeEach(func() {
		eventRecorder = events.NewFakeRecorder(1024)

		api, err := json.Marshal(fakeCRD)
		Expect(err).NotTo(HaveOccurred())
		promise = v1alpha1.Promise{
			ObjectMeta: metav1.ObjectMeta{Name: "redis"},
			TypeMeta:   metav1.TypeMeta{APIVersion: "platform.kratix.io/v1alpha1", Kind: "Promise"},
		}
		promise.Spec.API = &runtime.RawExtension{Raw: api}

		pipelines = []v1alpha1.Pipeline{{
			Kind:       "Pipeline",
			APIVersion: "kratix.io/v1alpha1",
			ObjectMeta: metav1.ObjectMeta{Name: "pipeline-1"},
			Spec:       v1alpha1.PipelineSpec{Containers: []v1alpha1.Container{{Name: "container-1", Image: "busybox"}}},
		}, {
			Kind:       "Pipeline",
			APIVersion: "kratix.io/v1alpha1",
			ObjectMeta: metav1.ObjectMeta{Name: "pipeline-2"},
			Spec:       v1alpha1.PipelineSpec{Containers: []v1alpha1.Container{{Name: "container-1", Image: "busybox"}}},
		}}
	})

	// An upgraded resource request: the apiserver has already pruned the old flat
	// status entries to empty objects, so their names and phases are gone.
	upgradedResourceRequest := func(name string, action v1alpha1.Action) (*unstructured.Unstructured, []v1alpha1.PipelineJobResources) {
		rr := &unstructured.Unstructured{}
		rr.SetAPIVersion("mygroup.example/v1")
		rr.SetKind("TheKind")
		rr.SetName(name)
		rr.SetNamespace(namespace)
		rr.SetLabels(map[string]string{v1alpha1.PromiseNameLabel: promise.Name})
		Expect(fakeK8sClient.Create(ctx, rr)).To(Succeed())

		resources := make([]v1alpha1.PipelineJobResources, len(pipelines))
		for i, pipeline := range pipelines {
			var err error
			resources[i], err = pipeline.ForResource(&promise, action, rr).Resources(nil)
			Expect(err).NotTo(HaveOccurred())
			resources[i].Job.SetCreationTimestamp(nextTimestamp())
		}

		pruned := []any{map[string]any{}, map[string]any{}}
		Expect(unstructured.SetNestedSlice(rr.Object, pruned, "status", "kratix", "workflows", "pipelines")).To(Succeed())
		Expect(unstructured.SetNestedField(rr.Object, int64(2), "status", "kratix", "workflows", "suspendedGeneration")).To(Succeed())
		Expect(fakeK8sClient.Status().Update(ctx, rr)).To(Succeed())
		return rr, resources
	}

	It("rebuilds the status from the newest Job of each pipeline", func() {
		rr, resources := upgradedResourceRequest("upgraded-request", v1alpha1.WorkflowActionConfigure)

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
		Expect(workflows).NotTo(HaveKey("suspendedGeneration"))

		rebuilt, _, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "configure", "pipelines")
		Expect(err).NotTo(HaveOccurred())
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

		_, err = workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(listJobs(namespace)).To(ConsistOf(
			HaveField("Name", earlierJob.Name),
			HaveField("Name", resources[0].Job.Name),
			HaveField("Name", resources[1].Job.Name),
		))
	})

	It("keeps a pipeline that failed before the upgrade failed", func() {
		rr, resources := upgradedResourceRequest("failed-request", v1alpha1.WorkflowActionConfigure)
		Expect(fakeK8sClient.Create(ctx, resources[0].Job)).To(Succeed())
		markJobAsFailed(resources[0].Job.Name)

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, rr, resources, "resource", 5, namespace)
		_, err := workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())

		rebuilt, _, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "configure", "pipelines")
		Expect(err).NotTo(HaveOccurred())
		Expect(rebuilt[0]).To(HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseFailed))

		_, err = workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(listJobs(namespace)).To(ConsistOf(HaveField("Name", resources[0].Job.Name)))
	})

	It("records the rebuilt status under the delete key", func() {
		rr, resources := upgradedResourceRequest("deleting-request", v1alpha1.WorkflowActionDelete)
		resources = resources[:1]

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, rr, resources, "resource", 5, namespace)
		requeue, err := workflow.ReconcileDelete(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(requeue).To(BeTrue())

		workflows, _, err := unstructured.NestedMap(rr.Object, "status", "kratix", "workflows")
		Expect(err).NotTo(HaveOccurred())
		Expect(workflows).NotTo(HaveKey("pipelines"))
		Expect(workflows).NotTo(HaveKey("configure"))

		rebuilt, _, err := unstructured.NestedSlice(rr.Object, "status", "kratix", "workflows", "delete", "pipelines")
		Expect(err).NotTo(HaveOccurred())
		Expect(rebuilt).To(ConsistOf(SatisfyAll(
			HaveKeyWithValue("name", resources[0].Name),
			HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
		)))
	})

	It("removes the status when there are no pipelines to rebuild", func() {
		rr, _ := upgradedResourceRequest("pipelineless-request", v1alpha1.WorkflowActionConfigure)

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, rr, nil, "resource", 5, namespace)
		removed, err := workflow.MigrateStatus(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(removed).To(BeTrue())

		workflows, _, err := unstructured.NestedMap(rr.Object, "status", "kratix", "workflows")
		Expect(err).NotTo(HaveOccurred())
		Expect(workflows).NotTo(HaveKey("pipelines"))
	})
})
