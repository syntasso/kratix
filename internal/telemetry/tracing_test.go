package telemetry_test

import (
	"context"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newTestTracer(t *testing.T) trace.Tracer {
	t.Helper()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})
	return provider.Tracer("test")
}

func TestStartSpanForObjectCreatesAnnotations(t *testing.T) {
	g := gomega.NewWithT(t)
	tracer := newTestTracer(t)

	promise := &v1alpha1.Promise{ObjectMeta: metav1.ObjectMeta{Name: "test", Generation: 1}}

	ctx, span, mutated, err := telemetry.StartSpanForObject(context.Background(), tracer, promise, "promise-flow")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(mutated).To(gomega.BeTrue())
	annotations := promise.GetAnnotations()
	g.Expect(annotations).NotTo(gomega.BeNil())
	g.Expect(annotations[telemetry.TraceParentAnnotation]).NotTo(gomega.BeEmpty())
	g.Expect(annotations[telemetry.TraceTimestampAnnotation]).NotTo(gomega.BeEmpty())
	g.Expect(annotations[telemetry.TraceGenerationAnnotation]).To(gomega.Equal("1"))
	span.End()

	// Reuse the existing trace.
	_, span2, mutatedSecond, err := telemetry.StartSpanForObject(ctx, tracer, promise, "promise-flow")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(mutatedSecond).To(gomega.BeFalse())
	g.Expect(promise.GetAnnotations()[telemetry.TraceParentAnnotation]).To(gomega.Equal(annotations[telemetry.TraceParentAnnotation]))
	span2.End()
}

func TestStartSpanForObjectDetectsExpiredTrace(t *testing.T) {
	g := gomega.NewWithT(t)
	tracer := newTestTracer(t)
	telemetry.SetTraceMaxAge(time.Second)
	telemetry.SetTimeNow(func() time.Time { return time.Unix(0, 0) })
	t.Cleanup(func() {
		telemetry.SetTraceMaxAge(24 * time.Hour)
		telemetry.ResetTimeNow()
	})

	work := &v1alpha1.Work{ObjectMeta: metav1.ObjectMeta{Name: "work", Namespace: "default", Generation: 1}}

	_, span, mutated, err := telemetry.StartSpanForObject(context.Background(), tracer, work, "work-flow")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(mutated).To(gomega.BeTrue())
	span.End()

	telemetry.SetTimeNow(func() time.Time { return time.Unix(5, 0) })

	_, span2, mutatedSecond, err := telemetry.StartSpanForObject(context.Background(), tracer, work, "work-flow")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(mutatedSecond).To(gomega.BeTrue())
	span2.End()
}

func TestContextWithTraceparent(t *testing.T) {
	g := gomega.NewWithT(t)
	tracer := newTestTracer(t)

	promise := &v1alpha1.Promise{ObjectMeta: metav1.ObjectMeta{Name: "trace", Generation: 1}}
	_, span, mutated, err := telemetry.StartSpanForObject(context.Background(), tracer, promise, "promise-flow")
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(mutated).To(gomega.BeTrue())
	span.End()

	parent := promise.GetAnnotations()[telemetry.TraceParentAnnotation]
	state := promise.GetAnnotations()[telemetry.TraceStateAnnotation]

	ctx, ok := telemetry.ContextWithTraceparent(context.Background(), parent, state)
	g.Expect(ok).To(gomega.BeTrue())
	g.Expect(trace.SpanContextFromContext(ctx).IsValid()).To(gomega.BeTrue())
}
