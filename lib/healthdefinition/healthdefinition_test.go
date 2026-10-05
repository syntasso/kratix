package healthdefinition_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/lib/healthdefinition"
)

const healthDefinition = `apiVersion: platform.kratix.io/v1alpha1
kind: HealthDefinition
metadata:
  name: check
`

const configMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: not-a-check
`

var _ = Describe("Count", func() {
	It("counts only HealthDefinition documents", func() {
		content := healthDefinition + "---\n" + configMap + "---\n" + healthDefinition
		Expect(healthdefinition.Count([]byte(content))).To(Equal(2))
	})

	It("ignores empty documents and comment-only markers", func() {
		content := "---\n" + healthDefinition + "--- # a comment\n\n---\n" + healthDefinition + "---\n"
		Expect(healthdefinition.Count([]byte(content))).To(Equal(2))
	})

	It("keeps a marker that carries inline content", func() {
		content := healthDefinition + "--- {apiVersion: platform.kratix.io/v1alpha1, kind: HealthDefinition}\n"
		Expect(healthdefinition.Count([]byte(content))).To(Equal(2))
	})

	It("does not treat a longer dash run as a marker", func() {
		content := healthDefinition + "----\n" + healthDefinition
		Expect(healthdefinition.Count([]byte(content))).To(Equal(0))
	})

	It("counts zero for a file with an unparseable document", func() {
		content := healthDefinition + "---\n: not: [yaml\n"
		Expect(healthdefinition.Count([]byte(content))).To(Equal(0))
	})

	It("counts zero for other kinds and empty content", func() {
		Expect(healthdefinition.Count([]byte(configMap))).To(Equal(0))
		Expect(healthdefinition.Count(nil)).To(Equal(0))
	})

	It("counts a HealthDefinition at a different apiVersion as zero", func() {
		content := "apiVersion: platform.kratix.io/v1\nkind: HealthDefinition\n"
		Expect(healthdefinition.Count([]byte(content))).To(Equal(0))
	})
})
