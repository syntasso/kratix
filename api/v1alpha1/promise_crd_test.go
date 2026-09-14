package v1alpha1_test

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// promiseCRDPath is the CRD as committed, which is what distribution/kratix.yaml
// ships and what an operator applies. These specs read it rather than regenerate
// it: a marker dropped without re-running `make manifests` leaves them green, and
// nothing in the Makefile checks the generated manifests for drift.
const promiseCRDPath = "../../config/crd/bases/platform.kratix.io_promises.yaml"

var _ = Describe("the committed Promise CRD at status.kratix.workflows", func() {
	var workflows apiextensionsv1.JSONSchemaProps

	BeforeEach(func() {
		raw, err := os.ReadFile(promiseCRDPath)
		Expect(err).NotTo(HaveOccurred())

		crd := apiextensionsv1.CustomResourceDefinition{}
		Expect(yaml.Unmarshal(raw, &crd)).To(Succeed())
		Expect(crd.Spec.Versions).To(HaveLen(1))

		node := crd.Spec.Versions[0].Schema.OpenAPIV3Schema
		for _, field := range []string{"status", "kratix", "workflows"} {
			child, found := node.Properties[field]
			Expect(found).To(BeTrue(), "the CRD has no %s under the path to status.kratix.workflows", field)
			node = &child
		}
		workflows = *node
	})

	It("is typed as an object", func() {
		Expect(workflows.Type).To(Equal("object"),
			"+kubebuilder:validation:Type=object is gone from Promise's Workflows field: a Schemaless "+
				"node with no type of its own is not a structural schema, and the apiserver rejects the CRD")
	})

	It("preserves the workflow keys the apiserver stores under it", func() {
		Expect(workflows.XPreserveUnknownFields).NotTo(BeNil())
		Expect(*workflows.XPreserveUnknownFields).To(BeTrue(),
			"+kubebuilder:pruning:PreserveUnknownFields is gone from Promise's Workflows field: an object "+
				"declaring no properties prunes everything stored under it, so every keyed workflow status "+
				"written to a Promise reads back as {}")
	})

	It("declares no shape of its own, so both status layouts validate", func() {
		Expect(workflows.Properties).To(BeEmpty(), schemalessFailure)
		Expect(workflows.AdditionalProperties).To(BeNil(), schemalessFailure)
	})
})

const schemalessFailure = "+kubebuilder:validation:Schemaless is gone from Promise's Workflows field: " +
	"controller-gen then declares the WorkflowStatus shape under additionalProperties, and the pre-keyed " +
	"flat layout (pipelines as a list, suspendedGeneration as a number) fails validation on the very " +
	"upgrade this node is widened to survive"
