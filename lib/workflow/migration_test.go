package workflow_test

import (
	"context"
	"fmt"

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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// Every assertion here reads a value out of the migrated object. It cannot read
// "did the decode succeed" instead: the WorkflowsStatus wrapper decodes a broken
// or unmigrated layout without error and simply loses it, so an error-free
// decode says nothing about whether the migration ran or what it produced.
var _ = Describe("Workflow status migration", func() {
	var eventRecorder *events.FakeRecorder

	BeforeEach(func() {
		eventRecorder = events.NewFakeRecorder(1024)
	})

	Describe("migrating an object's pre-keyed status", func() {
		var parentObject *unstructured.Unstructured
		var opts workflow.Opts

		BeforeEach(func() {
			parentObject = &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "platform.kratix.io/v1alpha1",
				"kind":       "Promise",
				"metadata":   map[string]any{"name": "redis"},
				"status":     map[string]any{},
			}}
			opts = workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, parentObject, nil, "promise", 5, namespace)
		})

		When("the object carries the flat pipeline statuses", func() {
			BeforeEach(func() {
				setFlatWorkflowsStatus(parentObject, map[string]any{
					"pipelines": []any{
						map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhaseSucceeded},
						map[string]any{"name": "pipeline-2", "phase": v1alpha1.WorkflowPhasePending},
					},
				})
			})

			It("moves them under the invoking key, entries and order untouched", func() {
				Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeTrue())

				Expect(pipelinesUnderKey(parentObject, configureKey)).To(Equal([]any{
					map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhaseSucceeded},
					map[string]any{"name": "pipeline-2", "phase": v1alpha1.WorkflowPhasePending},
				}))

				By("leaving nothing behind at the flat path", func() {
					_, found := flatWorkflowsField(parentObject, "pipelines")
					Expect(found).To(BeFalse())
				})
			})

			It("reports no change, and changes nothing, when it runs again", func() {
				Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeTrue())
				migrated := pipelinesUnderKey(parentObject, configureKey)

				Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeFalse())
				Expect(pipelinesUnderKey(parentObject, configureKey)).To(Equal(migrated))
			})

			It("keeps the keyed entries when a half-upgraded writer recreates the flat ones", func() {
				Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeTrue())

				setFlatWorkflowsStatus(parentObject, map[string]any{
					"pipelines": []any{
						map[string]any{"name": "written-by-the-old-code", "phase": v1alpha1.WorkflowPhasePending},
					},
				})

				Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeTrue())

				By("keeping the keyed entries the migration already established", func() {
					Expect(pipelinesUnderKey(parentObject, configureKey)).To(Equal([]any{
						map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhaseSucceeded},
						map[string]any{"name": "pipeline-2", "phase": v1alpha1.WorkflowPhasePending},
					}))
				})

				By("discarding the recreated flat entries rather than keeping them readable", func() {
					_, found := flatWorkflowsField(parentObject, "pipelines")
					Expect(found).To(BeFalse())
				})

				By("settling: a further run has nothing left to do", func() {
					Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeFalse())
				})
			})
		})

		When("the object carries a flat suspendedGeneration", func() {
			BeforeEach(func() {
				setFlatWorkflowsStatus(parentObject, map[string]any{"suspendedGeneration": int64(7)})
			})

			It("moves it under the invoking key", func() {
				Expect(workflow.MigrateWorkflowStatus(opts, deleteKey, v1alpha1.WorkflowActionDelete)).To(BeTrue())

				Expect(resourceutil.GetKratixWorkflowsInt64Status(parentObject, deleteKey, "suspendedGeneration")).
					To(Equal(int64(7)))
				_, found := flatWorkflowsField(parentObject, "suspendedGeneration")
				Expect(found).To(BeFalse())
			})

			It("never overwrites a keyed suspendedGeneration that is already there", func() {
				Expect(resourceutil.SetKratixWorkflowsInt64Status(parentObject, deleteKey, "suspendedGeneration", 42)).To(Succeed())

				Expect(workflow.MigrateWorkflowStatus(opts, deleteKey, v1alpha1.WorkflowActionDelete)).To(BeTrue())

				Expect(resourceutil.GetKratixWorkflowsInt64Status(parentObject, deleteKey, "suspendedGeneration")).
					To(Equal(int64(42)))
			})
			It("removes the stale flat field even when the keyed one wins, and then settles", func() {
				Expect(resourceutil.SetKratixWorkflowsInt64Status(parentObject, deleteKey, "suspendedGeneration", 42)).To(Succeed())

				Expect(workflow.MigrateWorkflowStatus(opts, deleteKey, v1alpha1.WorkflowActionDelete)).To(BeTrue())

				_, found := flatWorkflowsField(parentObject, "suspendedGeneration")
				Expect(found).To(BeFalse())
				Expect(workflow.MigrateWorkflowStatus(opts, deleteKey, v1alpha1.WorkflowActionDelete)).To(BeFalse())
			})
		})

		It("files the flat lastSuccessfulConfigureWorkflowTime under configure even when the delete workflow migrates it", func() {
			setFlatWorkflowsStatus(parentObject, map[string]any{
				"lastSuccessfulConfigureWorkflowTime": "2026-01-01T00:00:00Z",
			})

			Expect(workflow.MigrateWorkflowStatus(opts, deleteKey, v1alpha1.WorkflowActionDelete)).To(BeTrue())

			Expect(resourceutil.GetKratixWorkflowsStatus(parentObject, configureKey, "lastSuccessfulTime")).
				To(Equal("2026-01-01T00:00:00Z"))
			Expect(resourceutil.GetKratixWorkflowsStatus(parentObject, deleteKey, "lastSuccessfulTime")).
				To(BeEmpty(), "a configure timestamp must not be filed as a delete one")

			_, found := flatWorkflowsField(parentObject, "lastSuccessfulConfigureWorkflowTime")
			Expect(found).To(BeFalse())
		})

		It("removes the legacy top-level workflow counters", func() {
			Expect(unstructured.SetNestedField(parentObject.Object, int64(2), "status", "workflows")).To(Succeed())
			Expect(unstructured.SetNestedField(parentObject.Object, int64(2), "status", "workflowsSucceeded")).To(Succeed())
			Expect(unstructured.SetNestedField(parentObject.Object, int64(0), "status", "workflowsFailed")).To(Succeed())

			Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeTrue())

			status, _, err := unstructured.NestedMap(parentObject.Object, "status")
			Expect(err).NotTo(HaveOccurred())
			Expect(status).NotTo(HaveKey("workflows"))
			Expect(status).NotTo(HaveKey("workflowsSucceeded"))
			Expect(status).NotTo(HaveKey("workflowsFailed"))

			By("settling: a further run has nothing left to do", func() {
				Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeFalse())
			})
		})

		It("migrates the entries without a flat value that cannot be read at its type", func() {
			setFlatWorkflowsStatus(parentObject, map[string]any{
				"pipelines":           []any{map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhasePending}},
				"suspendedGeneration": "not-a-generation",
			})

			changed, err := workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)
			Expect(err).NotTo(HaveOccurred())
			Expect(changed).To(BeTrue())

			Expect(pipelinesUnderKey(parentObject, configureKey)).To(ConsistOf(HaveKeyWithValue("name", "pipeline-1")))
			Expect(resourceutil.GetKratixWorkflowsInt64Status(parentObject, configureKey, "suspendedGeneration")).To(BeZero())
		})

		It("reports no change for an object that was never on the flat layout", func() {
			Expect(resourceutil.ResetPipelineStatusToPending(parentObject, configureKey, nil)).To(Succeed())

			Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeFalse())
		})

		Describe("lifting each pipeline's hash off its retained Job", func() {
			BeforeEach(func() {
				setFlatWorkflowsStatus(parentObject, map[string]any{
					"pipelines": []any{
						map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhaseSucceeded},
						map[string]any{"name": "pipeline-2", "phase": v1alpha1.WorkflowPhaseSucceeded},
					},
				})
			})

			It("takes the hash of the most recent Job that succeeded for that pipeline", func() {
				createRetainedJob("pipeline-1-old", "pipeline-1", "hash-of-the-older-run", true, v1alpha1.WorkflowActionConfigure)
				createRetainedJob("pipeline-1-current", "pipeline-1", "hash-of-the-run", true, v1alpha1.WorkflowActionConfigure)
				// A later run that never succeeded says nothing about what the
				// Succeeded entry ran with, so it must not supply the hash.
				createRetainedJob("pipeline-1-failed", "pipeline-1", "hash-of-a-failed-run", false, v1alpha1.WorkflowActionConfigure)
				createRetainedJob("pipeline-2-current", "pipeline-2", "hash-of-the-other-run", true, v1alpha1.WorkflowActionConfigure)

				Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeTrue())

				Expect(pipelinesUnderKey(parentObject, configureKey)).To(ConsistOf(
					SatisfyAll(
						HaveKeyWithValue("name", "pipeline-1"),
						HaveKeyWithValue("hash", "hash-of-the-run"),
					),
					SatisfyAll(
						HaveKeyWithValue("name", "pipeline-2"),
						HaveKeyWithValue("hash", "hash-of-the-other-run"),
					),
				))
			})

			It("leaves the entry without a hash when no succeeded Job is retained for it", func() {
				createRetainedJob("pipeline-1-failed", "pipeline-1", "hash-of-a-failed-run", false, v1alpha1.WorkflowActionConfigure)

				Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeTrue())

				for _, pipeline := range pipelinesUnderKey(parentObject, configureKey) {
					Expect(pipeline).NotTo(HaveKey("hash"))
				}
			})

			It("never lifts a hash onto an entry that did not succeed", func() {
				setFlatWorkflowsStatus(parentObject, map[string]any{
					"pipelines": []any{
						map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhaseSuspended},
						map[string]any{"name": "pipeline-2", "phase": v1alpha1.WorkflowPhasePending},
					},
				})
				createRetainedJob("pipeline-1-current", "pipeline-1", "hash-of-the-run", true, v1alpha1.WorkflowActionConfigure)
				createRetainedJob("pipeline-2-current", "pipeline-2", "hash-of-the-other-run", true, v1alpha1.WorkflowActionConfigure)

				Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeTrue())

				for _, pipeline := range pipelinesUnderKey(parentObject, configureKey) {
					Expect(pipeline).NotTo(HaveKey("hash"))
				}
			})

			It("keeps a hash the entry already carries", func() {
				setFlatWorkflowsStatus(parentObject, map[string]any{
					"pipelines": []any{
						map[string]any{
							"name":  "pipeline-1",
							"phase": v1alpha1.WorkflowPhaseSucceeded,
							"hash":  "hash-already-recorded",
						},
					},
				})
				createRetainedJob("pipeline-1-current", "pipeline-1", "hash-of-the-run", true, v1alpha1.WorkflowActionConfigure)

				Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeTrue())

				Expect(pipelinesUnderKey(parentObject, configureKey)).To(ConsistOf(
					HaveKeyWithValue("hash", "hash-already-recorded"),
				))
			})

			It("never lifts a hash from a Job of the other workflow action", func() {
				setFlatWorkflowsStatus(parentObject, map[string]any{
					"pipelines": []any{
						map[string]any{"name": "main", "phase": v1alpha1.WorkflowPhaseSucceeded},
					},
				})
				createRetainedJob("main-configure", "main", "hash-of-the-configure-run", true, v1alpha1.WorkflowActionConfigure)

				Expect(workflow.MigrateWorkflowStatus(opts, deleteKey, v1alpha1.WorkflowActionDelete)).To(BeTrue())

				By("migrating the delete entry with no hash, so the delete pipeline still runs", func() {
					Expect(pipelinesUnderKey(parentObject, deleteKey)).To(ConsistOf(Not(HaveKey("hash"))))
				})

				By("still lifting the hash for the configure lane's own entry", func() {
					setFlatWorkflowsStatus(parentObject, map[string]any{
						"pipelines": []any{
							map[string]any{"name": "main", "phase": v1alpha1.WorkflowPhaseSucceeded},
						},
					})
					Expect(workflow.MigrateWorkflowStatus(opts, configureKey, v1alpha1.WorkflowActionConfigure)).To(BeTrue())
					Expect(pipelinesUnderKey(parentObject, configureKey)).To(ConsistOf(
						HaveKeyWithValue("hash", "hash-of-the-configure-run")))
				})
			})

			It("lifts the delete lane's hash off the delete lane's own retained Job", func() {
				setFlatWorkflowsStatus(parentObject, map[string]any{
					"pipelines": []any{
						map[string]any{"name": "main", "phase": v1alpha1.WorkflowPhaseSucceeded},
					},
				})
				createRetainedJob("main-configure", "main", "hash-of-the-configure-run", true, v1alpha1.WorkflowActionConfigure)
				createRetainedJob("main-delete", "main", "hash-of-the-delete-run", true, v1alpha1.WorkflowActionDelete)

				Expect(workflow.MigrateWorkflowStatus(opts, deleteKey, v1alpha1.WorkflowActionDelete)).To(BeTrue())

				Expect(pipelinesUnderKey(parentObject, deleteKey)).To(ConsistOf(
					HaveKeyWithValue("hash", "hash-of-the-delete-run")))
			})

			It("fails the reconciliation when the retained Jobs cannot be listed", func() {
				listErr := fmt.Errorf("the apiserver said no")
				failingClient := interceptor.NewClient(fakeK8sClient.(client.WithWatch), interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, listOpts ...client.ListOption) error {
						if _, isJobList := list.(*batchv1.JobList); isJobList {
							return listErr
						}
						return c.List(ctx, list, listOpts...)
					},
				})
				failingOpts := workflow.NewOpts(ctx, failingClient, eventRecorder, logger, parentObject, nil, "promise", 5, namespace)

				changed, err := workflow.MigrateWorkflowStatus(failingOpts, configureKey, v1alpha1.WorkflowActionConfigure)
				Expect(err).To(MatchError(listErr))
				Expect(changed).To(BeFalse())

				By("leaving the flat entries in place, so the next attempt migrates them whole", func() {
					_, found := flatWorkflowsField(parentObject, "pipelines")
					Expect(found).To(BeTrue())
				})
			})
		})
	})

	Describe("the engine running the migration", func() {
		var promise v1alpha1.Promise
		var uPromise *unstructured.Unstructured
		var resources []v1alpha1.PipelineJobResources

		BeforeEach(func() {
			promise = v1alpha1.Promise{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "platform.kratix.io/v1alpha1",
					Kind:       "Promise",
				},
				ObjectMeta: metav1.ObjectMeta{Name: "redis"},
			}

			pipelines := []v1alpha1.Pipeline{{
				Kind:       "Pipeline",
				APIVersion: "kratix.io/v1alpha1",
				ObjectMeta: metav1.ObjectMeta{Name: "pipeline-1"},
				Spec: v1alpha1.PipelineSpec{
					Containers: []v1alpha1.Container{{Name: "container-1", Image: "busybox"}},
				},
			}}

			promise.Spec.Workflows.Promise.Configure = make([]unstructured.Unstructured, len(pipelines))
			for i, p := range pipelines {
				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&p)
				Expect(err).NotTo(HaveOccurred())
				promise.Spec.Workflows.Promise.Configure[i] = unstructured.Unstructured{Object: obj}
			}
			Expect(fakeK8sClient.Create(ctx, &promise)).To(Succeed())

			resources = nil
			for _, p := range pipelines {
				generated, err := p.ForPromise(&promise, v1alpha1.WorkflowActionConfigure).Resources(nil)
				Expect(err).NotTo(HaveOccurred())
				generated.Job.SetCreationTimestamp(nextTimestamp())
				resources = append(resources, generated)
			}

			var err error
			uPromise, err = promise.ToUnstructured()
			Expect(err).NotTo(HaveOccurred())
		})

		It("migrates, persists and requeues before running any pipeline", func() {
			setFlatWorkflowsStatus(uPromise, map[string]any{
				"pipelines": []any{
					map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhaseSucceeded},
				},
				"lastSuccessfulConfigureWorkflowTime": "2026-01-01T00:00:00Z",
			})
			createRetainedJob("pipeline-1-current", "pipeline-1", "hash-of-the-run", true, v1alpha1.WorkflowActionConfigure)

			opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, resources, "promise", 5, namespace)
			passiveRequeue, err := workflow.ReconcileConfigure(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			stored := fetchPromise(promise.GetName())

			By("writing the migrated entries to the API", func() {
				Expect(pipelinesUnderKey(stored, configureKey)).To(ConsistOf(SatisfyAll(
					HaveKeyWithValue("name", "pipeline-1"),
					HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseSucceeded),
					HaveKeyWithValue("hash", "hash-of-the-run"),
				)))
				Expect(resourceutil.GetKratixWorkflowsStatus(stored, configureKey, "lastSuccessfulTime")).
					To(Equal("2026-01-01T00:00:00Z"))
			})

			By("doing nothing else on that reconciliation", func() {
				Expect(listJobs(namespace)).To(HaveLen(1), "only the retained Job should exist")
				Expect(findByName(listJobs(namespace), resources[0].Job.GetName())).To(BeFalse())
			})
		})

		It("migrates a mid-delete object's pipeline statuses under the delete key", func() {
			setFlatWorkflowsStatus(uPromise, map[string]any{
				"pipelines": []any{
					map[string]any{"name": "pipeline-1", "phase": v1alpha1.WorkflowPhaseSuspended},
				},
			})

			opts := workflow.NewOpts(ctx, fakeK8sClient, eventRecorder, logger, uPromise, resources, "promise", 5, namespace)
			passiveRequeue, err := workflow.ReconcileDelete(opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(passiveRequeue).To(BeTrue())

			stored := fetchPromise(promise.GetName())

			Expect(pipelinesUnderKey(stored, deleteKey)).To(ConsistOf(SatisfyAll(
				HaveKeyWithValue("name", "pipeline-1"),
				HaveKeyWithValue("phase", v1alpha1.WorkflowPhaseSuspended),
			)))

			By("leaving the suspended delete pipeline where the delete lane looks for it", func() {
				Expect(resourceutil.GetSuspendedPipelineIndex(stored, deleteKey)).To(Equal(0))
			})
		})
	})
})

