package telemetry

import (
	"context"
	"errors"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const envEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
const envServiceName = "OTEL_SERVICE_NAME"
const envSDKDisabled = "OTEL_SDK_DISABLED"

func disabled() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(envSDKDisabled)))
	return raw == "true" || raw == "1" || raw == "yes"
}

type Tracing struct {
	provider   *sdktrace.TracerProvider
	propagator propagation.TextMapPropagator
	service    string
}

func InitTracing(ctx context.Context, serviceName string) (*Tracing, error) {
	prop := DefaultTextMapPropagator()
	otel.SetTextMapPropagator(prop)

	if disabled() || strings.TrimSpace(os.Getenv(envEndpoint)) == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return &Tracing{propagator: prop, service: serviceName}, nil
	}

	endpoint := strings.TrimSpace(os.Getenv(envEndpoint))
	opts := []otlptracegrpc.Option{}
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		opts = append(opts, otlptracegrpc.WithEndpointURL(endpoint))
	} else {
		opts = append(opts, otlptracegrpc.WithEndpoint(endpoint), otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	return &Tracing{provider: tp, propagator: prop, service: serviceName}, nil
}

func (t *Tracing) Shutdown(ctx context.Context) error {
	if t == nil || t.provider == nil {
		return nil
	}
	return errors.Join(t.provider.Shutdown(ctx))
}

func (t *Tracing) Tracer(name string) trace.Tracer {
	if t == nil {
		return otel.Tracer(name)
	}
	return t.provider.Tracer(name)
}

func (t *Tracing) Propagator() propagation.TextMapPropagator {
	if t == nil || t.propagator == nil {
		return propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	}
	return t.propagator
}

func (t *Tracing) ServiceName() string {
	if t == nil {
		return ""
	}
	return t.service
}
