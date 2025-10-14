package telemetry

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	kratixPrefix              = "kratix.io/"
	TraceParentAnnotation     = kratixPrefix + "trace-parent"
	TraceStateAnnotation      = kratixPrefix + "trace-state"
	TraceTimestampAnnotation  = kratixPrefix + "trace-timestamp"
	TraceGenerationAnnotation = kratixPrefix + "trace-generation"
	TraceParentEnvVar         = "KRATIX_TRACE_PARENT"
	TraceStateEnvVar          = "KRATIX_TRACE_STATE"

	traceparentKey = "traceparent"
	tracestateKey  = "tracestate"
)

var (
	tracePropagator = propagation.TraceContext{}
	traceMaxAge     = 24 * time.Hour
	nowFunc         = time.Now
)

// StartSpanForObject ensures trace annotations are present on obj and starts a span whose
// parent is derived from those annotations. The returned boolean indicates whether the
// caller should persist obj because its annotations were updated.
func StartSpanForObject(ctx context.Context, tracer trace.Tracer, obj metav1.Object, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span, bool, error) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	if needsNewTrace(obj, annotations) {
		return startNewTrace(ctx, tracer, obj, spanName, annotations, opts...)
	}

	carrier := propagation.MapCarrier{}
	parent := annotations[TraceParentAnnotation]
	carrier.Set(traceparentKey, parent)
	if state := annotations[TraceStateAnnotation]; state != "" {
		carrier.Set(tracestateKey, state)
	}

	extracted := tracePropagator.Extract(ctx, carrier)
	spanCtx := trace.SpanContextFromContext(extracted)
	if !spanCtx.IsValid() {
		return startNewTrace(ctx, tracer, obj, spanName, annotations, opts...)
	}

	mutated := ensureExistingTraceMetadata(obj, annotations)
	ctx, span := tracer.Start(extracted, spanName, opts...)
	if mutated {
		obj.SetAnnotations(annotations)
	}
	return ctx, span, mutated, nil
}

func startNewTrace(
	ctx context.Context,
	tracer trace.Tracer,
	obj metav1.Object,
	spanName string,
	annotations map[string]string,
	opts ...trace.SpanStartOption,
) (context.Context, trace.Span, bool, error) {
	startOpts := append([]trace.SpanStartOption{trace.WithNewRoot()}, opts...)
	ctx, span := tracer.Start(ctx, spanName, startOpts...)
	if !span.SpanContext().IsValid() {
		return ctx, span, false, nil
	}
	storeSpanContext(annotations, span.SpanContext())
	annotateNewSpan(obj, annotations)
	obj.SetAnnotations(annotations)
	return ctx, span, true, nil
}

func annotateNewSpan(obj metav1.Object, annotations map[string]string) {
	annotations[TraceTimestampAnnotation] = nowFunc().UTC().Format(time.RFC3339Nano)
	if gen := obj.GetGeneration(); gen > 0 {
		annotations[TraceGenerationAnnotation] = strconv.FormatInt(gen, 10)
	} else {
		delete(annotations, TraceGenerationAnnotation)
	}
}

func ensureExistingTraceMetadata(obj metav1.Object, annotations map[string]string) bool {
	mutated := false
	if annotations[TraceTimestampAnnotation] == "" {
		annotations[TraceTimestampAnnotation] = nowFunc().UTC().Format(time.RFC3339Nano)
		mutated = true
	}
	if gen := obj.GetGeneration(); gen > 0 {
		if annotations[TraceGenerationAnnotation] == "" {
			annotations[TraceGenerationAnnotation] = strconv.FormatInt(gen, 10)
			mutated = true
		}
	}
	return mutated
}

func needsNewTrace(obj metav1.Object, annotations map[string]string) bool {
	parent := annotations[TraceParentAnnotation]
	if parent == "" {
		return true
	}

	carrier := propagation.MapCarrier{}
	carrier.Set(traceparentKey, parent)
	if state := annotations[TraceStateAnnotation]; state != "" {
		carrier.Set(tracestateKey, state)
	}
	extracted := tracePropagator.Extract(context.Background(), carrier)
	if !trace.SpanContextFromContext(extracted).IsValid() {
		return true
	}

	if tsStr := annotations[TraceTimestampAnnotation]; tsStr != "" {
		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			return true
		}
		if nowFunc().Sub(ts) > traceMaxAge {
			return true
		}
	}

	if gen := obj.GetGeneration(); gen > 0 {
		if genStr := annotations[TraceGenerationAnnotation]; genStr != "" {
			val, err := strconv.ParseInt(genStr, 10, 64)
			if err != nil {
				return true
			}
			if val != gen {
				return true
			}
		}
	}
	return false
}

