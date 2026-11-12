package ledger

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stevians/wallet-service/internal/platform/telemetry"
)

func insertOutboxEvent(ctx context.Context, tx pgx.Tx, aggregateType string, aggregateID uuid.UUID, eventType string, payload any) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	headers := map[string]string{
		"event_id":       uuid.NewString(),
		"schema_version": "1",
	}
	telemetry.InjectTrace(ctx, headers)
	headersJSON, err := json.Marshal(headers)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload, headers)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb)`,
		aggregateType, toPgUUID(aggregateID), eventType, string(payloadJSON), string(headersJSON),
	)
	return err
}
