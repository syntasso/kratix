package controller

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// reconcileTrace centralises per-controller span lifecycle management so each
// reconciler stays focused on business logic instead of trace boilerplate.
type reconcileTrace struct {
	ctx          context.Context
	tracer       trace.Tracer
	span         trace.Span
	spanName     string
	spanOpts     []trace.SpanStartOption
	baseLogger   logr.Logger
	logger       logr.Logger
	object       client.Object
	original     client.Object
	mutated      bool
	traceParent  string
	traceState   string
	pendingAttrs []attribute.KeyValue
}

func newReconcileTrace(
	ctx context.Context,
	tracerName string,
	spanName string,
	obj client.Object,
	logger logr.Logger,
) *reconcileTrace {
	tracer := otel.Tracer(tracerName)
	spanOpts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindServer)}
	original := obj.DeepCopyObject().(client.Object)
	tracedCtx, span, mutated, traceErr := telemetry.StartSpanForObject(
		ctx, tracer, obj, spanName, spanOpts...,
	)
	rt := &reconcileTrace{
		ctx:        tracedCtx,
		tracer:     tracer,
		span:       span,
		spanName:   spanName,
		spanOpts:   spanOpts,
		baseLogger: logger,
		logger:     logger,
		object:     obj,
		original:   original,
		mutated:    mutated,
	}
	if traceErr != nil {
		rt.ctx, rt.span = tracer.Start(ctx, spanName, spanOpts...)
		telemetry.RecordError(rt.span, traceErr)
		logger.Error(traceErr, "failed to initialise trace context")
	}

	annotations := obj.GetAnnotations()
	if annotations != nil {
		rt.traceParent = annotations[telemetry.TraceParentAnnotation]
		rt.traceState = annotations[telemetry.TraceStateAnnotation]
	}

	if rt.span != nil && rt.span.SpanContext().IsValid() {
		rt.logger = telemetry.LoggerWithTrace(logger, rt.span)
	} else if traceID := telemetry.TraceIDFromTraceParent(rt.traceParent); traceID != "" {
		rt.logger = logger.WithValues("trace_id", traceID)
	}

	return rt
}

func (rt *reconcileTrace) Context() context.Context {
	return rt.ctx
}

func (rt *reconcileTrace) Logger() logr.Logger {
	return rt.logger
}

func (rt *reconcileTrace) AddAttributes(attrs ...attribute.KeyValue) {
	if len(attrs) == 0 {
		return
	}
	rt.pendingAttrs = append(rt.pendingAttrs, attrs...)
	if rt.span != nil && rt.span.SpanContext().IsValid() {
		telemetry.SetCommonAttributes(rt.span, attrs...)
	}
}

func (rt *reconcileTrace) PersistAnnotations(c client.Client) (bool, error) {
	if !rt.mutated {
		return false, nil
	}
	if err := c.Patch(rt.ctx, rt.object, client.MergeFrom(rt.original)); err != nil {
		if rt.span != nil {
			telemetry.RecordError(rt.span, err)
		}
		return errors.IsConflict(err), err
	}
	rt.mutated = false
	return false, nil
}

func (rt *reconcileTrace) End(err error) {
	if rt.span == nil {
		return
	}
	if err != nil {
		telemetry.RecordError(rt.span, err)
	}
	rt.span.End()
}

func (rt *reconcileTrace) EnsureSpan() trace.Span {
	if rt.span != nil && rt.span.SpanContext().IsValid() {
		return rt.span
	}
	if rt.tracer == nil {
		return nil
	}
	ctx, span := rt.tracer.Start(rt.ctx, rt.spanName, rt.spanOpts...)
	if !span.SpanContext().IsValid() {
		rt.ctx = ctx
		rt.span = span
		return nil
	}
	rt.ctx = ctx
	rt.span = span
	if len(rt.pendingAttrs) > 0 {
		telemetry.SetCommonAttributes(rt.span, rt.pendingAttrs...)
	}
	rt.logger = telemetry.LoggerWithTrace(rt.baseLogger, rt.span)
	annotations := telemetry.AnnotateWithSpanContext(rt.object.GetAnnotations(), rt.span)
	rt.object.SetAnnotations(annotations)
	rt.traceParent = annotations[telemetry.TraceParentAnnotation]
	rt.traceState = annotations[telemetry.TraceStateAnnotation]
	rt.mutated = true
	return rt.span
}

func (rt *reconcileTrace) HasTrace() bool {
	return rt.traceParent != ""
}

func (rt *reconcileTrace) InjectTrace(annotations map[string]string) map[string]string {
	if !rt.HasTrace() {
		return annotations
	}
	if rt.span != nil && rt.span.SpanContext().IsValid() {
		return telemetry.AnnotateWithSpanContext(annotations, rt.span)
	}
	return telemetry.ApplyTraceAnnotations(annotations, rt.traceParent, rt.traceState)
}

func setupReconcileTrace(
	ctx context.Context,
	tracerName string,
	spanName string,
	obj client.Object,
	baseLogger logr.Logger,
) (context.Context, logr.Logger, *reconcileTrace) {
	traceCtx := newReconcileTrace(ctx, tracerName, spanName, obj, baseLogger)
	return traceCtx.Context(), traceCtx.Logger(), traceCtx
}

func finishReconcileTrace(traceCtx *reconcileTrace, retErr *error) func() {
	return func() {
		if traceCtx == nil {
			return
		}
		var err error
		if retErr != nil {
			err = *retErr
		}
		traceCtx.End(err)
	}
}

func persistReconcileTrace(traceCtx *reconcileTrace, c client.Client, logger logr.Logger,
) error {
	conflict, err := traceCtx.PersistAnnotations(c)
	if err != nil {
		if conflict {
			logger.Info("conflict persisting trace annotations, requeueing")
			return err
		}
		return err
	}
	return nil
}
