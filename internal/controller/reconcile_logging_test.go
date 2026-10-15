package controller_test

import (
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/internal/controller"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("WithTrace", func() {
	var logLines []string
	var logger logr.Logger

	BeforeEach(func() {
		logLines = nil
		logger = funcr.New(func(prefix, args string) {
			logLines = append(logLines, args)
		}, funcr.Options{})
	})

	It("logs the error and requeueAfter at info level when the reconcile fails", func() {
		_, err := controller.WithTrace(logger, func() (ctrl.Result, error) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, fmt.Errorf("boom")
		})
		Expect(err).To(MatchError("boom"))

		Expect(logLines).To(HaveLen(2))
		Expect(logLines[1]).To(ContainSubstring(`"msg"="reconciliation failed"`))
		Expect(logLines[1]).To(ContainSubstring(`"severity"="info"`))
		Expect(logLines[1]).To(ContainSubstring(`"error"="boom"`))
		Expect(logLines[1]).To(ContainSubstring(`"requeueAfter"="5s"`))
	})

	It("logs requeueAfter when the reconcile succeeds", func() {
		_, err := controller.WithTrace(logger, func() (ctrl.Result, error) {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(logLines).To(HaveLen(2))
		Expect(logLines[1]).To(ContainSubstring(`"msg"="reconciliation finished"`))
		Expect(logLines[1]).To(ContainSubstring(`"requeueAfter"="5s"`))
	})
})
