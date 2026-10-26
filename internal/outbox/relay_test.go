package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stevians/wallet-service/internal/testutil"
)

type recordingPublisher struct {
	mu        sync.Mutex
	events    []Event
	failOn    map[int64]error
	failCount map[int64]int
}

func newRecordingPublisher() *recordingPublisher {
	return &recordingPublisher{
		failOn:    make(map[int64]error),
		failCount: make(map[int64]int),
	}
}

func (p *recordingPublisher) Publish(_ context.Context, event Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err, ok := p.failOn[event.ID]; ok {
		if p.failCount[event.ID] == 0 {
			p.failCount[event.ID]++
			return err
		}
		delete(p.failOn, event.ID)
	}
	p.events = append(p.events, event)
	return nil
}

func (p *recordingPublisher) snapshot() []Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Event(nil), p.events...)
}

func TestRelayPublishesPerAggregateInOrder(t *testing.T) {
	pg := testutil.NewPostgres(t)
	ctx := context.Background()
	store := NewStore(pg.Pool)
	publisher := newRecordingPublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	relay := NewRelay(store, publisher, logger, 50, time.Millisecond, time.Second)

	aggregateA := uuid.New()
	aggregateB := uuid.New()
	insertEvent(t, pg.Pool, aggregateA, "a-1")
	insertEvent(t, pg.Pool, aggregateB, "b-1")
	insertEvent(t, pg.Pool, aggregateA, "a-2")
	insertEvent(t, pg.Pool, aggregateB, "b-2")
	insertEvent(t, pg.Pool, aggregateA, "a-3")

	wantBatches := [][]string{
		{"a-1", "b-1"},
		{"a-2", "b-2"},
		{"a-3"},
	}
	for i, want := range wantBatches {
		published, err := relay.RunOnce(ctx)
		if err != nil {
			t.Fatalf("run once %d: %v", i, err)
		}
		if published != len(want) {
			t.Fatalf("run once %d published = %d, want %d", i, published, len(want))
		}
	}
	if published, err := relay.RunOnce(ctx); err != nil || published != 0 {
		t.Fatalf("empty run = %d, %v; want 0, nil", published, err)
	}

	var got []string
	for _, event := range publisher.snapshot() {
		got = append(got, event.EventType)
	}
	want := []string{"a-1", "b-1", "a-2", "b-2", "a-3"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("published order = %v, want %v", got, want)
	}

	var unpublished, failed int
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
		t.Fatal(err)
	}
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE attempts <> 1`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if unpublished != 0 || failed != 0 {
		t.Fatalf("unpublished = %d, attempts != 1 = %d", unpublished, failed)
	}
}

func TestRelayMidBatchFailureDoesNotLoseEvents(t *testing.T) {
	pg := testutil.NewPostgres(t)
	ctx := context.Background()
	store := NewStore(pg.Pool)
	publisher := newRecordingPublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	relay := NewRelay(store, publisher, logger, 50, time.Millisecond, time.Second)

	aggregateA := uuid.New()
	aggregateB := uuid.New()
	a1 := insertEvent(t, pg.Pool, aggregateA, "a-1")
	b1 := insertEvent(t, pg.Pool, aggregateB, "b-1")
	insertEvent(t, pg.Pool, aggregateA, "a-2")

	publisher.failOn[b1] = errors.New("broker unavailable")
	published, err := relay.RunOnce(ctx)
	if err == nil {
		t.Fatal("expected publish error")
	}
	if published != 1 {
		t.Fatalf("published before failure = %d, want 1", published)
	}

	assertPublished(t, pg.Pool, a1, true)
	assertPublished(t, pg.Pool, b1, false)
	var attempts int
	var lastError string
	if err := pg.Pool.QueryRow(ctx, `SELECT attempts, last_error FROM outbox_events WHERE id = $1`, b1).Scan(&attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastError == "" {
		t.Fatalf("failed event attempts = %d, last_error = %q", attempts, lastError)
	}

	published, err = relay.RunOnce(ctx)
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if published != 2 {
		t.Fatalf("retry published = %d, want 2", published)
	}
	assertPublished(t, pg.Pool, b1, true)
	if err := pg.Pool.QueryRow(ctx, `SELECT attempts FROM outbox_events WHERE id = $1`, b1).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("retried event attempts = %d, want 2", attempts)
	}

	var unpublished int
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
		t.Fatal(err)
	}
	if unpublished != 0 {
		t.Fatalf("unpublished after retry = %d", unpublished)
	}
}

func TestTwoRelaysPublishWithoutDuplicateOrReordering(t *testing.T) {
	pg := testutil.NewPostgres(t)
	ctx := context.Background()
	store := NewStore(pg.Pool)
	publisher := newRecordingPublisher()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	relay := NewRelay(store, publisher, logger, 3, time.Millisecond, time.Second)

	const (
		aggregates = 10
		perAgg     = 5
	)
	for i := 0; i < perAgg; i++ {
		for a := 0; a < aggregates; a++ {
			aggregateID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("aggregate-%d", a)))
			insertEvent(t, pg.Pool, aggregateID, fmt.Sprintf("%s-%d", aggregateID, i))
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if _, err := relay.RunOnce(ctx); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent relay: %v", err)
	}

	for {
		published, err := relay.RunOnce(ctx)
		if err != nil {
			t.Fatalf("drain relay: %v", err)
		}
		if published == 0 {
			break
		}
	}

	seen := make(map[int64]int)
	lastSequence := make(map[uuid.UUID]int)
	for _, event := range publisher.snapshot() {
		seen[event.ID]++
		if seen[event.ID] > 1 {
			t.Fatalf("event %d published more than once", event.ID)
		}
		index := strings.LastIndex(event.EventType, "-")
		if index < 0 {
			t.Fatalf("unexpected event type %q", event.EventType)
		}
		sequence, err := strconv.Atoi(event.EventType[index+1:])
		if err != nil {
			t.Fatalf("parse sequence from %q: %v", event.EventType, err)
		}
		if previous, ok := lastSequence[event.AggregateID]; ok && sequence != previous+1 {
			t.Fatalf("aggregate %s published sequence %d after %d", event.AggregateID, sequence, previous)
		}
		lastSequence[event.AggregateID] = sequence
	}
	if len(seen) != aggregates*perAgg {
		t.Fatalf("published unique events = %d, want %d", len(seen), aggregates*perAgg)
	}

	var unpublished int
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
		t.Fatal(err)
	}
	if unpublished != 0 {
		t.Fatalf("unpublished events = %d", unpublished)
	}
}

func insertEvent(t *testing.T, pool *pgxpool.Pool, aggregateID uuid.UUID, eventType string) int64 {
	t.Helper()
	headers, err := json.Marshal(map[string]string{
		"event_id":       uuid.NewString(),
		"schema_version": "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"test": eventType})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	err = pool.QueryRow(context.Background(), `
		INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload, headers)
		VALUES ('test', $1::uuid, $2, $3::jsonb, $4::jsonb)
		RETURNING id`,
		aggregateID.String(), eventType, string(payload), string(headers),
	).Scan(&id)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return id
}

func assertPublished(t *testing.T, pool *pgxpool.Pool, eventID int64, want bool) {
	t.Helper()
	var published bool
	if err := pool.QueryRow(context.Background(), `SELECT published_at IS NOT NULL FROM outbox_events WHERE id = $1`, eventID).Scan(&published); err != nil {
		t.Fatal(err)
	}
	if published != want {
		t.Fatalf("event %d published = %v, want %v", eventID, published, want)
	}
}
