package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

type KafkaPublisher struct {
	client *kgo.Client
	topic  string
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
	return &KafkaPublisher{client: client, topic: topic}, nil
}

func (p *KafkaPublisher) Publish(ctx context.Context, event Event) error {
	eventID := event.Headers["event_id"]
	if eventID == "" {
		return fmt.Errorf("outbox event %d has no event_id header", event.ID)
	}

	headers := make([]kgo.RecordHeader, 0, len(event.Headers))
	for key, value := range event.Headers {
		headers = append(headers, kgo.RecordHeader{Key: key, Value: []byte(value)})
	}
	record := &kgo.Record{
		Topic:   p.topic,
		Key:     []byte(event.AggregateID.String()),
		Value:   event.Payload,
		Headers: headers,
	}
	if err := p.client.ProduceSync(ctx, record).FirstErr(); err != nil {
		return fmt.Errorf("produce event %s to kafka: %w", eventID, err)
	}
	return nil
}

func (p *KafkaPublisher) Close() {
	p.client.Close()
}
