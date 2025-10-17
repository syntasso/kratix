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
	Enabled  *bool             `json:"enabled,omitempty"`
	Endpoint string            `json:"endpoint,omitempty"`
	Protocol string            `json:"protocol,omitempty"`
	Insecure *bool             `json:"insecure,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// SetupTracerProvider configures and installs global OpenTelemetry providers for traces and metrics.
// If no OTLP endpoint is configured, telemetry is still generated locally so downstream resources can
// participate in traces and metrics, but data will not be exported.
func SetupTracerProvider(ctx context.Context, logger logr.Logger, serviceName string, cfg *Config) (func(context.Context) error, error) {
	if cfg == nil || (cfg.Enabled != nil && !*cfg.Enabled) {
		logging.Info(logger, "OpenTelemetry telemetry disabled via configuration")
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
			clientOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
			metricClientOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(endpoint)}
			insecure := false
			if cfg.Insecure != nil {
				insecure = *cfg.Insecure
			}
			if insecure {
				clientOpts = append(clientOpts, otlptracegrpc.WithInsecure())
				metricClientOpts = append(metricClientOpts, otlpmetricgrpc.WithInsecure())
			}
			if len(headers) > 0 {
				clientOpts = append(clientOpts, otlptracegrpc.WithHeaders(headers))
				metricClientOpts = append(metricClientOpts, otlpmetricgrpc.WithHeaders(headers))
			}
			traceExporter, err = otlptracegrpc.New(ctx, clientOpts...)
			if err != nil {
				logging.Error(logger, err, "creating OTLP trace exporter failed; falling back to local-only tracing")
				traceExporter = nil
			}
			metricExporter, err = otlpmetricgrpc.New(ctx, metricClientOpts...)
			if err != nil {
				logging.Error(logger, err, "creating OTLP metric exporter failed; metrics will not be exported")
				metricExporter = nil
			}
		default:
			err = fmt.Errorf("unsupported OTLP protocol %q", protocol)
			logging.Error(logger, err, "unable to create OTLP exporters")
		}
	}

	res, resErr := buildResource(ctx, serviceName)
	if resErr != nil {
		return nil, resErr
	}

	providerOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if traceExporter != nil {
		providerOpts = append(providerOpts, sdktrace.WithBatcher(traceExporter))
	}

	provider := sdktrace.NewTracerProvider(providerOpts...)

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	if traceExporter != nil {
		logging.Info(logger, "OpenTelemetry tracing enabled", "endpoint", endpoint)
	} else {
		logging.Info(logger, "OpenTelemetry tracing configured without exporter")
	}

	meterOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if metricExporter != nil {
		meterOpts = append(meterOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)))
	}
	meterProvider := sdkmetric.NewMeterProvider(meterOpts...)
	otel.SetMeterProvider(meterProvider)

	if metricExporter != nil {
		logging.Info(logger, "OpenTelemetry metrics enabled", "endpoint", endpoint)
	} else {
		logging.Info(logger, "OpenTelemetry metrics configured without exporter")
	}

	var shutdownFns []func(context.Context) error
	shutdownFns = append(shutdownFns, provider.Shutdown)
	shutdownFns = append(shutdownFns, meterProvider.Shutdown)

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
