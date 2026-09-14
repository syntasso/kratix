package cmd_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	. "github.com/syntasso/kratix/work-creator/cmd"
	"github.com/syntasso/kratix/work-creator/lib/helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

var _ = Describe("UpdateStatus", func() {
	const (
		// foreignKey stands for an entry under status.kratix.workflows this
		// container never owns. Every spec plants it and asserts it survives, so a
		// write that widens past the entry it addressed is caught.
		foreignKey       = "portal-x"
		foreignEntryJSON = `{"pipelines":[{"name":"their-pipeline","phase":"Running"}],"suspendedGeneration":11}`
	)

	var (
		ctx        context.Context
		baseDir    string
		params     *helpers.Parameters
		fakeClient *dynamicfake.FakeDynamicClient
		gvr        schema.GroupVersionResource
	)

	newObject := func(workflows map[string]any) *unstructured.Unstructured {
		workflows[foreignKey] = map[string]any{
			"pipelines": []any{
				map[string]any{
					"name":  "their-pipeline",
					"phase": "Running",
				},
			},
			"suspendedGeneration": int64(11),
		}

		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "group/version",
			"kind":       "TheKind",
			"metadata": map[string]any{
				"name":       "name-foo",
				"namespace":  "ns-foo",
				"generation": int64(4),
			},
			"status": map[string]any{
				"message": "Pending",
				"kratix": map[string]any{
					"workflows": workflows,
				},
			},
		}}
	}

	// storedWorkflows reads the workflows map back off the tracker, so assertions
	// see what was actually persisted rather than the in-memory merge.
	storedWorkflows := func() map[string]any {
		GinkgoHelper()
		obj, err := fakeClient.Resource(gvr).Namespace("ns-foo").Get(ctx, "name-foo", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		workflows, found, err := unstructured.NestedMap(obj.Object, "status", "kratix", "workflows")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "no status.kratix.workflows on the stored object")
		return workflows
	}

	storedForeignEntryJSON := func() string {
		GinkgoHelper()
		entry, ok := storedWorkflows()[foreignKey]
		Expect(ok).To(BeTrue(), "the foreign workflow entry is gone from the stored object")
		out, err := json.Marshal(entry)
		Expect(err).NotTo(HaveOccurred())
		return string(out)
	}

	writeControlFile := func(contents string) {
		GinkgoHelper()
		Expect(os.WriteFile(filepath.Join(baseDir, "workflow-control.yaml"), []byte(contents), 0600)).To(Succeed())
	}

	newClient := func(obj *unstructured.Unstructured) {
		scheme := runtime.NewScheme()
		fakeClient = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
			map[schema.GroupVersionResource]string{gvr: "TheKindList"}, obj)
	}

	objectClient := func() dynamic.ResourceInterface {
		return fakeClient.Resource(gvr).Namespace("ns-foo")
	}

	BeforeEach(func() {
		ctx = context.Background()
		baseDir = GinkgoT().TempDir()
		gvr = schema.GroupVersionResource{Group: "group", Version: "version", Resource: "thekinds"}

		params = &helpers.Parameters{
			ObjectGroup:     "group",
			ObjectVersion:   "version",
			ObjectName:      "name-foo",
			ObjectNamespace: "ns-foo",
			CRDPlural:       "thekinds",
			PromiseName:     "test-promise",
			WorkflowType:    v1alpha1.WorkflowTypeResource,
			WorkflowAction:  v1alpha1.WorkflowActionConfigure,
			PipelineName:    "pipeline-b",
		}
	})

	Describe("the workflow the writes are keyed by", func() {
		When("the pipeline is suspending itself", func() {
			BeforeEach(func() {
				writeControlFile("suspend: true\nmessage: waiting for approval\n")
			})

			It("suspends the pipeline under the configure workflow", func() {
				newClient(newObject(map[string]any{
					"configure": map[string]any{
						"pipelines": []any{
							map[string]any{"name": "pipeline-a", "phase": "Succeeded"},
							map[string]any{"name": "pipeline-b", "phase": "Running"},
						},
					},
				}))

				Expect(UpdateStatus(ctx, baseDir, params, objectClient())).To(Succeed())

				pipelines, found, err := unstructured.NestedSlice(storedWorkflows(), "configure", "pipelines")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue(), "no pipelines stored under workflow key \"configure\"")
				Expect(pipelines).To(ContainElement(SatisfyAll(
					HaveKeyWithValue("name", "pipeline-b"),
					HaveKeyWithValue("phase", "Suspended"),
					HaveKeyWithValue("message", "waiting for approval"),
				)))
				Expect(storedWorkflows()["configure"]).To(HaveKeyWithValue("suspendedGeneration", int64(4)))
				Expect(storedForeignEntryJSON()).To(Equal(foreignEntryJSON))
			})

			It("keys by the workflow action whatever case it arrives in", func() {
				params.WorkflowAction = v1alpha1.Action("Configure")
				newClient(newObject(map[string]any{
					"configure": map[string]any{
						"pipelines": []any{
							map[string]any{"name": "pipeline-b", "phase": "Running"},
						},
					},
				}))

				Expect(UpdateStatus(ctx, baseDir, params, objectClient())).To(Succeed())

				Expect(storedWorkflows()).NotTo(HaveKey("Configure"))
				pipelines, found, err := unstructured.NestedSlice(storedWorkflows(), "configure", "pipelines")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue(), "no pipelines stored under workflow key \"configure\"")
				Expect(pipelines).To(ContainElement(SatisfyAll(
					HaveKeyWithValue("name", "pipeline-b"),
					HaveKeyWithValue("phase", "Suspended"),
				)))
			})

			It("suspends the pipeline under the delete workflow on a delete run", func() {
				params.WorkflowAction = v1alpha1.WorkflowActionDelete
				newClient(newObject(map[string]any{
					"configure": map[string]any{
						"pipelines": []any{
							map[string]any{"name": "pipeline-b", "phase": "Succeeded"},
						},
					},
					"delete": map[string]any{
						"pipelines": []any{
							map[string]any{"name": "pipeline-b", "phase": "Running"},
						},
					},
				}))

				Expect(UpdateStatus(ctx, baseDir, params, objectClient())).To(Succeed())

				deletePipelines, found, err := unstructured.NestedSlice(storedWorkflows(), "delete", "pipelines")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue(), "no pipelines stored under workflow key \"delete\"")
				Expect(deletePipelines).To(ContainElement(SatisfyAll(
					HaveKeyWithValue("name", "pipeline-b"),
					HaveKeyWithValue("phase", "Suspended"),
				)))

				// The configure entry names the same pipeline: a write that lost its key
				// would land here instead and still satisfy the assertion above.
				configurePipelines, found, err := unstructured.NestedSlice(storedWorkflows(), "configure", "pipelines")
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue(), "no pipelines stored under workflow key \"configure\"")
				Expect(configurePipelines).To(ContainElement(HaveKeyWithValue("phase", "Succeeded")))
				Expect(storedForeignEntryJSON()).To(Equal(foreignEntryJSON))
			})
		})

		When("the last pipeline completes", func() {
			It("clears the suspended generation of its own workflow only", func() {
				params.IsLastPipeline = true
				newClient(newObject(map[string]any{
					"configure": map[string]any{
						"pipelines": []any{
							map[string]any{"name": "pipeline-b", "phase": "Running"},
						},
						"suspendedGeneration": int64(3),
					},
				}))

				Expect(UpdateStatus(ctx, baseDir, params, objectClient())).To(Succeed())

				Expect(storedWorkflows()["configure"]).NotTo(HaveKey("suspendedGeneration"))
				Expect(storedForeignEntryJSON()).To(Equal(foreignEntryJSON))
			})
		})
	})

	Describe("racing the workflow engine for the same status", func() {
		It("re-reads and retries when the status update conflicts", func() {
			newClient(newObject(map[string]any{
				"configure": map[string]any{
					"pipelines": []any{
						map[string]any{"name": "pipeline-b", "phase": "Running"},
					},
				},
			}))

			gets, conflicts := 0, 0
			fakeClient.PrependReactor("get", "thekinds", func(k8stesting.Action) (bool, runtime.Object, error) {
				gets++
				return false, nil, nil
			})
			fakeClient.PrependReactor("update", "thekinds", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() != "status" || conflicts > 0 {
					return false, nil, nil
				}
				conflicts++
				return true, nil, apierrors.NewConflict(gvr.GroupResource(), "name-foo", nil)
			})

			Expect(os.WriteFile(filepath.Join(baseDir, "status.yaml"), []byte("message: all good\n"), 0600)).To(Succeed())

			Expect(UpdateStatus(ctx, baseDir, params, objectClient())).To(Succeed())

			Expect(conflicts).To(Equal(1))
			Expect(gets).To(Equal(2), "the object was not re-read before the retry")

			obj, err := objectClient().Get(ctx, "name-foo", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(obj.Object["status"]).To(HaveKeyWithValue("message", "all good"))
		})

		It("keeps status the engine wrote while the suspend label was being applied", func() {
			newClient(newObject(map[string]any{
				"configure": map[string]any{
					"pipelines": []any{
						map[string]any{"name": "pipeline-b", "phase": "Running"},
					},
				},
			}))
			writeControlFile("suspend: true\nmessage: waiting for approval\n")

			// The label Update is the writer's second round trip. Stand in for the
			// engine writing between the writer's Get and its UpdateStatus by having
			// that Update hand back an object already carrying the engine's field.
			fakeClient.PrependReactor("update", "thekinds", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() != "" {
					return false, nil, nil
				}
				obj := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
				Expect(unstructured.SetNestedField(obj.Object,
					"2026-09-14T00:00:00Z", "status", "kratix", "workflows", "configure", "lastSuccessfulTime")).To(Succeed())
				return false, nil, nil
			})

			Expect(UpdateStatus(ctx, baseDir, params, objectClient())).To(Succeed())

			Expect(storedWorkflows()["configure"]).To(
				HaveKeyWithValue("lastSuccessfulTime", "2026-09-14T00:00:00Z"))
			Expect(storedForeignEntryJSON()).To(Equal(foreignEntryJSON))
		})
	})

	Describe("a workflow type this container cannot key", func() {
		It("skips the workflow-control writes but still merges the incoming status", func() {
			params.WorkflowType = v1alpha1.Type("healthcheck")
			newClient(newObject(map[string]any{
				"configure": map[string]any{
					"pipelines": []any{
						map[string]any{"name": "pipeline-b", "phase": "Suspended"},
					},
				},
			}))
			writeControlFile("suspend: true\nmessage: waiting for approval\n")
			Expect(os.WriteFile(filepath.Join(baseDir, "status.yaml"), []byte("message: all good\n"), 0600)).To(Succeed())

			Expect(UpdateStatus(ctx, baseDir, params, objectClient())).To(Succeed())

			obj, err := objectClient().Get(ctx, "name-foo", metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			Expect(obj.Object["status"]).To(HaveKeyWithValue("message", "all good"))
			Expect(obj.GetLabels()).NotTo(HaveKey(v1alpha1.WorkflowSuspendedLabel))
			Expect(storedWorkflows()["configure"]).To(HaveKeyWithValue("pipelines", ConsistOf(
				HaveKeyWithValue("phase", "Suspended"))))
			Expect(storedForeignEntryJSON()).To(Equal(foreignEntryJSON))
		})
	})
})
