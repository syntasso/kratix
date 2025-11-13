package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/syntasso/kratix/internal/logging"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	otlpProtocolGRPC = "grpc"
)

// Config captures OpenTelemetry exporter configuration loaded from the Kratix ConfigMap.
type Config struct {
	Endpoint string            `json:"endpoint,omitempty"`
	Protocol string            `json:"protocol,omitempty"`
	Insecure *bool             `json:"insecure,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Traces   *SignalConfig     `json:"traces,omitempty"`
	Metrics  *SignalConfig     `json:"metrics,omitempty"`
}

// SignalConfig toggles a telemetry signal.
type SignalConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// SetupTracerProvider configures and installs global OpenTelemetry providers for traces and metrics.
// If no OTLP endpoint is configured, telemetry is still generated locally so downstream resources can
// participate in traces and metrics, but data will not be exported.
func SetupTracerProvider(ctx context.Context, logger logr.Logger, serviceName string, cfg *Config) (func(context.Context) error, error) {
	tracesEnabled := isTracingEnabled(cfg)
	metricsEnabled := isMetricsEnabled(cfg)

	if !tracesEnabled && !metricsEnabled {
		logging.Info(logger, "OpenTelemetry tracing and metrics disabled via configuration")
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		otel.SetTextMapPropagator(
			propagation.NewCompositeTextMapPropagator(
				propagation.TraceContext{},
				propagation.Baggage{},
			),
		)
		return func(context.Context) error { return nil }, nil
	}

	var (
		traceExporter  *otlptrace.Exporter
		metricExporter *otlpmetricgrpc.Exporter
		err            error
	)

	protocol := strings.ToLower(strings.TrimSpace(cfg.Protocol))
	if protocol == "" {
		protocol = otlpProtocolGRPC
	}

	var headers map[string]string
	if len(cfg.Headers) > 0 {
		headers = make(map[string]string, len(cfg.Headers))
		for k, v := range cfg.Headers {
			headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint != "" {
		switch protocol {
		case otlpProtocolGRPC:
			insecure := cfg.Insecure != nil && *cfg.Insecure

			if tracesEnabled {
				traceExporter = createTraceExporter(ctx, logger, endpoint, insecure, headers)
			}
			if metricsEnabled {
				metricExporter = createMetricExporter(ctx, logger, endpoint, insecure, headers)
			}
		default:
			err = fmt.Errorf("unsupported OTLP protocol %q", protocol)
			logging.Error(logger, err, "unable to create OTLP exporters")
		}
	}

	var (
		res         *resource.Resource
		resErr      error
		shutdownFns []func(context.Context) error
	)

	if tracesEnabled || metricsEnabled {
		res, resErr = buildResource(ctx, serviceName)
		if resErr != nil {
			return nil, resErr
		}
	}

	if tracesEnabled {
		providerOpts := []sdktrace.TracerProviderOption{}
		if res != nil {
			providerOpts = append(providerOpts, sdktrace.WithResource(res))
		}
		if traceExporter != nil {
			providerOpts = append(providerOpts, sdktrace.WithBatcher(traceExporter))
		}

		provider := sdktrace.NewTracerProvider(providerOpts...)
		otel.SetTracerProvider(provider)
		shutdownFns = append(shutdownFns, provider.Shutdown)

		if traceExporter != nil {
			logging.Info(logger, "OpenTelemetry tracing enabled", "endpoint", endpoint)
		} else {
			logging.Info(logger, "OpenTelemetry tracing configured without exporter")
		}
	} else {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		logging.Info(logger, "OpenTelemetry tracing disabled via configuration")
	}

	if metricsEnabled {
		meterOpts := []sdkmetric.Option{}
		if res != nil {
			meterOpts = append(meterOpts, sdkmetric.WithResource(res))
		}
		if metricExporter != nil {
			meterOpts = append(meterOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)))
		}

		meterProvider := sdkmetric.NewMeterProvider(meterOpts...)
		otel.SetMeterProvider(meterProvider)
		shutdownFns = append(shutdownFns, meterProvider.Shutdown)

		if metricExporter != nil {
			logging.Info(logger, "OpenTelemetry metrics enabled", "endpoint", endpoint)
		} else {
			logging.Info(logger, "OpenTelemetry metrics configured without exporter")
		}
	} else {
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		logging.Info(logger, "OpenTelemetry metrics disabled via configuration")
	}

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	if len(shutdownFns) == 0 {
		return func(context.Context) error { return nil }, nil
	}
	return func(ctx context.Context) error {
		var errs []error
		for _, fn := range shutdownFns {
			if err := fn(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}, nil
}

func buildResource(ctx context.Context, serviceName string) (*resource.Resource, error) {
	base, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OpenTelemetry resource: %w", err)
	}

	if serviceName = strings.TrimSpace(serviceName); serviceName == "" {
		serviceName = "kratix"
	}

	if hasServiceName(base) {
		return base, nil
	}

	schemaURL := base.SchemaURL()
	if schemaURL == "" {
		schemaURL = semconv.SchemaURL
	}

	serviceRes := resource.NewWithAttributes(schemaURL, semconv.ServiceNameKey.String(serviceName))
	merged, err := resource.Merge(base, serviceRes)
	if err != nil {
		return nil, fmt.Errorf("merging OpenTelemetry resources: %w", err)
	}
	return merged, nil
}

func hasServiceName(res *resource.Resource) bool {
	if res == nil {
		return false
	}
	iter := res.Iter()
	for iter.Next() {
		attr := iter.Attribute()
		if attr.Key == semconv.ServiceNameKey {
			return true
		}
	}
	return false
}

func isTracingEnabled(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	if cfg.Traces != nil && cfg.Traces.Enabled != nil {
		return *cfg.Traces.Enabled
	}
	return true
}

func isMetricsEnabled(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	if cfg.Metrics != nil && cfg.Metrics.Enabled != nil {
		return *cfg.Metrics.Enabled
	}
	return true
}

func createTraceExporter(ctx context.Context, logger logr.Logger, endpoint string, insecure bool, headers map[string]string) *otlptrace.Exporter {
	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(endpoint),
	}

	if insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	if len(headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(headers))
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		logging.Error(logger, err, "creating OTLP trace exporter failed; falling back to local-only tracing")
		return nil
	}
	return exporter
}

func createMetricExporter(ctx context.Context, logger logr.Logger, endpoint string, insecure bool, headers map[string]string) *otlpmetricgrpc.Exporter {
	opts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(endpoint),
	}

	if insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	if len(headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(headers))
	}

	exporter, err := otlpmetricgrpc.New(ctx, opts...)
	if err != nil {
		logging.Error(logger, err, "creating OTLP metric exporter failed; metrics will not be exported")
		return nil
	}
	return exporter
}
