package hash_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/lib/hash"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ = Describe("Hasher", func() {
	var (
		input *unstructured.Unstructured
	)

	BeforeEach(func() {
		input = &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata": map[string]interface{}{
					"name":      "test-pod",
					"namespace": "test-namespace",
				},
				"spec": map[string]interface{}{
					"foo": "bar",
				},
				"status": map[string]interface{}{
					"some": "status",
				},
			},
		}
	})

	Describe("ComputeHash", func() {
		const expectedHash = "9bb58f26192e4ba00f01e2e7b136bbd8"

		It("hashes the inpuy Object Spec", func() {
			actualHash, err := hash.ComputeHashForResource(input)
			Expect(err).NotTo(HaveOccurred())
			Expect(actualHash).To(Equal(expectedHash))
		})

		It("remains constant if the request spec hasn't changed", func() {
			input.Object["metadata"].(map[string]interface{})["annotations"] = map[string]interface{}{
				"foo": "bar",
			}
			input.Object["status"] = map[string]interface{}{
				"another": "status",
				"yet":     "another",
			}

			actualHash, err := hash.ComputeHashForResource(input)
			Expect(err).NotTo(HaveOccurred())
			Expect(actualHash).To(Equal(expectedHash))
		})

		It("gets updated if the request spec changes", func() {
			input.Object["spec"].(map[string]interface{})["annotations"] = map[string]interface{}{
				"foo": "not-bar",
			}
			actualHash, err := hash.ComputeHashForResource(input)
			Expect(err).NotTo(HaveOccurred())
			Expect(actualHash).NotTo(Equal(expectedHash))
		})
	})
})
