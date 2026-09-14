package workflow_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/resourceutil"
	"github.com/syntasso/kratix/lib/workflow"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// status.kratix.workflows.<key>.pipelines is what says how far a workflow has
// got; Jobs are only evidence that moves those entries along. Reading the Jobs
// as the record of progress instead restarts the workflow from its first
// pipeline every time they are pruned, deleted, or never created.
var _ = Describe("Workflow progression", func() {
	var (
		promise       v1alpha1.Promise
		pipelines     []v1alpha1.Pipeline
		resources     []v1alpha1.PipelineJobResources
		parent        *unstructured.Unstructured
		eventRecorder *events.FakeRecorder
	)

	newOpts := func() workflow.Opts {
		return workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, parent, resources, "promise", 5, namespace)
	}

	BeforeEach(func() {
		eventRecorder = events.NewFakeRecorder(1024)
		promise, pipelines = progressionPromise("pipeline-1", "pipeline-2")
		resources, parent = setupTest(promise, pipelines)
	})

	Describe("a workflow whose pipelines have all succeeded", func() {
		BeforeEach(func() {
			markConfigureWorkflowCompleted(parent)
			writePipelineStatuses(parent, configureKey,
				settledEntry(resources[0]),
				settledEntry(resources[1]),
			)
		})

		It("runs nothing and writes nothing when its Jobs are gone", func() {
			Expect(listJobs(namespace)).To(BeEmpty())
			opts := newOpts()

			var before *unstructured.Unstructured
			By("reporting the workflow complete without touching anything", func() {
				before = storedParent(parent)
				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeFalse())
				Expect(listJobs(namespace)).To(BeEmpty())
				Expect(storedParent(parent)).To(Equal(before))
			})

			By("staying quiet on the reconciliation after that", func() {
				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeFalse())
				Expect(listJobs(namespace)).To(BeEmpty())
				Expect(storedParent(parent)).To(Equal(before))
			})
		})
	})

	It("reports the delete workflow complete when its status says so and its Job is gone", func() {
		deleteResources := deletePipelineResources(promise, pipelines[:1])
		writePipelineStatuses(parent, deleteKey, settledEntry(deleteResources[0]))

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, parent, deleteResources, "promise", 5, namespace)
		passiveRequeue, err := workflow.ReconcileDelete(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeFalse())
		Expect(listJobs(namespace)).To(BeEmpty())
	})

	Describe("the delete lane", func() {
		var deleteResources []v1alpha1.PipelineJobResources

		newDeleteOpts := func() workflow.Opts {
			return workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, parent, deleteResources, "promise", 5, namespace)
		}

		BeforeEach(func() {
			deleteResources = deletePipelineResources(promise, pipelines[:1])
		})

		It("re-runs the delete pipeline when its recorded run is of an older definition", func() {
			writePipelineStatuses(parent, deleteKey, succeededEntry(deleteResources[0], "a-hash-from-an-older-definition"))

			passiveRequeue, err := workflow.ReconcileDelete(newDeleteOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(jobNames()).To(ContainElement(deleteResources[0].Job.Name))
		})

		It("clears the retry bookkeeping when the delete pipeline is re-run by hand", func() {
			writePipelineStatuses(parent, deleteKey, map[string]any{
				"name":        deleteResources[0].Name,
				"phase":       v1alpha1.WorkflowPhaseSuspended,
				"message":     "waiting for approval",
				"attempts":    int64(3),
				"nextRetryAt": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
			})
			parent.SetLabels(map[string]string{resourceutil.ManualReconciliationLabel: "true"})
			Expect(fakeK8sClient.Update(ctx, parent)).To(Succeed())

			passiveRequeue, err := workflow.ReconcileDelete(newDeleteOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(storedPipelineStatuses(parent, deleteKey)).To(ConsistOf(SatisfyAll(
				HaveKeyWithValue("name", deleteResources[0].Name),
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseRunning),
				Not(HaveKey("attempts")),
				Not(HaveKey("nextRetryAt")),
			)))
		})

	})

	It("runs only the pipeline whose status records no run, not the whole workflow", func() {
		writePipelineStatuses(parent, configureKey,
			settledEntry(resources[0]),
			pendingEntry(resources[1]),
		)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		jobs := listJobs(namespace)
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].GetLabels()).To(HaveKeyWithValue(v1alpha1.PipelineNameLabel, "pipeline-2"))
	})

	It("re-runs from the first pipeline and unwinds the later entries when the definitions change", func() {
		writePipelineStatuses(parent, configureKey,
			succeededEntry(resources[0], "a-hash-from-an-older-definition"),
			succeededEntry(resources[1], "a-hash-from-an-older-definition"),
		)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		By("running the first pipeline", func() {
			jobs := listJobs(namespace)
			Expect(jobs).To(HaveLen(1))
			Expect(jobs[0].Name).To(Equal(resources[0].Job.Name))
		})

		By("returning the pipelines behind it to Pending, so the stale hash cannot settle them", func() {
			Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
				SatisfyAll(
					HaveKeyWithValue("name", "pipeline-1"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseRunning),
				),
				SatisfyAll(
					HaveKeyWithValue("name", "pipeline-2"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
					Not(HaveKey("hash")),
				),
			))
		})
	})

	It("does not start a second Job while another pipeline of the workflow is still running", func() {
		writePipelineStatuses(parent, configureKey,
			succeededEntry(resources[0], "a-hash-from-an-older-definition"),
			runningEntry(resources[1]),
		)
		outdated := copyJob(resources[1].Job, "outdated")
		outdated.Labels[v1alpha1.KratixResourceHashLabel] = "a-hash-from-an-older-definition"
		createJob(outdated, jobRunning)

		By("waiting while the other pipeline's Job runs", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(jobNames()).To(ConsistOf(outdated.Name))
		})

		By("restarting from the first pipeline once that Job has finished", func() {
			markRunningJobAsComplete(outdated.Name)

			passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(jobNames()).To(ContainElement(resources[0].Job.Name))
		})
	})

	It("recreates the Job of the pipeline that was running, not of the workflow's first pipeline", func() {
		writePipelineStatuses(parent, configureKey, settledEntry(resources[0]), runningEntry(resources[1]))

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		jobs := listJobs(namespace)
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].Name).To(Equal(resources[1].Job.Name))
		Expect(jobs[0].GetLabels()).To(HaveKeyWithValue(v1alpha1.PipelineNameLabel, "pipeline-2"))
	})

	It("records the failure against the hash the Job ran with, and goes no further", func() {
		// The Running entry carries no hash: the hash under test has to come off
		// the Job, and a fixture that supplies it up front could not tell a
		// recorded hash from an inherited one.
		writePipelineStatuses(parent, configureKey, startedEntry(resources[0]), pendingEntry(resources[1]))
		createJob(resources[0].Job, jobFailed)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		By("marking the entry Failed at the Job's own hash", func() {
			Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
				SatisfyAll(
					HaveKeyWithValue("name", "pipeline-1"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseFailed),
					HaveKeyWithValue("hash", resources[0].Job.GetLabels()[v1alpha1.KratixResourceHashLabel]),
				),
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
			))
		})

		By("reporting the workflow as failed", func() {
			condition := resourceutil.GetCondition(storedParent(parent), resourceutil.ConfigureWorkflowCompletedCondition)
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(v1.ConditionFalse))
			Expect(condition.Reason).To(Equal(resourceutil.ConfigureWorkflowCompletedFailedReason))
		})

		By("not advancing to the next pipeline on this or any later reconciliation", func() {
			Expect(listJobs(namespace)).To(HaveLen(1))
			passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(listJobs(namespace)).To(HaveLen(1))
		})
	})

	It("records the success against the hash the Job ran with, then advances", func() {
		// No hash on the entry, for the same reason as the failure spec above.
		writePipelineStatuses(parent, configureKey, startedEntry(resources[0]), pendingEntry(resources[1]))
		createJob(resources[0].Job, jobSucceeded)
		opts := newOpts()

		By("taking the hash off the Job's own kratix.io/hash label", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
				SatisfyAll(
					HaveKeyWithValue("name", "pipeline-1"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseSucceeded),
					HaveKeyWithValue("hash", resources[0].Job.GetLabels()[v1alpha1.KratixResourceHashLabel]),
				),
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
			))
		})

		By("running the next pipeline on the reconciliation the status update triggers", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(listJobs(namespace)).To(ContainElement(HaveField("Name", resources[1].Job.Name)))
		})
	})

	It("corrects a Succeeded entry recorded against a definition its Job never ran", func() {
		writePipelineStatuses(parent, configureKey,
			succeededEntry(resources[0], "a-hash-nothing-ran"),
			pendingEntry(resources[1]),
		)
		createJob(resources[0].Job, jobSucceeded)
		opts := newOpts()

		By("re-recording the hash off the Job that actually ran", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
				SatisfyAll(
					HaveKeyWithValue("name", "pipeline-1"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseSucceeded),
					HaveKeyWithValue("hash", resources[0].Job.GetLabels()[v1alpha1.KratixResourceHashLabel]),
				),
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
			))
		})

		By("advancing to the next pipeline rather than stalling on this one", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(jobNames()).To(ContainElement(resources[1].Job.Name))
		})
	})

	It("records a failure once, then holds without rewriting the status or repeating the event", func() {
		writePipelineStatuses(parent, configureKey,
			map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhaseFailed, "hash": "a-hash-nothing-ran"},
			pendingEntry(resources[1]),
		)
		createJob(resources[0].Job, jobFailed)
		opts := newOpts()

		passiveRequeue, err := workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())
		settled := storedParent(parent)

		By("changing nothing at all on the reconciliations after the failure is recorded", func() {
			for range 2 {
				passiveRequeue, err := workflow.ReconcileConfigure(opts)
				Expect(err).NotTo(HaveOccurred())
				Expect(passiveRequeue).To(BeTrue())
				Expect(storedParent(parent)).To(Equal(settled))
			}
		})

		By("emitting exactly one failure event for the one failure", func() {
			Expect(countEvents(eventRecorder, resourceutil.ConfigureWorkflowCompletedFailedReason)).To(Equal(1))
		})
	})

	It("drops a suspended entry that names no pipeline of this workflow, then runs its own", func() {
		writePipelineStatuses(parent, configureKey,
			pendingEntry(resources[0]),
			pendingEntry(resources[1]),
			map[string]any{"name": "a-pipeline-of-theirs", "phase": v1alpha1.WorkflowPhaseSuspended},
		)
		opts := newOpts()

		By("pruning the entry the workflow has no pipeline for", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
				HaveKeyWithValue("name", "pipeline-1"),
				HaveKeyWithValue("name", "pipeline-2"),
			))
			Expect(jobNames()).To(BeEmpty())
		})

		By("running its own first pipeline rather than waiting on the suspension", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(jobNames()).To(ContainElement(resources[0].Job.Name))
		})
	})

	It("re-runs once against a Job retained from before the pipeline hash was folded in", func() {
		preFoldPromise, preFoldPipelines := progressionPromise("pipeline-1")
		preFoldResources, _ := setupTest(preFoldPromise, preFoldPipelines)

		foldedPipelines := []v1alpha1.Pipeline{*preFoldPipelines[0].DeepCopy()}
		foldedPipelines[0].SetLabels(map[string]string{v1alpha1.KratixPipelineHashLabel: "pipeline-hash-v1"})
		foldedResources, foldedParent := setupTest(preFoldPromise, foldedPipelines)

		retained := preFoldResources[0].Job.DeepCopy()
		retained.Labels[v1alpha1.KratixPipelineHashLabel] = "pipeline-hash-v1"
		preFoldHash := retained.Labels[v1alpha1.KratixResourceHashLabel]
		Expect(preFoldHash).
			NotTo(Equal(foldedResources[0].Job.GetLabels()[v1alpha1.KratixResourceHashLabel]),
				"the fixture is only meaningful while the folded hash differs from the bare one")
		createJob(retained, jobSucceeded)
		writePipelineStatuses(foldedParent, configureKey, succeededEntry(preFoldResources[0], preFoldHash))

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, foldedParent, foldedResources, "promise", 5, namespace)
		passiveRequeue, err := workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		Expect(listJobs(namespace)).To(HaveLen(2))
		Expect(listJobs(namespace)).To(ContainElement(HaveField("Name", foldedResources[0].Job.Name)))
	})

	It("never prunes a Job that is still running, however old it is", func() {
		singlePromise, singlePipelines := progressionPromise("pipeline-1")
		singleResources, singleParent := setupTest(singlePromise, singlePipelines)
		writePipelineStatuses(singleParent, configureKey, settledEntry(singleResources[0]))

		oldest := copyJob(singleResources[0].Job, "oldest")
		stillRunning := copyJob(singleResources[0].Job, "still-running")
		newest := singleResources[0].Job
		createJob(oldest, jobSucceeded)
		createJob(stillRunning, jobRunning)
		createJob(newest, jobSucceeded)

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, singleParent, singleResources, "promise", 1, namespace)
		passiveRequeue, err := workflow.ReconcileConfigure(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeFalse())

		names := []string{}
		for _, job := range listJobs(namespace) {
			names = append(names, job.Name)
		}
		Expect(names).To(ContainElement(stillRunning.Name))
		Expect(names).NotTo(ContainElement(oldest.Name))
	})

	It("treats a Job carrying only the retired work-* labels as no evidence", func() {
		legacy := resources[0].Job.DeepCopy()
		legacy.Name = "legacy-" + legacy.Name
		legacy.Labels = map[string]string{
			v1alpha1.WorkTypeLabel:           "promise",
			v1alpha1.WorkActionLabel:         "configure",
			v1alpha1.PromiseNameLabel:        "redis",
			v1alpha1.PipelineNameLabel:       "pipeline-1",
			v1alpha1.KratixResourceHashLabel: resources[0].Job.GetLabels()[v1alpha1.KratixResourceHashLabel],
		}
		createJob(legacy, jobSucceeded)

		// The entry says Running, not Pending: a Pending entry runs the pipeline
		// whatever the Jobs say, so it could not tell an invisible Job from a
		// visible one. Running is the state where evidence decides, and the
		// legacy Job supplies none — so the pipeline runs again.
		writePipelineStatuses(parent, configureKey, runningEntry(resources[0]), pendingEntry(resources[1]))

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		Expect(listJobs(namespace)).To(ContainElement(HaveField("Name", resources[0].Job.Name)))
		Expect(storedPipelineStatuses(parent, configureKey)).To(ContainElement(SatisfyAll(
			HaveKeyWithValue("name", "pipeline-1"),
			HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseRunning),
		)))
	})

	It("prunes the entries of pipelines the workflow no longer has, and seeds the ones it does", func() {
		writePipelineStatuses(parent, configureKey,
			map[string]any{"name": "a-pipeline-that-was-renamed", "phase": v1alpha1.WorkflowPhaseSucceeded, "hash": "some-hash"},
			pendingEntry(resources[1]),
		)
		opts := newOpts()

		By("reconciling the stored entries with the workflow's pipelines", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
				SatisfyAll(
					HaveKeyWithValue("name", "pipeline-1"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
				),
				HaveKeyWithValue("name", "pipeline-2"),
			))
			Expect(listJobs(namespace)).To(BeEmpty())
		})

		By("running the pipeline the stale entry was standing in for", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(listJobs(namespace)).To(ConsistOf(HaveField("Name", resources[0].Job.Name)))
		})
	})

	It("never lets Job evidence overwrite a suspended entry", func() {
		parent.SetLabels(map[string]string{v1alpha1.WorkflowSuspendedLabel: "true"})
		Expect(fakeK8sClient.Update(ctx, parent)).To(Succeed())
		writePipelineStatuses(parent, configureKey,
			map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhaseSuspended, "message": "waiting"},
			pendingEntry(resources[1]),
		)
		createJob(resources[0].Job, jobSucceeded)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
			SatisfyAll(
				HaveKeyWithValue("name", "pipeline-1"),
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseSuspended),
			),
			HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
		))
		Expect(listJobs(namespace)).To(HaveLen(1))
	})

	It("suspends the running Job for a manual reconciliation without recording a failure", func() {
		writePipelineStatuses(parent, configureKey, runningEntry(resources[0]), pendingEntry(resources[1]))
		createJob(resources[0].Job, jobRunning)
		labelPromiseForManualReconciliation(promise.Name)
		parent.SetLabels(map[string]string{resourceutil.ManualReconciliationLabel: "true"})

		By("suspending the Job and leaving the pipeline statuses alone", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			job := &batchv1.Job{}
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resources[0].Job.Name, Namespace: namespace}, job)).To(Succeed())
			Expect(job.Spec.Suspend).To(HaveValue(BeTrue()))
			Expect(storedPipelineStatuses(parent, configureKey)).NotTo(ContainElement(
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseFailed)))
		})

		markJobAsSuspended(resources[0].Job.Name)
		resources, parent = setupTest(promise, pipelines)

		By("running the workflow again from the top once the Job has stopped", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(listJobs(namespace)).To(ContainElement(HaveField("Name", resources[0].Job.Name)))
			Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
				SatisfyAll(
					HaveKeyWithValue("name", "pipeline-1"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseRunning),
				),
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
			))

			stored := &v1alpha1.Promise{}
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, stored)).To(Succeed())
			Expect(stored.GetLabels()).NotTo(HaveKey(resourceutil.ManualReconciliationLabel))
		})
	})

	It("runs the pipeline again when its entry carries a phase the engine does not know", func() {
		writePipelineStatuses(parent, configureKey,
			map[string]any{"name": "pipeline-1", "phase": "Banana"},
			pendingEntry(resources[1]),
		)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())
		Expect(listJobs(namespace)).To(ConsistOf(HaveField("Name", resources[0].Job.Name)))
	})

	It("runs a pipeline whose entry records no phase at all", func() {
		writePipelineStatuses(parent, configureKey,
			map[string]any{"name": "pipeline-1"},
			pendingEntry(resources[1]),
		)
		createJob(resources[0].Job, jobSucceeded)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		Expect(countEvents(eventRecorder, "PipelineStarted")).To(Equal(1))
		Expect(storedPipelineStatuses(parent, configureKey)).To(ContainElement(SatisfyAll(
			HaveKeyWithValue("name", "pipeline-1"),
			HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseRunning),
		)))
	})

	It("puts reordered entries back into the workflow's own order before running anything", func() {
		writePipelineStatuses(parent, configureKey, pendingEntry(resources[1]), pendingEntry(resources[0]))

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
			HaveKeyWithValue("name", "pipeline-1"),
			HaveKeyWithValue("name", "pipeline-2"),
		))
		Expect(jobNames()).To(BeEmpty())
	})

	Describe("a pipeline resumed after its suspension is lifted", func() {
		BeforeEach(func() {
			writePipelineStatuses(parent, configureKey,
				settledEntry(resources[0]),
				map[string]any{
					"name":        "pipeline-2",
					"phase":       v1alpha1.WorkflowPhaseSuspended,
					"message":     "waiting for approval",
					"attempts":    int64(3),
					"nextRetryAt": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
				},
			)
		})

		It("resumes in place, keeping its retry bookkeeping and the entries around it", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
				SatisfyAll(
					HaveKeyWithValue("name", "pipeline-1"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseSucceeded),
				),
				SatisfyAll(
					HaveKeyWithValue("name", "pipeline-2"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseRunning),
					HaveKeyWithValue("attempts", int64(3)),
				),
			))
			Expect(jobNames()).To(ContainElement(resources[1].Job.Name))
		})

		It("waits when a Job of the workflow is still running", func() {
			createJob(resources[1].Job, jobRunning)

			passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(countEvents(eventRecorder, "PipelineStarted")).To(Equal(0))
			Expect(storedPipelineStatuses(parent, configureKey)).To(ContainElement(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-2"),
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseSuspended),
			)))
		})

		It("prunes the Works of pipelines the workflow no longer has", func() {
			stale := &v1alpha1.Work{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "work-of-a-removed-pipeline",
					Namespace: namespace,
					Labels: resourceutil.GetWorkLabels(promise.GetName(), "", "",
						"a-pipeline-the-workflow-no-longer-has", v1alpha1.WorkTypePromise),
				},
			}
			Expect(fakeK8sClient.Create(ctx, stale)).To(Succeed())

			_, err := workflow.ReconcileConfigure(newOpts())
			Expect(err).NotTo(HaveOccurred())

			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
				Name: stale.Name, Namespace: stale.Namespace,
			}, &v1alpha1.Work{})).To(MatchError(ContainSubstring("not found")))
		})
	})

	It("writes no status at all when re-running a pipeline leaves the other entries alone", func() {
		writePipelineStatuses(parent, configureKey, runningEntry(resources[0]), pendingEntry(resources[1]))
		resourceutil.SetStatus(parent, logger, "message", "Pending")
		resourceutil.MarkReconciledPending(parent, "WorkflowPending")
		Expect(fakeK8sClient.Status().Update(ctx, parent)).To(Succeed())
		before := storedParent(parent)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		Expect(jobNames()).To(ContainElement(resources[0].Job.Name))
		Expect(storedParent(parent)).To(Equal(before))
	})

	It("re-seeds an entry deleted by hand and carries on with the running Job", func() {
		writePipelineStatuses(parent, configureKey, pendingEntry(resources[1]))
		createJob(resources[0].Job, jobRunning)
		opts := newOpts()

		By("putting the missing entry back", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(storedPipelineStatuses(parent, configureKey)).To(HaveExactElements(
				HaveKeyWithValue("name", "pipeline-1"),
				HaveKeyWithValue("name", "pipeline-2"),
			))
		})

		By("waiting on the Job that was already running rather than starting another", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(listJobs(namespace)).To(HaveLen(1))
		})
	})

	Describe("a controller that embeds the workflow engine", func() {
		It("keeps its pipeline statuses under its own key, beside Kratix's", func() {
			writePipelineStatuses(parent, configureKey, settledEntry(resources[0]), settledEntry(resources[1]))
			configureBefore := storedPipelineStatuses(parent, configureKey)

			opts := newOpts()
			opts.WorkflowKey = "their-workflow"

			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(storedPipelineStatuses(parent, "their-workflow")).To(HaveExactElements(
				HaveKeyWithValue("name", "pipeline-1"),
				HaveKeyWithValue("name", "pipeline-2"),
			))
			Expect(storedPipelineStatuses(parent, configureKey)).To(Equal(configureBefore))
		})

		It("is refused a key Kratix's own workflows already use", func() {
			for _, reserved := range []string{configureKey, deleteKey} {
				opts := newOpts()
				opts.WorkflowKey = reserved

				_, err := workflow.ReconcileConfigure(opts)
				Expect(err).To(MatchError(ContainSubstring("reserved")))
				_, err = workflow.ReconcileDelete(opts)
				Expect(err).To(MatchError(ContainSubstring("reserved")))
			}
			Expect(listJobs(namespace)).To(BeEmpty())
		})

		It("is refused a key that would not survive as a status path segment", func() {
			for _, invalid := range []string{"their/workflow", "their.workflow", "-their-workflow", ""} {
				opts := newOpts()
				opts.WorkflowKey = invalid
				if invalid == "" {
					// The empty key is not invalid, it means "use Kratix's own".
					continue
				}
				_, err := workflow.ReconcileConfigure(opts)
				Expect(err).To(MatchError(ContainSubstring("not a valid status path segment")), "key %q", invalid)
			}
			Expect(listJobs(namespace)).To(BeEmpty())
		})
	})
})

