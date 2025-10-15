package logging_test

import (
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/syntasso/kratix/internal/logging"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

var _ = Describe("Severity helpers", func() {
	var (
		core     zapcore.Core
		recorded *observer.ObservedLogs
		logger   logr.Logger
	)

	BeforeEach(func() {
		core, recorded = observer.New(zapcore.Level(-2)) // allow trace-level messages
		zapLogger := zap.New(core)
		logger = zapr.NewLogger(zapLogger)
	})

	It("annotates severity levels for each helper", func() {
		testCases := []struct {
			name     string
			call     func(logr.Logger)
			expected string
		}{
			{
				name: "info",
				call: func(l logr.Logger) {
					logging.Info(l, "info-message")
				},
				expected: "info",
			},
			{
				name: "warn",
				call: func(l logr.Logger) {
					logging.Warn(l, "warn-message")
				},
				expected: "warning",
			},
			{
				name: "debug",
				call: func(l logr.Logger) {
					logging.Debug(l, "debug-message")
				},
				expected: "debug",
			},
			{
				name: "trace",
				call: func(l logr.Logger) {
					logging.Trace(l, "trace-message")
				},
				expected: "trace",
			},
			{
				name: "error",
				call: func(l logr.Logger) {
					logging.Error(l, nil, "error-message")
				},
				expected: "error",
			},
		}

		for idx, tc := range testCases {
			tc.call(logger)
			entries := recorded.All()
			Expect(entries).To(HaveLen(idx+1), "unexpected number of log entries for %s", tc.name)

			last := entries[len(entries)-1]
			Expect(severityFromFields(last.Context)).To(Equal(tc.expected), "unexpected severity for %s", tc.name)
		}
	})
})

var _ = Describe("Warn log level gating", func() {
	It("emits warnings even when Info level is disabled", func() {
		core, recorded := observer.New(zapcore.WarnLevel)
		zapLogger := zap.New(core)
		logger := zapr.NewLogger(zapLogger)

		logging.Info(logger, "info suppressed")
		Expect(recorded.Len()).To(Equal(0))

		logging.Warn(logger, "warn-message")
		entries := recorded.All()
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Entry.Level).To(Equal(zapcore.WarnLevel))
		Expect(severityFromFields(entries[0].Context)).To(Equal("warning"))
	})
})

func severityFromFields(fields []zapcore.Field) string {
	for _, field := range fields {
		if field.Key == logging.SeverityKey && field.Type == zapcore.StringType {
			return field.String
		}
	}
	return ""
}
