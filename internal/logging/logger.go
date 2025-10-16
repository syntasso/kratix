package logging

import (
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
)

const SeverityKey = "severity"

// Info logs a heartbeat-style message intended for all operators.
func Info(logger logr.Logger, msg string, keysAndValues ...any) {
	logger.WithValues(SeverityKey, "info").Info(msg, keysAndValues...)
}

// Warn logs a recoverable issue that may resolve during reconciliation.
func Warn(logger logr.Logger, msg string, keysAndValues ...any) {
	if logWarn(logger, msg, keysAndValues...) {
		return
	}
	logger.WithValues(SeverityKey, "warning").Info(msg, keysAndValues...)
}

// Debug logs lower-level controller actions.
func Debug(logger logr.Logger, msg string, keysAndValues ...any) {
	logger.V(1).WithValues(SeverityKey, "debug").Info(msg, keysAndValues...)
}

// Trace logs the most granular control-flow steps.
func Trace(logger logr.Logger, msg string, keysAndValues ...any) {
	logger.V(2).WithValues(SeverityKey, "trace").Info(msg, keysAndValues...)
}

// Error logs unrecoverable failures requiring human intervention.
func Error(logger logr.Logger, err error, msg string, keysAndValues ...any) {
	logger.WithValues(SeverityKey, "error").Error(err, msg, keysAndValues...)
}

func logWarn(logger logr.Logger, msg string, keysAndValues ...any) bool {
	withSeverity := logger.WithValues(SeverityKey, "warning")
	sink := withSeverity.GetSink()

	underlier, ok := sink.(zapr.Underlier)
	if !ok {
		return false
	}

	baseZap := underlier.GetUnderlying()
	if baseZap.Check(zap.WarnLevel, msg) == nil {
		return true
	}

	loggerWithFields := withSeverity.WithValues(keysAndValues...)
	fieldSink := loggerWithFields.GetSink()
	fieldUnderlier, ok := fieldSink.(zapr.Underlier)
	if !ok {
		return false
	}

	fieldUnderlier.GetUnderlying().WithOptions(zap.AddCallerSkip(1)).Warn(msg)
	return true
}
