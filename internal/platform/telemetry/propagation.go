package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

type mapCarrier map[string]string

func (m mapCarrier) Get(key string) string { return m[key] }
func (m mapCarrier) Set(key, value string) { m[key] = value }
func (m mapCarrier) Keys() []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func InjectTrace(ctx context.Context, headers map[string]string) {
	if headers == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, mapCarrier(headers))
}

func ExtractTrace(ctx context.Context, headers map[string]string) context.Context {
	if headers == nil {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, mapCarrier(headers))
}

func DefaultTextMapPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}
