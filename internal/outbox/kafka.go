package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/stevians/wallet-service/internal/platform/telemetry"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type KafkaPublisher struct {
	client *kgo.Client
	topic  string
	tracer trace.Tracer
}

func NewKafkaPublisher(brokers []string, topic string) (*KafkaPublisher, error) {
	if len(brokers) == 0 {
		return nil, fmt.Errorf("kafka brokers are required")
	}
	if topic == "" {
		return nil, fmt.Errorf("kafka topic is required")
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.AllowAutoTopicCreation(),
		kgo.ProducerLinger(5*time.Millisecond),
	)
	if err != nil {
		return nil, fmt.Errorf("create kafka producer: %w", err)
	}
	return &KafkaPublisher{client: client, topic: topic, tracer: otel.Tracer("wallet.kafka")}, nil
}

func (p *KafkaPublisher) Publish(ctx context.Context, event Event) error {
	eventID := event.Headers["event_id"]
	if eventID == "" {
		return fmt.Errorf("outbox event %d has no event_id header", event.ID)
	}

	ctx, span := p.tracer.Start(ctx, "kafka.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination.name", p.topic),
			attribute.String("messaging.kafka.message_key", event.AggregateID.String()),
			attribute.String("wallet.event.type", event.EventType),
			attribute.String("wallet.aggregate.type", event.AggregateType),
		),
	)
	defer span.End()

	headers := make([]kgo.RecordHeader, 0, len(event.Headers)+2)
	for key, value := range event.Headers {
		headers = append(headers, kgo.RecordHeader{Key: key, Value: []byte(value)})
	}
	telemetry.InjectTrace(ctx, event.Headers)
	for key, value := range event.Headers {
		alreadyPresent := false
		for _, h := range headers {
			if h.Key == key {
				h.Value = []byte(value)
				alreadyPresent = true
				break
			}
		}
		if !alreadyPresent {
			headers = append(headers, kgo.RecordHeader{Key: key, Value: []byte(value)})
		}
	}

	record := &kgo.Record{
		Topic:   p.topic,
		Key:     []byte(event.AggregateID.String()),
		Value:   event.Payload,
		Headers: headers,
	}
	if err := p.client.ProduceSync(ctx, record).FirstErr(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("produce event %s to kafka: %w", eventID, err)
	}
	return nil
}

func (p *KafkaPublisher) Close() {
	p.client.Close()
}
