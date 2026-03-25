package lib_test

import (
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/work-creator/lib"
)

var _ = Describe("WorkflowControl", func() {
	var samplesDir string

	BeforeEach(func() {
		samplesDir, _ = filepath.Abs("../samples/workflow-control")
	})

	When("the file is valid", func() {
		It("can parse it", func() {
			wc, err := lib.ReadWorkflowControlFile(filepath.Join(samplesDir, "workflow-control-with-retry.yaml"))
			Expect(err).NotTo(HaveOccurred())
			Expect(wc.Message).To(Equal("ananas"))
			Expect(wc.IsRetry()).To(BeTrue())
			Expect(wc.IsSuspend()).To(BeTrue())
			Expect(wc.IfSuspendOrRetry()).To(BeTrue())

			d, err := wc.RetryDuration()
			Expect(err).NotTo(HaveOccurred())
			Expect(d).To(Equal(10 * time.Minute))
		})
	})

	When("the file has an invalid 'retryAfter' configuration", func() {
		It("returns a specific error", func() {
			wc, err := lib.ReadWorkflowControlFile(filepath.Join(samplesDir, "workflow-control-with-invalid-retry.yaml"))
			Expect(err).NotTo(HaveOccurred())

			_, err = wc.RetryDuration()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to parse retry duration: \"whale\" with error: "))
		})
	})

})