func progressionPromise(pipelineNames ...string) (v1alpha1.Promise, []v1alpha1.Pipeline) {
	GinkgoHelper()

	promise := v1alpha1.Promise{
		ObjectMeta: metav1.ObjectMeta{Name: "redis"},
		TypeMeta:   metav1.TypeMeta{APIVersion: "platform.kratix.io/v1alpha1", Kind: "Promise"},
	}

	pipelines := make([]v1alpha1.Pipeline, 0, len(pipelineNames))
	for _, name := range pipelineNames {
		pipelines = append(pipelines, v1alpha1.Pipeline{
			Kind:       "Pipeline",
			APIVersion: "kratix.io/v1alpha1",
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.PipelineSpec{
				Containers: []v1alpha1.Container{{Name: "container-1", Image: "busybox"}},
			},
		})
	}

	promise.Spec.Workflows.Promise.Configure = make([]unstructured.Unstructured, len(pipelines))
	for i, pipeline := range pipelines {
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pipeline)
		Expect(err).NotTo(HaveOccurred())
		promise.Spec.Workflows.Promise.Configure[i] = unstructured.Unstructured{Object: obj}
	}

	existing := &v1alpha1.Promise{}
	if err := fakeK8sClient.Get(ctx, types.NamespacedName{Name: promise.Name}, existing); err == nil {
		existing.Spec = promise.Spec
		Expect(fakeK8sClient.Update(ctx, existing)).To(Succeed())
		return *existing, pipelines
	}

	Expect(fakeK8sClient.Create(ctx, &promise)).To(Succeed())
	Expect(fakeK8sClient.Status().Update(ctx, &promise)).To(Succeed())
	return promise, pipelines
}