// setFlatWorkflowsStatus writes the pre-keyed layout Kratix wrote before the
// workflow status was keyed: the fields sit directly under
// status.kratix.workflows, where the workflow keys live now.
func setFlatWorkflowsStatus(obj *unstructured.Unstructured, flat map[string]any) {
	GinkgoHelper()
	existing, _, err := unstructured.NestedMap(obj.Object, "status", "kratix", "workflows")
	Expect(err).NotTo(HaveOccurred())
	if existing == nil {
		existing = map[string]any{}
	}
	for field, value := range flat {
		existing[field] = value
	}
	Expect(unstructured.SetNestedMap(obj.Object, existing, "status", "kratix", "workflows")).To(Succeed())
}

func flatWorkflowsField(obj *unstructured.Unstructured, field string) (any, bool) {
	GinkgoHelper()
	value, found, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "kratix", "workflows", field)
	Expect(err).NotTo(HaveOccurred())
	return value, found
}

func fetchPromise(name string) *unstructured.Unstructured {
	GinkgoHelper()
	stored := &v1alpha1.Promise{}
	Expect(fakeK8sClient.Get(ctx, types.NamespacedName{Name: name}, stored)).To(Succeed())
	uPromise, err := stored.ToUnstructured()
	Expect(err).NotTo(HaveOccurred())
	return uPromise
}

// createRetainedJob creates a Job carrying the labels the engine looks Jobs up
// by today, so the hash lift can find it.
func createRetainedJob(name, pipelineName, hash string, succeeded bool, action v1alpha1.Action) {
	GinkgoHelper()
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			CreationTimestamp: nextTimestamp(),
			Labels: map[string]string{
				v1alpha1.WorkflowTypeLabel:       "promise",
				v1alpha1.PromiseNameLabel:        "redis",
				v1alpha1.PipelineNameLabel:       pipelineName,
				v1alpha1.KratixResourceHashLabel: hash,
				v1alpha1.WorkflowActionLabel:     string(action),
			},
		},
	}
	Expect(fakeK8sClient.Create(ctx, job)).To(Succeed())

	if succeeded {
		job.Status.Succeeded = 1
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: v1.ConditionTrue}}
	} else {
		job.Status.Failed = 1
		job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: v1.ConditionTrue}}
	}
	Expect(fakeK8sClient.Status().Update(ctx, job)).To(Succeed())
}
