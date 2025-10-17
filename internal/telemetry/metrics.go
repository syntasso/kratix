package telemetry

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	instrumentationName             = "github.com/syntasso/kratix/internal/telemetry"
	WorkPlacementWritesMetric       = "kratix_workplacement_writes_total"
	WorkPlacementWriteResultSuccess = "success"
	WorkPlacementWriteResultFailure = "failure"
	WorkPlacementOutcomesMetric     = "kratix_workplacement_outcomes_total"
	WorkPlacementOutcomeScheduled   = "scheduled"
	WorkPlacementOutcomeUnscheduled = "unscheduled"
	WorkPlacementOutcomeMisplaced   = "misplaced"
)

var (
	workPlacementWriteCounter     metric.Int64Counter
	workPlacementWriteCounterOnce sync.Once
	errWorkPlacementWrite         error

	workPlacementOutcomeCounter     metric.Int64Counter
	workPlacementOutcomeCounterOnce sync.Once
	errWorkPlacementOutcome         error
)

// WorkPlacementWriteAttributes builds OpenTelemetry attributes describing a WorkPlacement write attempt.
func WorkPlacementWriteAttributes(promise, resource, destination, pipeline string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 4)
	if promise != "" {
		attrs = append(attrs, attribute.String("promise", promise))
	}
	if resource != "" {
		attrs = append(attrs, attribute.String("resource", resource))
	}
	if destination != "" {
		attrs = append(attrs, attribute.String("destination", destination))
	}
	if pipeline != "" {
		attrs = append(attrs, attribute.String("pipeline", pipeline))
	}
	return attrs
}

// RecordWorkPlacementWrite records a WorkPlacement write attempt with the supplied attributes.
func RecordWorkPlacementWrite(ctx context.Context, result string, attrs ...attribute.KeyValue) {
	workPlacementWriteCounterOnce.Do(func() {
		meter := otel.Meter(instrumentationName)
		workPlacementWriteCounter, errWorkPlacementWrite = meter.Int64Counter(
			WorkPlacementWritesMetric,
			metric.WithDescription("Total number of WorkPlacement write attempts"),
		)
	})
	if errWorkPlacementWrite != nil {
		return
	}

	allAttrs := append([]attribute.KeyValue{attribute.String("result", result)}, attrs...)
	workPlacementWriteCounter.Add(ctx, 1, metric.WithAttributes(allAttrs...))
}

// WorkPlacementOutcomeAttributes builds OpenTelemetry attributes describing the result of scheduling a WorkPlacement.
func WorkPlacementOutcomeAttributes(promise, resource, namespace, destination string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 4)
	if promise != "" {
		attrs = append(attrs, attribute.String("promise", promise))
	}
	if resource != "" {
		attrs = append(attrs, attribute.String("resource", resource))
	}
	if namespace != "" {
		attrs = append(attrs, attribute.String("namespace", namespace))
	}
	if destination != "" {
		attrs = append(attrs, attribute.String("destination", destination))
	}
	return attrs
}

// RecordWorkPlacementOutcome records a WorkPlacement scheduling outcome with the supplied attributes.
func RecordWorkPlacementOutcome(ctx context.Context, outcome string, attrs ...attribute.KeyValue) {
	workPlacementOutcomeCounterOnce.Do(func() {
		meter := otel.Meter(instrumentationName)
		workPlacementOutcomeCounter, errWorkPlacementOutcome = meter.Int64Counter(
			WorkPlacementOutcomesMetric,
			metric.WithDescription("Total number of WorkPlacement scheduling outcomes"),
		)
	})
	if errWorkPlacementOutcome != nil {
		return
	}

	allAttrs := append([]attribute.KeyValue{attribute.String("result", outcome)}, attrs...)
	workPlacementOutcomeCounter.Add(ctx, 1, metric.WithAttributes(allAttrs...))
}

// ResetWorkPlacementMetricsForTest clears cached metric state to allow tests to install a fresh meter provider.
func ResetWorkPlacementMetricsForTest() {
	workPlacementWriteCounter = nil
	errWorkPlacementWrite = nil
	workPlacementWriteCounterOnce = sync.Once{}

	workPlacementOutcomeCounter = nil
	errWorkPlacementOutcome = nil
	workPlacementOutcomeCounterOnce = sync.Once{}
}
