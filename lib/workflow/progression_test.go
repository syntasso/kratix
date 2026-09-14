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

// These specs pin the contract of issue #811: the pipeline ledger under
// status.kratix.workflows.<key>.pipelines is what says how far a workflow has
// got, and Jobs are only evidence that moves entries along. Every one of them
// is about a state Kratix reaches routinely once Jobs are pruned, deleted, or
// simply never created — where reading the Jobs as the record of progress makes
// the workflow start again from its first pipeline.
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
			writeLedger(parent, configureKey,
				settledEntry(resources[0]),
				settledEntry(resources[1]),
			)
		})

		// Assertion 0 — the issue's central scenario. Retained Jobs are pruned
		// on a schedule, so "the workflow succeeded and its Jobs are gone" is
		// the steady state of every promise that has been up for a while.
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

	// Assertion 0d — the delete twin. Recreating the delete pipeline whenever
	// its Job is gone means the finalizer is never removed and the object never
	// finishes deleting.
	It("reports the delete workflow complete when its ledger says so and its Job is gone", func() {
		deleteResources := deletePipelineResources(promise, pipelines[:1])
		writeLedger(parent, deleteKey, settledEntry(deleteResources[0]))

		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, parent, deleteResources, "promise", 5, namespace)
		passiveRequeue, err := workflow.ReconcileDelete(opts)
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeFalse())
		Expect(listJobs(namespace)).To(BeEmpty())
	})

	// Assertion 1.
	It("runs only the pipeline the ledger has not recorded, not the whole workflow", func() {
		writeLedger(parent, configureKey,
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

	// Assertion 2 — the distinguisher from assertion 0 is the targeted reset:
	// the same "everything Succeeded, no Jobs" ledger behaves completely
	// differently once the hashes no longer match what the pipelines would run.
	It("re-runs from the first pipeline and unwinds the later entries when the definitions change", func() {
		writeLedger(parent, configureKey,
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
			Expect(storedLedger(parent, configureKey)).To(HaveExactElements(
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

	// Assertion 3 — characterization: the engine has always waited on a running
	// Job, and it still has to.
	It("does not start a second Job while the pipeline's Job is running", func() {
		writeLedger(parent, configureKey, runningEntry(resources[0]), pendingEntry(resources[1]))
		createJob(resources[0].Job, jobRunning)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())
		Expect(listJobs(namespace)).To(HaveLen(1))
	})

	// Assertion 4.
	It("recreates the Job of the pipeline that was running, not of the workflow's first pipeline", func() {
		writeLedger(parent, configureKey, settledEntry(resources[0]), runningEntry(resources[1]))

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		jobs := listJobs(namespace)
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].Name).To(Equal(resources[1].Job.Name))
		Expect(jobs[0].GetLabels()).To(HaveKeyWithValue(v1alpha1.PipelineNameLabel, "pipeline-2"))
	})

	// Assertion 5 — the phase and conditions are characterization; the recorded
	// hash is not, and it is what stops the failed pipeline being re-run on
	// every reconciliation once the ledger, not the Job, decides.
	It("records the failure against the hash the Job ran with, and goes no further", func() {
		// The Running entry carries no hash: the hash under test has to come off
		// the Job, and a fixture that supplies it up front could not tell a
		// recorded hash from an inherited one.
		writeLedger(parent, configureKey, startedEntry(resources[0]), pendingEntry(resources[1]))
		createJob(resources[0].Job, jobFailed)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		By("marking the entry Failed at the Job's own hash", func() {
			Expect(storedLedger(parent, configureKey)).To(HaveExactElements(
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

	// Assertion 6.
	It("records the success against the hash the Job ran with, then advances", func() {
		// No hash on the entry, for the same reason as the failure spec above.
		writeLedger(parent, configureKey, startedEntry(resources[0]), pendingEntry(resources[1]))
		createJob(resources[0].Job, jobSucceeded)
		opts := newOpts()

		By("taking the hash off the Job's own kratix.io/hash label", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(storedLedger(parent, configureKey)).To(HaveExactElements(
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

	Describe("a caller that owns the parent object's status", func() {
		// Assertion 7a — the whole ephemeral-ledger path. A controller that
		// embeds the workflow engine gets its progression from the Jobs alone,
		// and the engine must not read or write a byte of status doing it.
		It("progresses from the retained Jobs alone, without touching the status", func() {
			createJob(resources[0].Job, jobSucceeded)
			createJob(resources[1].Job, jobSucceeded)

			opts := newOpts()
			opts.SkipConditions = true

			before := storedParent(parent)
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeFalse())

			Expect(listJobs(namespace)).To(HaveLen(2))
			Expect(storedParent(parent)).To(Equal(before))
		})

		// Assertion 7b — the documented one-time re-sync at upgrade. Jobs
		// retained from before the pipeline hash was folded into
		// kratix.io/hash carry the bare request hash, so the pipeline they
		// belong to runs once more and the retained Job is never mistaken for
		// a run of the current definition.
		It("re-runs once against a Job retained from before the pipeline hash was folded in", func() {
			preFoldPromise, preFoldPipelines := progressionPromise("pipeline-1")
			preFoldResources, _ := setupTest(preFoldPromise, preFoldPipelines)

			foldedPipelines := []v1alpha1.Pipeline{*preFoldPipelines[0].DeepCopy()}
			foldedPipelines[0].SetLabels(map[string]string{v1alpha1.KratixPipelineHashLabel: "pipeline-hash-v1"})
			foldedResources, foldedParent := setupTest(preFoldPromise, foldedPipelines)

			retained := preFoldResources[0].Job.DeepCopy()
			retained.Labels[v1alpha1.KratixPipelineHashLabel] = "pipeline-hash-v1"
			Expect(retained.Labels[v1alpha1.KratixResourceHashLabel]).
				NotTo(Equal(foldedResources[0].Job.GetLabels()[v1alpha1.KratixResourceHashLabel]),
					"the fixture is only meaningful while the folded hash differs from the bare one")
			createJob(retained, jobSucceeded)

			opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, foldedParent, foldedResources, "promise", 5, namespace)
			opts.SkipConditions = true

			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(listJobs(namespace)).To(HaveLen(2))
			Expect(listJobs(namespace)).To(ContainElement(HaveField("Name", foldedResources[0].Job.Name)))
		})
	})

	// Assertion 8.
	It("keeps the delete workflow's progress out of the configure workflow's ledger", func() {
		writeLedger(parent, configureKey, settledEntry(resources[0]), runningEntry(resources[1]))
		configureBefore := storedLedger(parent, configureKey)

		deleteResources := deletePipelineResources(promise, pipelines[:1])
		opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, parent, deleteResources, "promise", 5, namespace)
		_, err := workflow.ReconcileDelete(opts)
		Expect(err).NotTo(HaveOccurred())

		Expect(storedLedger(parent, deleteKey)).To(ConsistOf(
			HaveKeyWithValue("name", deleteResources[0].Name),
		))
		Expect(storedLedger(parent, configureKey)).To(Equal(configureBefore))
	})

	// Assertion 9 — pruning now removes the engine's own evidence, so a Job it
	// deletes while the pipeline is still running makes that pipeline look
	// unrun and starts a duplicate of it.
	It("never prunes a Job that is still running, however old it is", func() {
		singlePromise, singlePipelines := progressionPromise("pipeline-1")
		singleResources, singleParent := setupTest(singlePromise, singlePipelines)
		writeLedger(singleParent, configureKey, settledEntry(singleResources[0]))

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

	// Assertion 10 — pins the deliberate blind spot (#360). A Job labelled only
	// with the retired kratix.io/work-* labels is old enough that the run it
	// describes is not the run the ledger records, so it is no evidence at all
	// and the pipeline runs once more.
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
		writeLedger(parent, configureKey, runningEntry(resources[0]), pendingEntry(resources[1]))

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		Expect(listJobs(namespace)).To(ContainElement(HaveField("Name", resources[0].Job.Name)))
		Expect(storedLedger(parent, configureKey)).To(ContainElement(SatisfyAll(
			HaveKeyWithValue("name", "pipeline-1"),
			HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseRunning),
		)))
	})

	// Assertion 11.
	It("prunes ledger entries for pipelines the workflow no longer has, and seeds the ones it does", func() {
		writeLedger(parent, configureKey,
			map[string]any{"name": "a-pipeline-that-was-renamed", "phase": v1alpha1.WorkflowPhaseSucceeded, "hash": "some-hash"},
			pendingEntry(resources[1]),
		)
		opts := newOpts()

		By("reconciling the ledger with the workflow's pipelines", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(storedLedger(parent, configureKey)).To(HaveExactElements(
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

	// Assertion 12 — suspension is written by whoever owns the retry policy,
	// and the Job it leaves behind reads as finished. Letting the evidence win
	// would step straight over a pipeline that is waiting to be retried.
	It("never lets Job evidence overwrite a suspended entry", func() {
		parent.SetLabels(map[string]string{v1alpha1.WorkflowSuspendedLabel: "true"})
		Expect(fakeK8sClient.Update(ctx, parent)).To(Succeed())
		writeLedger(parent, configureKey,
			map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhaseSuspended, "message": "waiting"},
			pendingEntry(resources[1]),
		)
		createJob(resources[0].Job, jobSucceeded)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())

		Expect(storedLedger(parent, configureKey)).To(HaveExactElements(
			SatisfyAll(
				HaveKeyWithValue("name", "pipeline-1"),
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseSuspended),
			),
			HaveKeyWithValue("phase", v1alpha1.WorkflowPhasePending),
		))
		Expect(listJobs(namespace)).To(HaveLen(1))
	})

	// Assertion 13 — the branch-order pin. isFailed() counts a suspended Job as
	// failed, and manual reconciliation suspends the running Job on purpose, so
	// reading the Job evidence before the label would write Failed into the
	// ledger on every manual re-run and then halt the run the label asked for.
	It("suspends the running Job for a manual reconciliation without recording a failure", func() {
		writeLedger(parent, configureKey, runningEntry(resources[0]), pendingEntry(resources[1]))
		createJob(resources[0].Job, jobRunning)
		labelPromiseForManualReconciliation(promise.Name)
		parent.SetLabels(map[string]string{resourceutil.ManualReconciliationLabel: "true"})

		By("suspending the Job and leaving the ledger alone", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			job := &batchv1.Job{}
			Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: resources[0].Job.Name, Namespace: namespace}, job)).To(Succeed())
			Expect(job.Spec.Suspend).To(HaveValue(BeTrue()))
			Expect(storedLedger(parent, configureKey)).NotTo(ContainElement(
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseFailed)))
		})

		markJobAsSuspended(resources[0].Job.Name)
		resources, parent = setupTest(promise, pipelines)

		By("running the workflow again from the top once the Job has stopped", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(listJobs(namespace)).To(ContainElement(HaveField("Name", resources[0].Job.Name)))
			Expect(storedLedger(parent, configureKey)).To(HaveExactElements(
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

	// Assertion 14 — a phase the engine does not recognise must not be a
	// deadlock. Anything that is not a settled entry means "this pipeline still
	// has to run".
	It("runs the pipeline again when its entry carries a phase the engine does not know", func() {
		writeLedger(parent, configureKey,
			map[string]any{"name": "pipeline-1", "phase": "Banana"},
			pendingEntry(resources[1]),
		)

		passiveRequeue, err := workflow.ReconcileConfigure(newOpts())
		Expect(err).NotTo(HaveOccurred())
		Expect(passiveRequeue).To(BeTrue())
		Expect(listJobs(namespace)).To(ConsistOf(HaveField("Name", resources[0].Job.Name)))
	})

	// Assertion 15 — an entry can go missing (an editor, a partial status
	// write, a pipeline added to the workflow), and the marks the engine makes
	// fail hard with "no pipeline found for job" when it does.
	It("re-seeds an entry deleted by hand and carries on with the running Job", func() {
		writeLedger(parent, configureKey, pendingEntry(resources[1]))
		createJob(resources[0].Job, jobRunning)
		opts := newOpts()

		By("putting the missing entry back", func() {
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())
			Expect(storedLedger(parent, configureKey)).To(HaveExactElements(
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
		It("keeps its pipeline ledger under its own key, beside Kratix's", func() {
			writeLedger(parent, configureKey, settledEntry(resources[0]), settledEntry(resources[1]))
			configureBefore := storedLedger(parent, configureKey)

			opts := newOpts()
			opts.WorkflowKey = "their-workflow"

			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			Expect(storedLedger(parent, "their-workflow")).To(HaveExactElements(
				HaveKeyWithValue("name", "pipeline-1"),
				HaveKeyWithValue("name", "pipeline-2"),
			))
			Expect(storedLedger(parent, configureKey)).To(Equal(configureBefore))
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

// writeLedger replaces the pipeline ledger under key and persists it, so a spec
// states the workflow's recorded progress outright instead of reconciling its
// way to it.
func writeLedger(parent *unstructured.Unstructured, key string, entries ...map[string]any) {
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

func storedLedger(parent *unstructured.Unstructured, key string) []any {
	GinkgoHelper()
	entries, _, err := resourceutil.GetPipelineStatuses(storedParent(parent), key)
	Expect(err).NotTo(HaveOccurred())
	return entries
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

// startedEntry is a Running entry that does not yet say which definition it is
// running, as an entry written by an older Kratix or by a status-writer that
// only tracks the phase does.
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

// markJobAsSuspended is what the Job controller does once spec.suspend takes
// effect. isFailed() counts the condition as a failure, which is exactly why the
// manual-reconciliation branch has to be evaluated before any Job evidence.
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
