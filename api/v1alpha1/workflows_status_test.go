package v1alpha1_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
)

// storedFlatWorkflows is the value a Promise stored before the workflow status
// was keyed carries at status.kratix.workflows.
const storedFlatWorkflows = `{"pipelines":[{"name":"first-pipeline","phase":"Succeeded",` +
	`"lastTransitionTime":"2026-01-01T00:00:00Z"},{"name":"second-pipeline","phase":"Pending",` +
	`"lastTransitionTime":"2026-01-01T00:00:00Z"}],"suspendedGeneration":3}`

func promiseJSONWithWorkflows(workflows string) []byte {
	return []byte(`{"apiVersion":"platform.kratix.io/v1alpha1","kind":"Promise",` +
		`"metadata":{"name":"stored-promise"},` +
		`"status":{"kratix":{"workflows":` + workflows + `}}}`)
}

// workflowsValueOf marshals the promise and hands back the raw bytes stored at
// status.kratix.workflows, so a round-trip can be compared byte for byte.
func workflowsValueOf(promise v1alpha1.Promise) string {
	encoded, err := json.Marshal(promise)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	var envelope struct {
		Status struct {
			Kratix struct {
				Workflows json.RawMessage `json:"workflows"`
			} `json:"kratix"`
		} `json:"status"`
	}
	ExpectWithOffset(1, json.Unmarshal(encoded, &envelope)).To(Succeed())
	return string(envelope.Status.Kratix.Workflows)
}

var _ = Describe("WorkflowsStatus", func() {
	Describe("decoding a Promise stored before the workflow status was keyed", func() {
		var promise v1alpha1.Promise

		BeforeEach(func() {
			promise = v1alpha1.Promise{}
			Expect(json.Unmarshal(promiseJSONWithWorkflows(storedFlatWorkflows), &promise)).To(Succeed())
		})

		It("carries the stored flat layout through a marshal round-trip byte for byte", func() {
			Expect(promise.GetName()).To(Equal("stored-promise"))
			Expect(promise.Status.Kratix.Workflows.Actions).To(BeNil())
			Expect(string(promise.Status.Kratix.Workflows.LegacyRaw)).To(Equal(storedFlatWorkflows))
			Expect(workflowsValueOf(promise)).To(Equal(storedFlatWorkflows))
		})
	})

	Describe("decoding the keyed layout", func() {
		It("decodes each workflow under its own key", func() {
			var promise v1alpha1.Promise
			Expect(json.Unmarshal(promiseJSONWithWorkflows(
				`{"configure":{"pipelines":[{"name":"first-pipeline","phase":"Succeeded","hash":"abc123"}]},`+
					`"delete":{"pipelines":[{"name":"cleanup","phase":"Pending"}],"lastSuccessfulTime":"2026-02-02T00:00:00Z"}}`,
			), &promise)).To(Succeed())

			Expect(promise.Status.Kratix.Workflows.LegacyRaw).To(BeEmpty())

			configure := promise.Status.Kratix.Workflows.Get(string(v1alpha1.WorkflowActionConfigure))
			Expect(configure.Pipelines).To(HaveLen(1))
			Expect(configure.Pipelines[0].Hash).To(Equal("abc123"))

			deleteStatus := promise.Status.Kratix.Workflows.Get(string(v1alpha1.WorkflowActionDelete))
			Expect(deleteStatus.Pipelines).To(ConsistOf(v1alpha1.WorkflowPipelineStatus{
				Name:  "cleanup",
				Phase: v1alpha1.WorkflowPhasePending,
			}))
			Expect(deleteStatus.LastSuccessfulTime).To(Equal("2026-02-02T00:00:00Z"))
		})

		It("treats an absent, null or empty value as no workflow status", func() {
			for _, stored := range []string{`null`, `{}`} {
				var promise v1alpha1.Promise
				Expect(json.Unmarshal(promiseJSONWithWorkflows(stored), &promise)).To(Succeed())
				Expect(promise.Status.Kratix.Workflows.Actions).To(BeNil())
				Expect(promise.Status.Kratix.Workflows.LegacyRaw).To(BeEmpty())
				Expect(promise.Status.Kratix.Workflows.IsZero()).To(BeTrue())
			}
		})
	})

	Describe("ClearPipelineExecutionStatus", func() {
		var promise v1alpha1.Promise

		BeforeEach(func() {
			promise = v1alpha1.Promise{}
			promise.Status.Kratix.Workflows.Set(string(v1alpha1.WorkflowActionConfigure), v1alpha1.WorkflowStatus{
				Pipelines: []v1alpha1.WorkflowPipelineStatus{{Name: "first-pipeline", Phase: v1alpha1.WorkflowPhaseSucceeded}},
			})
			promise.Status.Kratix.Workflows.Set(string(v1alpha1.WorkflowActionDelete), v1alpha1.WorkflowStatus{
				Pipelines: []v1alpha1.WorkflowPipelineStatus{{Name: "cleanup", Phase: v1alpha1.WorkflowPhasePending}},
			})
			promise.Status.Kratix.Workflows.Set("embedding-controller", v1alpha1.WorkflowStatus{
				Pipelines: []v1alpha1.WorkflowPipelineStatus{{Name: "theirs", Phase: v1alpha1.WorkflowPhaseRunning}},
			})
		})

		It("clears the workflows Kratix core owns", func() {
			Expect(promise.ClearPipelineExecutionStatus()).To(BeTrue())

			Expect(promise.Status.Kratix.Workflows.Actions).NotTo(HaveKey("configure"))
			Expect(promise.Status.Kratix.Workflows.Actions).NotTo(HaveKey("delete"))
		})

		It("leaves the keys of a controller that embeds the workflow engine alone", func() {
			Expect(promise.ClearPipelineExecutionStatus()).To(BeTrue())

			Expect(promise.Status.Kratix.Workflows.Get("embedding-controller").Pipelines).To(ConsistOf(
				v1alpha1.WorkflowPipelineStatus{Name: "theirs", Phase: v1alpha1.WorkflowPhaseRunning},
			))
		})

		It("clears a stored flat layout, which is core's too", func() {
			var stored v1alpha1.Promise
			Expect(json.Unmarshal(promiseJSONWithWorkflows(storedFlatWorkflows), &stored)).To(Succeed())

			Expect(stored.ClearPipelineExecutionStatus()).To(BeTrue())
			Expect(stored.Status.Kratix.Workflows.LegacyRaw).To(BeEmpty())
			Expect(stored.Status.Kratix.Workflows.IsZero()).To(BeTrue())
		})

		It("reports no change when there was nothing of core's to clear", func() {
			promise.Status.Kratix.Workflows.Actions = map[string]v1alpha1.WorkflowStatus{
				"embedding-controller": {Pipelines: []v1alpha1.WorkflowPipelineStatus{{Name: "theirs"}}},
			}

			Expect(promise.ClearPipelineExecutionStatus()).To(BeFalse())
		})
	})
})
