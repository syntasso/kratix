package lib_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/work-creator/lib"
)

var _ = Describe("ReadHealthDefinitionCount", func() {
	var countFile string

	BeforeEach(func() {
		countFile = filepath.Join(GinkgoT().TempDir(), lib.HealthDefinitionCountFile)
	})

	It("reports not found when the file is absent", func() {
		count, found, err := lib.ReadHealthDefinitionCount(countFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(count).To(BeNil())
	})

	It("parses the version and the count when present", func() {
		Expect(os.WriteFile(countFile, []byte("promiseVersion: v2.0.0\nhealthDefinitions: 2\n"), 0o600)).To(Succeed())

		count, found, err := lib.ReadHealthDefinitionCount(countFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(count.PromiseVersion).To(Equal("v2.0.0"))
		Expect(count.HealthDefinitions).To(Equal(2))
	})

	It("errors on garbage content", func() {
		Expect(os.WriteFile(countFile, []byte("promiseVersion: [unclosed"), 0o600)).To(Succeed())

		_, _, err := lib.ReadHealthDefinitionCount(countFile)
		Expect(err).To(MatchError(ContainSubstring(lib.HealthDefinitionCountFile)))
	})

	It("errors when the file cannot be read", func() {
		Expect(os.Mkdir(countFile, 0o700)).To(Succeed())

		_, _, err := lib.ReadHealthDefinitionCount(countFile)
		Expect(err).To(HaveOccurred())
	})
})
