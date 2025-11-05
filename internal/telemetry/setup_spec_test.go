package telemetry_test

import (
	"context"
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

var _ = Describe("SetupTracerProvider configuration", func() {
	var (
		originalTracer trace.TracerProvider
		originalMeter  metric.MeterProvider
		logger         logr.Logger
		noopTracerType = reflect.TypeOf(tracenoop.NewTracerProvider())
		noopMeterType  = reflect.TypeOf(metricnoop.NewMeterProvider())
	)

	BeforeEach(func() {
		logger = logr.Discard()
		originalTracer = otel.GetTracerProvider()
		originalMeter = otel.GetMeterProvider()
	})

	AfterEach(func() {
		otel.SetTracerProvider(originalTracer)
		otel.SetMeterProvider(originalMeter)
	})

	It("installs noop providers when both signals are disabled", func() {
		shutdown, err := telemetry.SetupTracerProvider(context.Background(), logger, "kratix-test", &telemetry.Config{
			Traces:  &telemetry.SignalConfig{Enabled: ptr(false)},
			Metrics: &telemetry.SignalConfig{Enabled: ptr(false)},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(reflect.TypeOf(otel.GetTracerProvider())).To(Equal(noopTracerType))
		Expect(reflect.TypeOf(otel.GetMeterProvider())).To(Equal(noopMeterType))
		Expect(shutdown(context.Background())).To(Succeed())
	})

	It("enables tracing without metrics when configured", func() {
		shutdown, err := telemetry.SetupTracerProvider(context.Background(), logger, "kratix-test", &telemetry.Config{
			Traces:  &telemetry.SignalConfig{Enabled: ptr(true)},
			Metrics: &telemetry.SignalConfig{Enabled: ptr(false)},
		})
		Expect(err).NotTo(HaveOccurred())

		_, isSDKTracer := otel.GetTracerProvider().(*sdktrace.TracerProvider)
		Expect(isSDKTracer).To(BeTrue())
		Expect(reflect.TypeOf(otel.GetMeterProvider())).To(Equal(noopMeterType))
		Expect(shutdown(context.Background())).To(Succeed())
	})

	It("enables metrics without tracing when configured", func() {
		shutdown, err := telemetry.SetupTracerProvider(context.Background(), logger, "kratix-test", &telemetry.Config{
			Traces:  &telemetry.SignalConfig{Enabled: ptr(false)},
			Metrics: &telemetry.SignalConfig{Enabled: ptr(true)},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(reflect.TypeOf(otel.GetTracerProvider())).To(Equal(noopTracerType))
		_, isSDKMeter := otel.GetMeterProvider().(*sdkmetric.MeterProvider)
		Expect(isSDKMeter).To(BeTrue())
		Expect(shutdown(context.Background())).To(Succeed())
	})

	It("defaults to enabling both signals when not explicitly disabled", func() {
		shutdown, err := telemetry.SetupTracerProvider(context.Background(), logger, "kratix-test", &telemetry.Config{})
		Expect(err).NotTo(HaveOccurred())

		_, isSDKTracer := otel.GetTracerProvider().(*sdktrace.TracerProvider)
		Expect(isSDKTracer).To(BeTrue())
		_, isSDKMeter := otel.GetMeterProvider().(*sdkmetric.MeterProvider)
		Expect(isSDKMeter).To(BeTrue())
		Expect(shutdown(context.Background())).To(Succeed())
	})
})

func ptr(value bool) *bool {
	return &value
}