// writePipelineStatuses replaces the pipeline statuses under key and persists
// them, so a spec states recorded progress outright rather than reconciling to it.
func writePipelineStatuses(parent *unstructured.Unstructured, key string, entries ...map[string]any) {
	GinkgoHelper()
	raw := make([]any, 0, len(entries))
	for _, entry := range entries {
		raw = append(raw, entry)
	}
	Expect(resourceutil.SetPipelineStatuses(parent, key, raw)).To(Succeed())
	Expect(fakeK8sClient.Status().Update(ctx, parent)).To(Succeed())
}

func storedParent(parent *unstructured.Unstructured) *unstructured.Unstructured {
	GinkgoHelper()
	stored := &unstructured.Unstructured{}
	stored.SetGroupVersionKind(parent.GroupVersionKind())
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{
		Name: parent.GetName(), Namespace: parent.GetNamespace(),
	}, stored)).To(Succeed())
	return stored
}

func storedPipelineStatuses(parent *unstructured.Unstructured, key string) []any {
	GinkgoHelper()
	entries, _, err := resourceutil.GetPipelineStatuses(storedParent(parent), key)
	Expect(err).NotTo(HaveOccurred())
	return entries
}

// jobNames lists the Jobs by name alone: asserting on the Jobs themselves prints
// a whole PodSpec each on failure, which buries what went wrong.
func jobNames() []string {
	GinkgoHelper()
	names := []string{}
	for _, job := range listJobs(namespace) {
		names = append(names, job.Name)
	}
	return names
}

