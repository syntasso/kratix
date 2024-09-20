package utils_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	utils "github.com/syntasso/kratix/work-creator/pipeline/lib"
)

var _ = Describe("Utils", func() {
	var (
		longContent []byte
	)

	Describe("CompressContent", func() {
		BeforeEach(func() {
			longContent = []byte(`This is a string, a long long long long long long long long string
				This is a string, a long long long long long long long long string
				This is a string, a long long long long long long long long string
				This is a string, a long long long long long long long long string
				This is a string, a long long long long long long long long string
				This is a string, a long long long long long long long long string
				This is a string, a long long long long long long long long string
			`)
		})

		It("CompressContent compresses a long byte array", func() {
			longContentSize := len(longContent)
			compressedContent, err := utils.CompressContent(longContent)
			Expect(err).ToNot(HaveOccurred())
			Expect(longContentSize > len(compressedContent)).To(BeTrue())
		})

		It("DecompressContent decompresses compressed content", func() {
			longContentSize := len(longContent)
			compressedContent, err := utils.CompressContent(longContent)
			Expect(err).ToNot(HaveOccurred())
			decompressedContent, err := utils.DecompressContent(compressedContent)
			Expect(err).ToNot(HaveOccurred())
			Expect(decompressedContent).To(HaveLen(longContentSize))
			Expect(decompressedContent).To(Equal(longContent))
		})
	})
})
