package outbox

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/stevians/wallet-service/internal/platform/telemetry"
	"github.com/stevians/wallet-service/internal/testutil"
)

func TestKafkaPublisherPublishesKeyHeadersAndPayload(t *testing.T) {
	broker := testutil.NewRedpanda(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	topic := "ledger.events.test." + uuid.NewString()
	publisher, err := NewKafkaPublisher([]string{broker}, topic)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	defer publisher.Close()

	aggregateID := uuid.New()
	eventID := uuid.NewString()
	event := Event{
		ID:            1,
		AggregateType: "transfer",
		AggregateID:   aggregateID,
		EventType:     "transfer.posted",
		Payload:       []byte(`{"transfer_id":"abc"}`),
		Headers: map[string]string{
			"event_id":       eventID,
			"schema_version": "1",
		},
	}
	if err := publisher.Publish(ctx, event); err != nil {
		t.Fatalf("publish: %v", err)
	}

	record := consumeOne(t, ctx, broker, topic)

	if string(record.Key) != aggregateID.String() {
		t.Fatalf("record key = %q, want %q", string(record.Key), aggregateID.String())
	}
	if got := headerValue(record.Headers, "event_id"); got != eventID {
		t.Fatalf("event_id header = %q, want %q", got, eventID)
	}
	if got := headerValue(record.Headers, "schema_version"); got != "1" {
		t.Fatalf("schema_version header = %q, want 1", got)
	}
	if got := string(record.Value); got != string(event.Payload) {
		t.Fatalf("record value = %q, want %q", got, string(event.Payload))
	}
}

func headerValue(headers []kgo.RecordHeader, key string) string {
	for _, header := range headers {
		if header.Key == key {
			return fmt.Sprint(string(header.Value))
		}
	}
	return ""
}

// consumeOne reads the first record of topic.
func consumeOne(t *testing.T, ctx context.Context, broker, topic string) *kgo.Record {
	t.Helper()
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(broker),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("new consumer: %v", err)
	}
	defer consumer.Close()

	var record *kgo.Record
	for record == nil {
		fetches := consumer.PollFetches(ctx)
		if ctx.Err() != nil {
			t.Fatalf("timed out waiting for record: %v", ctx.Err())
		}
		for _, fetchErr := range fetches.Errors() {
			if fetchErr.Err != context.DeadlineExceeded {
				t.Fatalf("fetch error: %v", fetchErr.Err)
			}
		}
		fetches.EachRecord(func(candidate *kgo.Record) {
			record = candidate
		})
	}
	return record
}

// TestKafkaPublisherReplacesStoredTraceparent: the outbox row carries the
// traceparent of the HTTP request; the Kafka record must carry the
// kafka.publish span instead, exactly once, without mutating the event.
func TestKafkaPublisherReplacesStoredTraceparent(t *testing.T) {
	broker := testutil.NewRedpanda(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prevProvider := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(telemetry.DefaultTextMapPropagator())
	t.Cleanup(func() {
		otel.SetTracerProvider(prevProvider)
		otel.SetTextMapPropagator(prevPropagator)
		_ = tp.Shutdown(context.Background())
	})

	topic := "ledger.events.test." + uuid.NewString()
	publisher, err := NewKafkaPublisher([]string{broker}, topic)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	defer publisher.Close()

	parentCtx, parent := tp.Tracer("test").Start(ctx, "relay-batch")
	defer parent.End()

	const stale = "00-11111111111111111111111111111111-2222222222222222-01"
	headers := map[string]string{"event_id": uuid.NewString(), "traceparent": stale}
	event := Event{
		ID: 1, AggregateType: "transfer", AggregateID: uuid.New(), EventType: "transfer.posted",
		Payload: []byte(`{}`), Headers: headers,
	}
	if err := publisher.Publish(parentCtx, event); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if headers["traceparent"] != stale {
		t.Fatalf("Publish mutated the event headers: traceparent = %q", headers["traceparent"])
	}

	record := consumeOne(t, ctx, broker, topic)
	var traceparents []string
	for _, h := range record.Headers {
		if h.Key == "traceparent" {
			traceparents = append(traceparents, string(h.Value))
		}
	}
	if len(traceparents) != 1 {
		t.Fatalf("traceparent headers = %q, want exactly one", traceparents)
	}
	got := traceparents[0]
	if got == stale {
		t.Fatalf("record kept the stored traceparent %q", got)
	}
	wantTraceID := parent.SpanContext().TraceID().String()
	if !strings.Contains(got, wantTraceID) {
		t.Fatalf("traceparent = %q, want trace id %s", got, wantTraceID)
	}
}
