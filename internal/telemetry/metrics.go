package telemetry

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

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

type gaugeRecorder struct {
	name        string
	description string

	once  sync.Once
	gauge metric.Int64Gauge
	err   error

	totals sync.Map // map[string]*atomic.Int64
}

func newGaugeRecorder(name, description string) *gaugeRecorder {
	return &gaugeRecorder{name: name, description: description}
}

func (gr *gaugeRecorder) record(ctx context.Context, delta int64, attrs ...attribute.KeyValue) {
	gr.once.Do(func() {
		meter := otel.Meter(instrumentationName)
		gr.gauge, gr.err = meter.Int64Gauge(
			gr.name,
			metric.WithDescription(gr.description),
		)
	})
	if gr.err != nil {
		return
	}

	key := attributesKey(attrs)
	valAny, _ := gr.totals.LoadOrStore(key, &atomic.Int64{})
	total := valAny.(*atomic.Int64).Add(delta)
	gr.gauge.Record(ctx, total, metric.WithAttributes(attrs...))
}

func (gr *gaugeRecorder) reset() {
	gr.gauge = nil
	gr.err = nil
	gr.once = sync.Once{}
	gr.totals = sync.Map{}
}

func attributesKey(attrs []attribute.KeyValue) string {
	if len(attrs) == 0 {
		return ""
	}
	set := attribute.NewSet(attrs...)
	iter := set.Iter()
	var b strings.Builder
	for iter.Next() {
		attr := iter.Attribute()
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(string(attr.Key))
		b.WriteByte('=')
		b.WriteString(attr.Value.AsString())
	}
	return b.String()
}

var (
	workPlacementWriteGauge   = newGaugeRecorder(WorkPlacementWritesMetric, "Total number of WorkPlacement write attempts")
	workPlacementOutcomeGauge = newGaugeRecorder(WorkPlacementOutcomesMetric, "Total number of WorkPlacement scheduling outcomes")
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

// RecordWorkPlacementWrite increments the WorkPlacement write total with the supplied attributes.
func RecordWorkPlacementWrite(ctx context.Context, result string, attrs ...attribute.KeyValue) {
	allAttrs := make([]attribute.KeyValue, 0, len(attrs)+1)
	allAttrs = append(allAttrs, attribute.String("result", result))
	allAttrs = append(allAttrs, attrs...)
	workPlacementWriteGauge.record(ctx, 1, allAttrs...)
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

// RecordWorkPlacementOutcome increments the WorkPlacement outcome total with the supplied attributes.
func RecordWorkPlacementOutcome(ctx context.Context, outcome string, attrs ...attribute.KeyValue) {
	allAttrs := make([]attribute.KeyValue, 0, len(attrs)+1)
	allAttrs = append(allAttrs, attribute.String("result", outcome))
	allAttrs = append(allAttrs, attrs...)
	workPlacementOutcomeGauge.record(ctx, 1, allAttrs...)
}

// ResetWorkPlacementMetricsForTest clears cached metric state to allow tests to install a fresh meter provider.
func ResetWorkPlacementMetricsForTest() {
	workPlacementWriteGauge.reset()
	workPlacementOutcomeGauge.reset()
}
