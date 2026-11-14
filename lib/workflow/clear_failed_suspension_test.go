package workflow_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"

	"github.com/syntasso/kratix/lib/workflow"
)

var _ = Describe("ClearFailedSuspension", func() {
	var (
		promise       v1alpha1.Promise
		uPromise      *unstructured.Unstructured
		eventRecorder *events.FakeRecorder
	)

	pipeline := v1alpha1.Pipeline{
		Kind:       "Pipeline",
		APIVersion: "kratix.io/v1alpha1",
		ObjectMeta: metav1.ObjectMeta{Name: "pipeline-1"},
		Spec: v1alpha1.PipelineSpec{
			Containers: []v1alpha1.Container{{Name: "container-1", Image: "busybox"}},
		},
	}

	BeforeEach(func() {
		eventRecorder = events.NewFakeRecorder(1024)
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pipeline)
		Expect(err).NotTo(HaveOccurred())
		promise = v1alpha1.Promise{
			TypeMeta:   metav1.TypeMeta{APIVersion: "platform.kratix.io/v1alpha1", Kind: "Promise"},
			ObjectMeta: metav1.ObjectMeta{Name: "redis"},
		}
		promise.Spec.Workflows.Promise.Configure = []unstructured.Unstructured{{Object: obj}}
		promise.Spec.Workflows.Promise.Delete = []unstructured.Unstructured{{Object: obj}}
		Expect(fakeK8sClient.Create(ctx, &promise)).To(Succeed())
		uPromise, err = promise.ToUnstructured()
		Expect(err).NotTo(HaveOccurred())
	})

	// suspendedByJob creates the pipeline's Job, records the pipeline in the given
	// phase against it, and labels the Promise as suspended.
	suspendedByJob := func(action v1alpha1.Action, phase string, jobCondition batchv1.JobConditionType) workflow.Opts {
		GinkgoHelper()
		resources, err := pipeline.ForPromise(&promise, action).Resources(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeK8sClient.Create(ctx, resources.Job)).To(Succeed())
		if jobCondition != "" {
			markJobAs(jobCondition, resources.Job.Name)
		}

		Expect(unstructured.SetNestedSlice(uPromise.Object, []any{map[string]any{
			"name":  pipeline.Name,
			"phase": phase,
			"job":   resources.Job.Name,
			"hash":  resources.Job.Labels[v1alpha1.KratixResourceHashLabel],
		}}, "status", "kratix", "workflows", string(action), "pipelines")).To(Succeed())
		Expect(fakeK8sClient.Status().Update(ctx, uPromise)).To(Succeed())
		uPromise.SetLabels(map[string]string{v1alpha1.WorkflowSuspendedLabel: "true"})
		Expect(fakeK8sClient.Update(ctx, uPromise)).To(Succeed())

		return workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise,
			[]v1alpha1.PipelineJobResources{resources}, "promise", 5, namespace)
	}

	storedLabels := func() map[string]string {
		GinkgoHelper()
		stored := &v1alpha1.Promise{}
		Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, stored)).To(Succeed())
		return stored.GetLabels()
	}

	DescribeTable("removes the label when the Job that added it failed before recording the suspension",
		func(action v1alpha1.Action) {
			opts := suspendedByJob(action, v1alpha1.WorkflowPhaseRunning, batchv1.JobFailed)

			cleared, err := workflow.ClearFailedSuspension(opts)

			Expect(err).NotTo(HaveOccurred())
			Expect(cleared).To(BeTrue())
			Expect(storedLabels()).NotTo(HaveKey(v1alpha1.WorkflowSuspendedLabel))
		},
		Entry("configure", v1alpha1.WorkflowActionConfigure),
		Entry("delete", v1alpha1.WorkflowActionDelete),
	)

	It("lets the next configure reconcile record the failed Job", func() {
		opts := suspendedByJob(v1alpha1.WorkflowActionConfigure, v1alpha1.WorkflowPhaseRunning, batchv1.JobFailed)
		_, err := workflow.ClearFailedSuspension(opts)
		Expect(err).NotTo(HaveOccurred())

		_, err = workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())

		pipelines, _, _ := unstructured.NestedSlice(uPromise.Object, "status", "kratix", "workflows", "configure", "pipelines")
		Expect(pipelines).To(ConsistOf(HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseFailed)))
		Expect(eventRecorder.Events).To(Receive(ContainSubstring(v1.EventTypeWarning)))
	})

	It("lets the next delete reconcile record the failed Job", func() {
		opts := suspendedByJob(v1alpha1.WorkflowActionDelete, v1alpha1.WorkflowPhaseRunning, batchv1.JobFailed)
		_, err := workflow.ClearFailedSuspension(opts)
		Expect(err).NotTo(HaveOccurred())

		_, err = workflow.ReconcileDelete(opts)
		Expect(err).To(MatchError(workflow.ErrDeletePipelineFailed))

		pipelines, _, _ := unstructured.NestedSlice(uPromise.Object, "status", "kratix", "workflows", "delete", "pipelines")
		Expect(pipelines).To(ConsistOf(HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseFailed)))
	})

	DescribeTable("keeps the label",
		func(phase string, jobCondition batchv1.JobConditionType) {
			opts := suspendedByJob(v1alpha1.WorkflowActionConfigure, phase, jobCondition)

			cleared, err := workflow.ClearFailedSuspension(opts)

			Expect(err).NotTo(HaveOccurred())
			Expect(cleared).To(BeFalse())
			Expect(storedLabels()).To(HaveKeyWithValue(v1alpha1.WorkflowSuspendedLabel, "true"))
		},
		Entry("while the Job that added it is still running", v1alpha1.WorkflowPhaseRunning, batchv1.JobConditionType("")),
		Entry("once the suspension is recorded", v1alpha1.WorkflowPhaseSuspended, batchv1.JobComplete),
	)

	It("does nothing when the object is not labelled", func() {
		opts := suspendedByJob(v1alpha1.WorkflowActionConfigure, v1alpha1.WorkflowPhaseRunning, batchv1.JobFailed)
		uPromise.SetLabels(nil)
		Expect(fakeK8sClient.Update(ctx, uPromise)).To(Succeed())

		cleared, err := workflow.ClearFailedSuspension(opts)

		Expect(err).NotTo(HaveOccurred())
		Expect(cleared).To(BeFalse())
	})
})
