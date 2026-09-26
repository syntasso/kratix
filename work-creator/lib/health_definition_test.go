package lib_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/work-creator/lib"
)

var _ = Describe("ReadHealthDefinitionsMarker", func() {
	var markerFile string

	BeforeEach(func() {
		markerFile = filepath.Join(GinkgoT().TempDir(), lib.HealthDefinitionsMarkerFile)
	})

	It("reports not found when the marker is absent", func() {
		marker, found, err := lib.ReadHealthDefinitionsMarker(markerFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(marker).To(BeNil())
	})

	It("parses the promise version when present", func() {
		Expect(os.WriteFile(markerFile, []byte("promiseVersion: v2.0.0\n"), 0o600)).To(Succeed())

		marker, found, err := lib.ReadHealthDefinitionsMarker(markerFile)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(marker.PromiseVersion).To(Equal("v2.0.0"))
	})

	It("errors on garbage content", func() {
		Expect(os.WriteFile(markerFile, []byte("promiseVersion: [unclosed"), 0o600)).To(Succeed())

		_, _, err := lib.ReadHealthDefinitionsMarker(markerFile)
		Expect(err).To(MatchError(ContainSubstring(lib.HealthDefinitionsMarkerFile)))
	})

	It("errors when the marker cannot be read", func() {
		Expect(os.Mkdir(markerFile, 0o700)).To(Succeed())

		_, _, err := lib.ReadHealthDefinitionsMarker(markerFile)
		Expect(err).To(HaveOccurred())
	})
})