func storeSpanContext(annotations map[string]string, spanCtx trace.SpanContext) {
	carrier := propagation.MapCarrier{}
	ctx := trace.ContextWithSpanContext(context.Background(), spanCtx)
	tracePropagator.Inject(ctx, carrier)
	if tp := carrier.Get(traceparentKey); tp != "" {
		annotations[TraceParentAnnotation] = tp
	}
	if ts := carrier.Get(tracestateKey); ts != "" {
		annotations[TraceStateAnnotation] = ts
	} else {
		delete(annotations, TraceStateAnnotation)
	}
}

// CopyTraceAnnotations copies trace annotations from src to dst.
func CopyTraceAnnotations(dst map[string]string, src map[string]string) map[string]string {
	if dst == nil {
		dst = map[string]string{}
	}
	if src == nil {
		return dst
	}
	for _, key := range []string{TraceParentAnnotation, TraceStateAnnotation, TraceTimestampAnnotation, TraceGenerationAnnotation} {
		if val, ok := src[key]; ok && val != "" {
			dst[key] = val
		}
	}
	return dst
}

// ApplyTraceAnnotations ensures the provided annotations map contains the supplied
// traceparent and tracestate values.
func ApplyTraceAnnotations(annotations map[string]string, traceParent, traceState string) map[string]string {
	if traceParent == "" {
		return annotations
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[TraceParentAnnotation] = traceParent
	if traceState != "" {
		annotations[TraceStateAnnotation] = traceState
	} else {
		delete(annotations, TraceStateAnnotation)
	}
	return annotations
}

// ContextWithTraceparent returns a context containing the provided trace parent values.
func ContextWithTraceparent(ctx context.Context, traceParent, traceState string) (context.Context, bool) {
	if traceParent == "" {
		return ctx, false
	}
	carrier := propagation.MapCarrier{}
	carrier.Set(traceparentKey, traceParent)
	if traceState != "" {
		carrier.Set(tracestateKey, traceState)
	}
	extracted := tracePropagator.Extract(ctx, carrier)
	spanCtx := trace.SpanContextFromContext(extracted)
	if !spanCtx.IsValid() {
		return ctx, false
	}
	return extracted, true
}

// LoggerWithTrace enriches the logger with the trace id if available.
func LoggerWithTrace(logger logr.Logger, span trace.Span) logr.Logger {
	sc := span.SpanContext()
	if !sc.IsValid() {
		return logger
	}
	return LoggerWithTraceID(logger, sc.TraceID().String())
}

// RecordError annotates the span status with the provided error if non-nil.
func RecordError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// SetCommonAttributes attaches common flow attributes to the span.
func SetCommonAttributes(span trace.Span, attrs ...attribute.KeyValue) {
	if span == nil {
		return
	}
	span.SetAttributes(attrs...)
}

// TraceParentFromEnv reads the trace parent information from the environment.
func TraceParentFromEnv() (string, string) {
	return os.Getenv(TraceParentEnvVar), os.Getenv(TraceStateEnvVar)
}

// SetTraceMaxAge is used in tests to override the default duration.
func SetTraceMaxAge(d time.Duration) {
	traceMaxAge = d
}

// ResetTimeNow is used in tests to reset the injected time function.
func ResetTimeNow() {
	nowFunc = time.Now
}

// SetTimeNow overrides the now function, primarily for testing.
func SetTimeNow(f func() time.Time) {
	nowFunc = f
}

// TraceInfoString returns a formatted representation of the stored trace annotations.
func TraceInfoString(annotations map[string]string) string {
	if annotations == nil {
		return ""
	}
	return fmt.Sprintf("traceparent=%s tracestate=%s", annotations[TraceParentAnnotation], annotations[TraceStateAnnotation])
}

// AnnotateWithSpanContext injects span context values into a map of annotations.
func AnnotateWithSpanContext(annotations map[string]string, span trace.Span) map[string]string {
	if !span.SpanContext().IsValid() {
		return annotations
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	storeSpanContext(annotations, span.SpanContext())
	return annotations
}

// LoggerWithTraceID enriches the logger with the provided trace ID when available.
func LoggerWithTraceID(logger logr.Logger, traceID string) logr.Logger {
	if traceID == "" {
		return logger
	}
	return logger.WithValues("trace_id", traceID)
}

// TraceIDFromTraceParent extracts the trace ID from a W3C traceparent string.
func TraceIDFromTraceParent(traceParent string) string {
	if traceParent == "" {
		return ""
	}
	parts := strings.Split(traceParent, "-")
	if len(parts) < 2 {
		return ""
	}
	traceID := parts[1]
	if len(traceID) != 32 {
		return ""
	}
	return traceID
}
