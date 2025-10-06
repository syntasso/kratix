package telemetry_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Tracing helpers", func() {
	var (
		tracer   trace.Tracer
		provider *sdktrace.TracerProvider
	)

	BeforeEach(func() {
		provider = sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
		tracer = provider.Tracer("test")
		DeferCleanup(func() {
			Expect(provider.Shutdown(context.Background())).To(Succeed())
		})
	})

	Describe("StartSpanForObject", func() {
		It("creates annotations and reuses an existing span context", func() {
			promise := &v1alpha1.Promise{ObjectMeta: metav1.ObjectMeta{Name: "test", Generation: 1}}

			ctx, span, mutated, err := telemetry.StartSpanForObject(context.Background(), tracer, promise, "promise-flow")
			Expect(err).NotTo(HaveOccurred())
			Expect(mutated).To(BeTrue())
			annotations := promise.GetAnnotations()
			Expect(annotations).NotTo(BeNil())
			Expect(annotations[telemetry.TraceParentAnnotation]).NotTo(BeEmpty())
			Expect(annotations[telemetry.TraceTimestampAnnotation]).NotTo(BeEmpty())
			Expect(annotations[telemetry.TraceGenerationAnnotation]).To(Equal("1"))
			span.End()

			ctx2, span2, mutatedSecond, err := telemetry.StartSpanForObject(ctx, tracer, promise, "promise-flow")
			Expect(err).NotTo(HaveOccurred())
			Expect(mutatedSecond).To(BeFalse())
			Expect(span2).NotTo(BeNil())
			Expect(trace.SpanContextFromContext(ctx2).IsValid()).To(BeTrue())
			Expect(promise.GetAnnotations()[telemetry.TraceParentAnnotation]).To(Equal(annotations[telemetry.TraceParentAnnotation]))
			span2.End()
		})

		It("starts a new span when the stored trace expires", func() {
			telemetry.SetTraceMaxAge(time.Second)
			telemetry.SetTimeNow(func() time.Time { return time.Unix(0, 0) })
			DeferCleanup(func() {
				telemetry.SetTraceMaxAge(24 * time.Hour)
				telemetry.ResetTimeNow()
			})

			work := &v1alpha1.Work{ObjectMeta: metav1.ObjectMeta{Name: "work", Namespace: "default", Generation: 1}}

			_, span, mutated, err := telemetry.StartSpanForObject(context.Background(), tracer, work, "work-flow")
			Expect(err).NotTo(HaveOccurred())
			Expect(mutated).To(BeTrue())
			span.End()

			telemetry.SetTimeNow(func() time.Time { return time.Unix(5, 0) })

			_, span2, mutatedSecond, err := telemetry.StartSpanForObject(context.Background(), tracer, work, "work-flow")
			Expect(err).NotTo(HaveOccurred())
			Expect(mutatedSecond).To(BeTrue())
			span2.End()
		})
	})

	Describe("ContextWithTraceparent", func() {
		It("extracts a valid span context", func() {
			promise := &v1alpha1.Promise{ObjectMeta: metav1.ObjectMeta{Name: "trace", Generation: 1}}
			_, span, mutated, err := telemetry.StartSpanForObject(context.Background(), tracer, promise, "promise-flow")
			Expect(err).NotTo(HaveOccurred())
			Expect(mutated).To(BeTrue())
			span.End()

			parent := promise.GetAnnotations()[telemetry.TraceParentAnnotation]
			state := promise.GetAnnotations()[telemetry.TraceStateAnnotation]

			ctx, ok := telemetry.ContextWithTraceparent(context.Background(), parent, state)
			Expect(ok).To(BeTrue())
			Expect(trace.SpanContextFromContext(ctx).IsValid()).To(BeTrue())
		})
	})

	Describe("AnnotateWithSpanContext", func() {
		It("injects trace metadata into annotations", func() {
			ctx, span := tracer.Start(context.Background(), "test-span")
			DeferCleanup(func() { span.End() })

			annotations := telemetry.AnnotateWithSpanContext(nil, span)
			Expect(annotations[telemetry.TraceParentAnnotation]).NotTo(BeEmpty())
			Expect(annotations[telemetry.TraceStateAnnotation]).To(BeEmpty())

			span.End()
			Expect(trace.SpanContextFromContext(ctx).IsValid()).To(BeTrue())
		})
	})

	Describe("StartSpanForObject with noop provider", func() {
		It("leaves annotations untouched when the tracer is noop", func() {
			noopTracer := trace.NewNoopTracerProvider().Tracer("noop")

			promise := &v1alpha1.Promise{ObjectMeta: metav1.ObjectMeta{Name: "noop"}}
			_, span, mutated, err := telemetry.StartSpanForObject(context.Background(), noopTracer, promise, "promise-flow")
			Expect(err).NotTo(HaveOccurred())
			Expect(mutated).To(BeFalse())
			Expect(promise.GetAnnotations()).To(BeNil())
			span.End()
		})
	})
})
