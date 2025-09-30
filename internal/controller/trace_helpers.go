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
	ctx      context.Context
	span     trace.Span
	logger   logr.Logger
	object   client.Object
	original client.Object
	mutated  bool
}

func newReconcileTrace(
	ctx context.Context,
	tracerName string,
	spanName string,
	obj client.Object,
	logger logr.Logger,
) *reconcileTrace {
	tracer := otel.Tracer(tracerName)
	original := obj.DeepCopyObject().(client.Object)
	tracedCtx, span, mutated, traceErr := telemetry.StartSpanForObject(
		ctx, tracer, obj, spanName, trace.WithSpanKind(trace.SpanKindServer),
	)
	if traceErr != nil {
		tracedCtx, span = tracer.Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindServer))
		telemetry.RecordError(span, traceErr)
		logger.Error(traceErr, "failed to initialise trace context")
	}

	reconcileLogger := telemetry.LoggerWithTrace(logger, span)

	return &reconcileTrace{
		ctx:      tracedCtx,
		span:     span,
		logger:   reconcileLogger,
		object:   obj,
		original: original,
		mutated:  mutated,
	}
}

func (rt *reconcileTrace) Context() context.Context {
	return rt.ctx
}

func (rt *reconcileTrace) Logger() logr.Logger {
	return rt.logger
}

func (rt *reconcileTrace) Span() trace.Span {
	return rt.span
}

func (rt *reconcileTrace) AddAttributes(attrs ...attribute.KeyValue) {
	telemetry.SetCommonAttributes(rt.span, attrs...)
}

func (rt *reconcileTrace) PersistAnnotations(c client.Client) (bool, error) {
	if !rt.mutated {
		return false, nil
	}
	if err := c.Patch(rt.ctx, rt.object, client.MergeFrom(rt.original)); err != nil {
		telemetry.RecordError(rt.span, err)
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
