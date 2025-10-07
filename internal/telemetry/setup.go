package telemetry

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace/noop"
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

// SetupTracerProvider configures and installs a global OpenTelemetry tracer provider.
// If no OTLP endpoint is configured, tracing metadata is still generated locally so
// downstream resources can participate in traces, but spans will not be exported.
func SetupTracerProvider(ctx context.Context, logger logr.Logger, serviceName string, cfg *Config) (func(context.Context) error, error) {
	if cfg == nil || (cfg.Enabled != nil && !*cfg.Enabled) {
		logger.Info("OpenTelemetry tracing disabled via configuration")
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(
			propagation.NewCompositeTextMapPropagator(
				propagation.TraceContext{},
				propagation.Baggage{},
			),
		)
		return func(context.Context) error { return nil }, nil
	}

	var (
		exporter *otlptrace.Exporter
		err      error
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
			if *cfg.Insecure {
				clientOpts = append(clientOpts, otlptracegrpc.WithInsecure())
			}
			if len(headers) > 0 {
				clientOpts = append(clientOpts, otlptracegrpc.WithHeaders(headers))
			}
			exporter, err = otlptracegrpc.New(ctx, clientOpts...)
		default:
			err = fmt.Errorf("unsupported OTLP protocol %q", protocol)
		}

		if err != nil {
			logger.Error(err, "creating OTLP trace exporter failed; falling back to local-only tracing")
			exporter = nil
		}
	}

	res, resErr := buildResource(ctx, serviceName)
	if resErr != nil {
		return nil, resErr
	}

	providerOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if exporter != nil {
		providerOpts = append(providerOpts, sdktrace.WithBatcher(exporter))
	}

	provider := sdktrace.NewTracerProvider(providerOpts...)

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	if exporter != nil {
		logger.Info("OpenTelemetry tracing enabled", "endpoint", endpoint)
	} else {
		logger.Info("OpenTelemetry tracing configured without exporter")
	}

	return provider.Shutdown, nil
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

	serviceRes := resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(serviceName))
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
