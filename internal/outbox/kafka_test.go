package outbox

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

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