func settledEntry(resource v1alpha1.PipelineJobResources) map[string]any {
	return succeededEntry(resource, resource.Job.GetLabels()[v1alpha1.KratixResourceHashLabel])
}

func succeededEntry(resource v1alpha1.PipelineJobResources, hash string) map[string]any {
	return map[string]any{
		"name":               resource.Name,
		"phase":              v1alpha1.WorkflowPhaseSucceeded,
		"hash":               hash,
		"lastTransitionTime": metav1.Now().Format(time.RFC3339),
	}
}

func runningEntry(resource v1alpha1.PipelineJobResources) map[string]any {
	return map[string]any{
		"name":               resource.Name,
		"phase":              v1alpha1.WorkflowPhaseRunning,
		"hash":               resource.Job.GetLabels()[v1alpha1.KratixResourceHashLabel],
		"lastTransitionTime": metav1.Now().Format(time.RFC3339),
	}
}

// startedEntry is a Running entry that does not say which definition it is
// running, as an older Kratix or a phase-only status writer leaves it.
func startedEntry(resource v1alpha1.PipelineJobResources) map[string]any {
	return map[string]any{
		"name":               resource.Name,
		"phase":              v1alpha1.WorkflowPhaseRunning,
		"lastTransitionTime": metav1.Now().Format(time.RFC3339),
	}
}

