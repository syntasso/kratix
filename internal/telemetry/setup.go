package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	otlpProtocolGRPC         = "grpc"
	otlpProtocolHTTPProtobuf = "http/protobuf"
)

// SetupTracerProvider configures and installs a global OpenTelemetry tracer provider.
// If no OTLP endpoint is configured, tracing remains disabled and a no-op shutdown
// function is returned.
func SetupTracerProvider(ctx context.Context, logger logr.Logger, serviceName string) (func(context.Context) error, error) {
	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if endpoint == "" {
		logger.Info("OTEL_EXPORTER_OTLP_ENDPOINT not set, tracing disabled")
		return func(context.Context) error { return nil }, nil
	}

	protocol := strings.TrimSpace(strings.ToLower(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")))
	if protocol == "" {
		protocol = otlpProtocolGRPC
	}

	var (
		exporter *otlptrace.Exporter
		err      error
	)

	switch protocol {
	case otlpProtocolGRPC:
		clientOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
		if strings.EqualFold(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE"), "true") {
			clientOpts = append(clientOpts, otlptracegrpc.WithInsecure())
		}
		if headers := parseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")); len(headers) > 0 {
			clientOpts = append(clientOpts, otlptracegrpc.WithHeaders(headers))
		}
		exporter, err = otlptracegrpc.New(ctx, clientOpts...)
	case otlpProtocolHTTPProtobuf:
		err = fmt.Errorf("unsupported OTLP protocol %q", protocol)
	default:
		err = fmt.Errorf("unknown OTLP protocol %q", protocol)
	}

	if err != nil {
		return nil, fmt.Errorf("creating OTLP exporter: %w", err)
	}

	res, err := buildResource(ctx, serviceName)
	if err != nil {
		return nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	logger.Info("OpenTelemetry tracing enabled", "endpoint", endpoint, "protocol", protocol)

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

func parseHeaders(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	headers := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		headers[key] = value
	}
	return headers
}
