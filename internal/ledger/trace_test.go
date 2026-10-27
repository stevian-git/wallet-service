package ledger

import (
	"context"
	"encoding/json"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/stevians/wallet-service/internal/platform/telemetry"
	"github.com/stevians/wallet-service/internal/testutil"
)

func TestOutboxEventIncludesTraceparent(t *testing.T) {
	prevTracer := otel.GetTracerProvider()
	prevPropagator := otel.GetTextMapPropagator()
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(telemetry.DefaultTextMapPropagator())
	defer func() {
		otel.SetTracerProvider(prevTracer)
		otel.SetTextMapPropagator(prevPropagator)
	}()
	defer tp.Shutdown(context.Background())

	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	owner := createAccount(t, pg.Pool, book, "trace-owner")

	ctx, span := tp.Tracer("test").Start(ctx, "topup-parent")
	defer span.End()

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	_, err = book.Topup(ctx, tx, TopupInput{
		DestinationAccountID: owner.ID,
		Amount:               100,
		Currency:             "IDR",
	})
	if err != nil {
		t.Fatalf("topup: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var headersJSON string
	err = pg.Pool.QueryRow(context.Background(), `
		SELECT headers::text FROM outbox_events
		WHERE event_type = 'transfer.posted'
		ORDER BY id DESC LIMIT 1
	`).Scan(&headersJSON)
	if err != nil {
		t.Fatalf("query headers: %v", err)
	}

	var headers map[string]string
	if err := json.Unmarshal([]byte(headersJSON), &headers); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	tp_val, ok := headers["traceparent"]
	if !ok {
		t.Fatalf("missing traceparent in headers: %v", headers)
	}
	if len(tp_val) < 55 {
		t.Fatalf("traceparent too short: %q", tp_val)
	}
	if tp_val[0:3] != "00-" {
		t.Fatalf("traceparent doesn't start with 00-: %q", tp_val)
	}
}

func TestOutboxEventWithoutActiveSpan(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	owner := createAccount(t, pg.Pool, book, "no-trace-owner")

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	_, err = book.Topup(ctx, tx, TopupInput{
		DestinationAccountID: owner.ID,
		Amount:               50,
		Currency:             "IDR",
	})
	if err != nil {
		t.Fatalf("topup: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var headersJSON string
	err = pg.Pool.QueryRow(context.Background(), `
		SELECT headers::text FROM outbox_events
		WHERE event_type = 'transfer.posted'
		ORDER BY id DESC LIMIT 1
	`).Scan(&headersJSON)
	if err != nil {
		t.Fatalf("query headers: %v", err)
	}

	var headers map[string]string
	if err := json.Unmarshal([]byte(headersJSON), &headers); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if _, ok := headers["event_id"]; !ok {
		t.Fatalf("missing event_id in headers: %v", headers)
	}
	if _, ok := headers["traceparent"]; ok {
		t.Fatalf("unexpected traceparent in headers (no active span): %v", headers)
	}
}
