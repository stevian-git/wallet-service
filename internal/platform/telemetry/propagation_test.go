package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

func TestInjectAndExtractRoundTrip(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(DefaultTextMapPropagator())
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })

	provider := testProvider()
	defer provider.Shutdown(context.Background())
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(nil) })

	tctx, span := provider.Tracer("test").Start(context.Background(), "parent")
	defer span.End()

	headers := map[string]string{}
	InjectTrace(tctx, headers)

	if _, ok := headers["traceparent"]; !ok {
		t.Fatal("expected traceparent header after inject")
	}

	got := ExtractTrace(context.Background(), headers)
	extractedSC := trace.SpanContextFromContext(got)
	if !extractedSC.HasTraceID() || !extractedSC.HasSpanID() {
		t.Fatal("extracted span context missing trace/span id")
	}
	if extractedSC.TraceID() != span.SpanContext().TraceID() {
		t.Fatalf("trace id mismatch: got %s want %s", extractedSC.TraceID(), span.SpanContext().TraceID())
	}
}

func TestExtractNilHeaders(t *testing.T) {
	ctx := ExtractTrace(context.Background(), nil)
	if ctx == nil {
		t.Fatal("expected non-nil context for nil headers")
	}
}

func TestInjectNilHeaders(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	InjectTrace(context.Background(), nil)
}
