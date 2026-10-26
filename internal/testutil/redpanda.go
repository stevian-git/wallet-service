package testutil

import (
	"context"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
)

func NewRedpanda(t *testing.T) string {
	t.Helper()

	ctx := context.Background()
	container, err := redpanda.Run(ctx,
		"docker.redpanda.com/redpandadata/redpanda:v25.2.4",
		redpanda.WithAutoCreateTopics(),
	)
	if err != nil {
		t.Fatalf("start redpanda container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate redpanda container: %v", err)
		}
	})

	broker, err := container.KafkaSeedBroker(ctx)
	if err != nil {
		t.Fatalf("get redpanda broker: %v", err)
	}
	return broker
}
