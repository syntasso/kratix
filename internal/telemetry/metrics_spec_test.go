package telemetry_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/syntasso/kratix/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var _ = Describe("Metrics helpers", func() {
	var (
		ctx      context.Context
		reader   *sdkmetric.ManualReader
		restore  func()
		settings struct {
			promise     string
			resource    string
			destination string
			pipeline    string
			namespace   string
		}
	)

	BeforeEach(func() {
		ctx = context.Background()
		telemetry.ResetWorkPlacementMetricsForTest()

		reader = sdkmetric.NewManualReader()
		original := otel.GetMeterProvider()
		otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
		restore = func() {
			otel.SetMeterProvider(original)
		}

		settings.promise = "database"
		settings.resource = "tenant-a"
		settings.destination = "east-cluster"
		settings.pipeline = "configure"
		settings.namespace = "default"
	})

	AfterEach(func() {
		if restore != nil {
			restore()
		}
		telemetry.ResetWorkPlacementMetricsForTest()
	})

	It("builds attribute sets with only non-empty values", func() {
		attrs := telemetry.WorkPlacementWriteAttributes(settings.promise, "", settings.destination, settings.pipeline)
		Expect(attrs).To(ConsistOf(
			attribute.String("promise", settings.promise),
			attribute.String("destination", settings.destination),
			attribute.String("pipeline", settings.pipeline),
		))
	})

	It("records work placement write outcomes with result labels", func() {
		metricAttrs := telemetry.WorkPlacementWriteAttributes(settings.promise, settings.resource, settings.destination, settings.pipeline)

		telemetry.RecordWorkPlacementWrite(ctx, telemetry.WorkPlacementWriteResultSuccess, metricAttrs...)
		telemetry.RecordWorkPlacementWrite(ctx, telemetry.WorkPlacementWriteResultFailure, metricAttrs...)

		counts := collectMetricDataPoints(ctx, reader, telemetry.WorkPlacementWritesMetric)
		Expect(counts).To(HaveKeyWithValue(telemetry.WorkPlacementWriteResultSuccess, BeAssignableToTypeOf(metricdata.DataPoint[int64]{})))
		Expect(counts).To(HaveKeyWithValue(telemetry.WorkPlacementWriteResultFailure, BeAssignableToTypeOf(metricdata.DataPoint[int64]{})))

		success := counts[telemetry.WorkPlacementWriteResultSuccess]
		Expect(success.Value).To(Equal(int64(1)))
		Expect(attributeValue(success.Attributes, "promise")).To(Equal(settings.promise))
		Expect(attributeValue(success.Attributes, "resource")).To(Equal(settings.resource))
		Expect(attributeValue(success.Attributes, "destination")).To(Equal(settings.destination))
		Expect(attributeValue(success.Attributes, "pipeline")).To(Equal(settings.pipeline))

		failure := counts[telemetry.WorkPlacementWriteResultFailure]
		Expect(failure.Value).To(Equal(int64(1)))
		Expect(attributeValue(failure.Attributes, "promise")).To(Equal(settings.promise))
		Expect(attributeValue(failure.Attributes, "resource")).To(Equal(settings.resource))
		Expect(attributeValue(failure.Attributes, "destination")).To(Equal(settings.destination))
		Expect(attributeValue(failure.Attributes, "pipeline")).To(Equal(settings.pipeline))
	})

	It("records work placement outcomes with namespace and destination attributes", func() {
		attrs := telemetry.WorkPlacementOutcomeAttributes(settings.promise, settings.resource, settings.namespace, settings.destination)
		Expect(attrs).To(ConsistOf(
			attribute.String("promise", settings.promise),
			attribute.String("resource", settings.resource),
			attribute.String("namespace", settings.namespace),
			attribute.String("destination", settings.destination),
		))

		outcomeAttrs := telemetry.WorkPlacementOutcomeAttributes(settings.promise, settings.resource, settings.namespace, settings.destination)
		telemetry.RecordWorkPlacementOutcome(ctx, telemetry.WorkPlacementOutcomeScheduled, outcomeAttrs...)

		counts := collectMetricDataPoints(ctx, reader, telemetry.WorkPlacementOutcomesMetric)
		Expect(counts).To(HaveKey(telemetry.WorkPlacementOutcomeScheduled))

		point := counts[telemetry.WorkPlacementOutcomeScheduled]
		Expect(point.Value).To(Equal(int64(1)))
		Expect(attributeValue(point.Attributes, "promise")).To(Equal(settings.promise))
		Expect(attributeValue(point.Attributes, "resource")).To(Equal(settings.resource))
		Expect(attributeValue(point.Attributes, "namespace")).To(Equal(settings.namespace))
		Expect(attributeValue(point.Attributes, "destination")).To(Equal(settings.destination))
	})
})

func collectMetricDataPoints(ctx context.Context, reader *sdkmetric.ManualReader, metricName string) map[string]metricdata.DataPoint[int64] {
	var rm metricdata.ResourceMetrics
	Expect(reader.Collect(ctx, &rm)).To(Succeed())

	collected := map[string]metricdata.DataPoint[int64]{}
	for _, scopeMetrics := range rm.ScopeMetrics {
		for _, metric := range scopeMetrics.Metrics {
			if metric.Name != metricName {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			Expect(ok).To(BeTrue(), "expected an int64 Sum aggregation")

			for _, dp := range sum.DataPoints {
				val, ok := dp.Attributes.Value(attribute.Key("result"))
				Expect(ok).To(BeTrue(), "expected result attribute")
				collected[val.AsString()] = dp
			}
		}
	}

	return collected
}

func attributeValue(set attribute.Set, key string) string {
	if val, ok := set.Value(attribute.Key(key)); ok {
		return val.AsString()
	}
	return ""
}
