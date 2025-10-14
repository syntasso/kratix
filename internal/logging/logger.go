package logging

import "github.com/go-logr/logr"

const SeverityKey = "severity"

// Info logs a heartbeat-style message intended for all operators.
func Info(logger logr.Logger, msg string, keysAndValues ...interface{}) {
	logger.WithValues(SeverityKey, "info").Info(msg, keysAndValues...)
}

// Warn logs a recoverable issue that may resolve during reconciliation.
func Warn(logger logr.Logger, msg string, keysAndValues ...interface{}) {
	logger.WithValues(SeverityKey, "warning").Info(msg, keysAndValues...)
}

// Debug logs lower-level controller actions.
func Debug(logger logr.Logger, msg string, keysAndValues ...interface{}) {
	logger.V(1).WithValues(SeverityKey, "debug").Info(msg, keysAndValues...)
}

// Trace logs the most granular control-flow steps.
func Trace(logger logr.Logger, msg string, keysAndValues ...interface{}) {
	logger.V(2).WithValues(SeverityKey, "trace").Info(msg, keysAndValues...)
}

// Error logs unrecoverable failures requiring human intervention.
func Error(logger logr.Logger, err error, msg string, keysAndValues ...interface{}) {
	logger.WithValues(SeverityKey, "error").Error(err, msg, keysAndValues...)
}
