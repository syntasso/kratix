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

var _ = Describe("WorkflowsStatus", func() {
	Describe("decoding a Promise stored before the workflow status was keyed", func() {
		var promise v1alpha1.Promise

		BeforeEach(func() {
			promise = v1alpha1.Promise{}
			Expect(json.Unmarshal(promiseJSONWithWorkflows(storedFlatWorkflows), &promise)).To(Succeed())
		})

		It("decodes without error, into no workflow status at all", func() {
			Expect(promise.GetName()).To(Equal("stored-promise"))
			Expect(promise.Status.Kratix.Workflows).To(BeEmpty())
		})

		It("clears the workflow status already in the value the decoder is reusing", func() {
			Expect(json.Unmarshal(promiseJSONWithWorkflows(
				`{"configure":{"pipelines":[{"name":"first-pipeline","phase":"Succeeded"}]}}`,
			), &promise)).To(Succeed())
			Expect(promise.Status.Kratix.Workflows).To(HaveKey("configure"))

			Expect(json.Unmarshal(promiseJSONWithWorkflows(storedFlatWorkflows), &promise)).To(Succeed())

			Expect(promise.Status.Kratix.Workflows).To(BeEmpty())
		})
	})

	Describe("decoding a workflow key that is not a workflow status", func() {
		var promise v1alpha1.Promise

		BeforeEach(func() {
			promise = v1alpha1.Promise{}
			Expect(json.Unmarshal(promiseJSONWithWorkflows(
				`{"configure":{"pipelines":[{"name":"first-pipeline","phase":"Succeeded"}]},`+
					`"delete":{"pipelines":[{"name":"cleanup","phase":"Pending"}]},`+
					`"their-workflow":{"pipelines":[{"name":"theirs","phase":"Running"}]},`+
					`"their-other-workflow":{"suspendedGeneration":"not-a-generation"}}`,
			), &promise)).To(Succeed())
		})

		It("keeps every key that does decode, Kratix's own and the embedding controller's", func() {
			Expect(promise.Status.Kratix.Workflows).To(HaveKey("configure"))
			Expect(promise.Status.Kratix.Workflows).To(HaveKey("delete"))
			Expect(promise.Status.Kratix.Workflows["their-workflow"].Pipelines).To(ConsistOf(
				v1alpha1.WorkflowPipelineStatus{Name: "theirs", Phase: v1alpha1.WorkflowPhaseRunning},
			))
			Expect(promise.Status.Kratix.Workflows).NotTo(HaveKey("their-other-workflow"))
		})

		It("re-encodes the surviving keys, so the next status write does not delete them", func() {
			encoded, err := json.Marshal(promise)
			Expect(err).NotTo(HaveOccurred())

			Expect(string(encoded)).To(ContainSubstring(`"configure":`))
			Expect(string(encoded)).To(ContainSubstring(`"delete":`))
			Expect(string(encoded)).To(ContainSubstring(`"their-workflow":`))
		})
	})

	Describe("decoding the keyed layout", func() {
		It("decodes each workflow under its own key", func() {
			var promise v1alpha1.Promise
			Expect(json.Unmarshal(promiseJSONWithWorkflows(
				`{"configure":{"pipelines":[{"name":"first-pipeline","phase":"Succeeded","hash":"abc123"}]},`+
					`"delete":{"pipelines":[{"name":"cleanup","phase":"Pending"}],"lastSuccessfulTime":"2026-02-02T00:00:00Z"}}`,
			), &promise)).To(Succeed())

			configure := promise.Status.Kratix.Workflows[string(v1alpha1.WorkflowActionConfigure)]
			Expect(configure.Pipelines).To(HaveLen(1))
			Expect(configure.Pipelines[0].Hash).To(Equal("abc123"))

			deleteStatus := promise.Status.Kratix.Workflows[string(v1alpha1.WorkflowActionDelete)]
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
				Expect(promise.Status.Kratix.Workflows).To(BeEmpty())
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

			Expect(promise.Status.Kratix.Workflows).NotTo(HaveKey("configure"))
			Expect(promise.Status.Kratix.Workflows).NotTo(HaveKey("delete"))
		})

		It("leaves the keys of a controller that embeds the workflow engine alone", func() {
			Expect(promise.ClearPipelineExecutionStatus()).To(BeTrue())

			Expect(promise.Status.Kratix.Workflows["embedding-controller"].Pipelines).To(ConsistOf(
				v1alpha1.WorkflowPipelineStatus{Name: "theirs", Phase: v1alpha1.WorkflowPhaseRunning},
			))
		})

		It("reports no change when there was nothing of core's to clear", func() {
			promise.Status.Kratix.Workflows = v1alpha1.WorkflowsStatus{
				"embedding-controller": {Pipelines: []v1alpha1.WorkflowPipelineStatus{{Name: "theirs"}}},
			}

			Expect(promise.ClearPipelineExecutionStatus()).To(BeFalse())
		})
	})
})
