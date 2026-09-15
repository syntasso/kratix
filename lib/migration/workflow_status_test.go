package migration_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/lib/migration"
	"github.com/syntasso/kratix/lib/resourceutil"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestMigration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Migration Suite")
}

// A Promise written by an older Kratix cannot be read as a Promise at all, so
// these work on the object as it is stored.
var _ = Describe("MovePromiseWorkflowRecord", func() {
	promiseWithStatus := func(status map[string]any) *unstructured.Unstructured {
		promise := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "platform.kratix.io/v1alpha1",
			"kind":       "Promise",
			"metadata":   map[string]any{"name": "redis"},
		}}
		if status != nil {
			promise.Object["status"] = status
		}
		return promise
	}

	When("the Promise was written by an older Kratix", func() {
		var promise *unstructured.Unstructured

		BeforeEach(func() {
			promise = promiseWithStatus(map[string]any{
				"kratix": map[string]any{
					"workflows": map[string]any{
						"pipelines": []any{
							map[string]any{"name": "first-pipeline", "phase": "Succeeded"},
							map[string]any{"name": "second-pipeline", "phase": "Succeeded"},
						},
						"suspendedGeneration":                 int64(4),
						"lastSuccessfulConfigureWorkflowTime": "2026-09-14T10:00:00Z",
					},
				},
			})
			Expect(migration.MovePromiseWorkflowRecord(promise)).To(BeTrue())
		})

		It("keeps what the configure workflow recorded, under its own key", func() {
			Expect(resourceutil.GetPipelineStatuses(promise, "configure")).To(Equal(
				[]v1alpha1.WorkflowPipelineStatus{
					{Name: "first-pipeline", Phase: "Succeeded"},
					{Name: "second-pipeline", Phase: "Succeeded"},
				}))
			Expect(resourceutil.GetSuspendedGeneration(promise, "configure")).To(Equal(int64(4)))
		})

		It("removes the older layout, which the Promise type cannot read", func() {
			_, found, err := unstructured.NestedFieldNoCopy(promise.Object, "status", "kratix", "workflows", "pipelines")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("leaves the rest of the workflow status where it was", func() {
			Expect(resourceutil.GetKratixWorkflowsStatus(promise, "lastSuccessfulConfigureWorkflowTime")).
				To(Equal("2026-09-14T10:00:00Z"))
		})
	})

	When("the Promise is already in the current layout", func() {
		It("has nothing to move", func() {
			promise := promiseWithStatus(map[string]any{
				"kratix": map[string]any{"workflows": map[string]any{
					"configure": map[string]any{"pipelines": []any{
						map[string]any{"name": "only-pipeline", "phase": "Running", "hash": "abc"},
					}},
				}},
			})

			Expect(migration.MovePromiseWorkflowRecord(promise)).To(BeFalse())
			Expect(resourceutil.GetPipelineStatuses(promise, "configure")).To(Equal(
				[]v1alpha1.WorkflowPipelineStatus{{Name: "only-pipeline", Phase: "Running", Hash: "abc"}}))
		})
	})

	When("the Promise has no status at all", func() {
		It("has nothing to move", func() {
			promise := promiseWithStatus(nil)
			Expect(migration.MovePromiseWorkflowRecord(promise)).To(BeFalse())
			Expect(promise.Object).NotTo(HaveKey("status"))
		})
	})
})