func pendingEntry(resource v1alpha1.PipelineJobResources) map[string]any {
	return map[string]any{
		"name":               resource.Name,
		"phase":              v1alpha1.WorkflowPhasePending,
		"lastTransitionTime": metav1.Now().Format(time.RFC3339),
	}
}

func markConfigureWorkflowCompleted(parent *unstructured.Unstructured) {
	GinkgoHelper()
	resourceutil.SetCondition(parent, &clusterv1.Condition{
		Type:               resourceutil.ConfigureWorkflowCompletedCondition,
		Status:             v1.ConditionTrue,
		Reason:             resourceutil.PipelinesExecutedSuccessfully,
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
	Expect(fakeK8sClient.Status().Update(ctx, parent)).To(Succeed())
}

type jobOutcome int

const (
	jobRunning jobOutcome = iota
	jobSucceeded
	jobFailed
)

func createJob(job *batchv1.Job, outcome jobOutcome) {
	GinkgoHelper()
	Expect(fakeK8sClient.Create(ctx, job)).To(Succeed())
	switch outcome {
	case jobRunning:
		job.Status.Active = 1
		Expect(fakeK8sClient.Status().Update(ctx, job)).To(Succeed())
	case jobSucceeded:
		markJobAsComplete(job.Name)
	case jobFailed:
		markJobAsFailed(job.Name)
	}
}

// markRunningJobAsComplete does what the Job controller does when a Job with an
// active Pod finishes. The Complete condition alone leaves status.active at 1,
// which still reads as running.
func markRunningJobAsComplete(name string) {
	GinkgoHelper()
	job := &batchv1.Job{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, job)).To(Succeed())
	job.Status.Active = 0
	job.Status.Succeeded = 1
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: v1.ConditionTrue}}
	Expect(fakeK8sClient.Status().Update(ctx, job)).To(Succeed())
}

// markJobAsSuspended does what the Job controller does once spec.suspend takes
// effect. isFailed() counts the condition as a failure, which is why the
// manual-reconciliation branch runs before any Job evidence is read.
func markJobAsSuspended(name string) {
	GinkgoHelper()
	job := &batchv1.Job{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, job)).To(Succeed())
	job.Status.Active = 0
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuspended, Status: v1.ConditionTrue}}
	Expect(fakeK8sClient.Status().Update(ctx, job)).To(Succeed())
}

func copyJob(job *batchv1.Job, suffix string) *batchv1.Job {
	copied := job.DeepCopy()
	copied.Name = job.Name + "-" + suffix
	copied.ResourceVersion = ""
	copied.SetCreationTimestamp(metav1.NewTime(job.GetCreationTimestamp().Add(-time.Hour)))
	return copied
}
