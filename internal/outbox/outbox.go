package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Event struct {
	ID            int64
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	Payload       []byte
	Headers       map[string]string
	Attempts      int
}

type Publisher interface {
	Publish(ctx context.Context, event Event) error
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) Claim(ctx context.Context, tx pgx.Tx, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	rows, err := tx.Query(ctx, `
		SELECT e.id, e.aggregate_type, e.aggregate_id, e.event_type,
		       e.payload, e.headers, e.attempts
		  FROM outbox_events e
		 WHERE e.published_at IS NULL
		   AND NOT EXISTS (
		       SELECT 1
		         FROM outbox_events p
		        WHERE p.aggregate_type = e.aggregate_type
		          AND p.aggregate_id = e.aggregate_id
		          AND p.id < e.id
		          AND p.published_at IS NULL
		   )
		 ORDER BY e.id
		 LIMIT $1
		   FOR UPDATE OF e SKIP LOCKED`, limit)
	if err != nil {
		return nil, fmt.Errorf("claim outbox events: %w", err)
	}
	defer rows.Close()

	events := make([]Event, 0, limit)
	for rows.Next() {
		var (
			event       Event
			aggregateID pgtype.UUID
			payload     []byte
			headersJSON []byte
		)
		if err := rows.Scan(
			&event.ID,
			&event.AggregateType,
			&aggregateID,
			&event.EventType,
			&payload,
			&headersJSON,
			&event.Attempts,
		); err != nil {
			return nil, fmt.Errorf("scan outbox event: %w", err)
		}
		event.AggregateID = uuid.UUID(aggregateID.Bytes)
		event.Payload = payload
		if err := json.Unmarshal(headersJSON, &event.Headers); err != nil {
			return nil, fmt.Errorf("decode outbox headers for event %d: %w", event.ID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox events: %w", err)
	}
	return events, nil
}

func (s *Store) MarkPublished(ctx context.Context, tx pgx.Tx, eventID int64) error {
	_, err := tx.Exec(ctx, `
		UPDATE outbox_events
		   SET published_at = now(), attempts = attempts + 1, last_error = NULL
		 WHERE id = $1`, eventID)
	if err != nil {
		return fmt.Errorf("mark outbox event %d published: %w", eventID, err)
	}
	return nil
}

func (s *Store) MarkFailed(ctx context.Context, tx pgx.Tx, eventID int64, cause error) error {
	message := ""
	if cause != nil {
		message = truncate(cause.Error(), 1000)
	}
	_, err := tx.Exec(ctx, `
		UPDATE outbox_events
		   SET attempts = attempts + 1, last_error = $2
		 WHERE id = $1`, eventID, message)
	if err != nil {
		return fmt.Errorf("mark outbox event %d failed: %w", eventID, err)
	}
	return nil
}

type Relay struct {
	store        *Store
	publisher    Publisher
	logger       *slog.Logger
	batchSize    int
	pollInterval time.Duration
	maxBackoff   time.Duration
}

func NewRelay(store *Store, publisher Publisher, logger *slog.Logger, batchSize int, pollInterval, maxBackoff time.Duration) *Relay {
	if batchSize <= 0 {
		batchSize = 50
	}
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	if maxBackoff < pollInterval {
		maxBackoff = pollInterval
	}
	return &Relay{
		store:        store,
		publisher:    publisher,
		logger:       logger,
		batchSize:    batchSize,
		pollInterval: pollInterval,
		maxBackoff:   maxBackoff,
	}
}

func (r *Relay) Run(ctx context.Context) error {
	backoff := r.pollInterval
	for {
		published, err := r.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			r.logger.Error("outbox relay batch failed", "error", err, "published", published, "backoff", backoff)
			if !sleep(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff, r.maxBackoff)
			continue
		}
		backoff = r.pollInterval
		if published == 0 && !sleep(ctx, r.pollInterval) {
			return nil
		}
	}
}

func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	tx, err := r.store.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin outbox transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	events, err := r.store.Claim(ctx, tx, r.batchSize)
	if err != nil {
		return 0, err
	}
	if len(events) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("commit empty outbox batch: %w", err)
		}
		return 0, nil
	}

	published := 0
	for _, event := range events {
		if err := r.publisher.Publish(ctx, event); err != nil {
			failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			markErr := r.store.MarkFailed(failureCtx, tx, event.ID, err)
			commitErr := tx.Commit(failureCtx)
			cancel()
			if markErr != nil {
				return published, errors.Join(fmt.Errorf("publish outbox event %d: %w", event.ID, err), markErr)
			}
			if commitErr != nil {
				return published, errors.Join(fmt.Errorf("publish outbox event %d: %w", event.ID, err), commitErr)
			}
			return published, fmt.Errorf("publish outbox event %d (%s): %w", event.ID, event.EventType, err)
		}
		if err := r.store.MarkPublished(ctx, tx, event.ID); err != nil {
			return published, err
		}
		published++
		r.logger.Info("outbox event published",
			"event_id", event.ID,
			"event_type", event.EventType,
			"aggregate_type", event.AggregateType,
			"aggregate_id", event.AggregateID,
		)
	}

	if err := tx.Commit(ctx); err != nil {
		return published, fmt.Errorf("commit outbox batch: %w", err)
	}
	return published, nil
}

func nextBackoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		next = max
	}
	if next <= 0 {
		return next
	}
	jitter := next / 4
	if jitter <= 0 {
		return next
	}
	delta := time.Duration(rand.Int64N(int64(jitter*2)+1)) - jitter
	result := next + delta
	if result <= 0 {
		return current
	}
	if result > max {
		return max
	}
	return result
}

func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
