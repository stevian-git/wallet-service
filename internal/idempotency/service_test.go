package idempotency

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stevians/wallet-service/internal/testutil"
)

func TestReplayAndKeyReuse(t *testing.T) {
	pg := testutil.NewPostgres(t)
	service := New(pg.Pool, 5*time.Second)
	ctx := context.Background()

	var calls atomic.Int32
	work := func(_ context.Context, _ pgx.Tx) (Response, error) {
		calls.Add(1)
		return Response{StatusCode: 201, Body: []byte(`{"ok":true}`)}, nil
	}

	request := Request{
		Scope:  "test",
		Key:    "key-1",
		Method: "POST",
		Path:   "/v1/test",
		Body:   []byte(`{"amount":100}`),
	}

	first, err := service.Do(ctx, request, work, nil)
	if err != nil {
		t.Fatalf("first do: %v", err)
	}
	if first.Replayed {
		t.Fatal("first response must not be a replay")
	}
	if first.Response.StatusCode != 201 || string(first.Response.Body) != `{"ok": true}` {
		t.Fatalf("unexpected first response: %+v", first)
	}

	second, err := service.Do(ctx, request, work, nil)
	if err != nil {
		t.Fatalf("second do: %v", err)
	}
	if !second.Replayed {
		t.Fatal("second response must be a replay")
	}
	if second.Response.StatusCode != first.Response.StatusCode || string(second.Response.Body) != string(first.Response.Body) {
		t.Fatalf("replay differs: %+v vs %+v", second.Response, first.Response)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected work to run once, got %d", calls.Load())
	}

	differentBody := request
	differentBody.Body = []byte(`{"amount":200}`)
	if _, err := service.Do(ctx, differentBody, work, nil); !errors.Is(err, ErrKeyReused) {
		t.Fatalf("expected ErrKeyReused, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("reused key must not execute work, got %d calls", calls.Load())
	}
}

func TestStableErrorsAreCachedAndTransientErrorsAreReleased(t *testing.T) {
	pg := testutil.NewPostgres(t)
	service := New(pg.Pool, 5*time.Second)
	ctx := context.Background()

	stableRequest := Request{
		Scope:  "test",
		Key:    "stable",
		Method: "POST",
		Path:   "/v1/test",
		Body:   []byte(`{"x":1}`),
	}
	var stableCalls atomic.Int32
	stableWork := func(_ context.Context, _ pgx.Tx) (Response, error) {
		stableCalls.Add(1)
		return Response{}, errors.New("domain failure")
	}
	mapper := func(error) (Response, bool) {
		return Response{StatusCode: 422, Body: []byte(`{"code":"validation_failed"}`)}, true
	}

	first, err := service.Do(ctx, stableRequest, stableWork, mapper)
	if err != nil {
		t.Fatalf("stable first: %v", err)
	}
	if first.Replayed || first.Response.StatusCode != 422 {
		t.Fatalf("unexpected stable first response: %+v", first)
	}

	second, err := service.Do(ctx, stableRequest, stableWork, mapper)
	if err != nil {
		t.Fatalf("stable second: %v", err)
	}
	if !second.Replayed || string(second.Response.Body) != string(first.Response.Body) {
		t.Fatalf("expected cached stable error, got %+v", second)
	}
	if stableCalls.Load() != 1 {
		t.Fatalf("expected stable work once, got %d", stableCalls.Load())
	}

	transientRequest := stableRequest
	transientRequest.Key = "transient"
	var transientCalls atomic.Int32
	transientWork := func(_ context.Context, _ pgx.Tx) (Response, error) {
		transientCalls.Add(1)
		return Response{}, errors.New("temporary failure")
	}

	for i := 0; i < 2; i++ {
		_, err := service.Do(ctx, transientRequest, transientWork, nil)
		if err == nil || !strings.Contains(err.Error(), "temporary failure") {
			t.Fatalf("transient attempt %d: unexpected error %v", i, err)
		}
	}
	if transientCalls.Load() != 2 {
		t.Fatalf("transient key must be retryable, got %d calls", transientCalls.Load())
	}
}

func TestInProgressReturnsConflictAndCanBeRetriedAfterCompletion(t *testing.T) {
	pg := testutil.NewPostgres(t)
	service := New(pg.Pool, 5*time.Second)
	ctx := context.Background()

	request := Request{
		Scope:  "test",
		Key:    "concurrent",
		Method: "POST",
		Path:   "/v1/test",
		Body:   []byte(`{}`),
	}

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		_, err := service.Do(ctx, request, func(_ context.Context, _ pgx.Tx) (Response, error) {
			close(started)
			<-release
			return Response{StatusCode: 201, Body: []byte(`{"ok":true}`)}, nil
		}, nil)
		done <- err
	}()

	<-started
	if _, err := service.Do(ctx, request, func(context.Context, pgx.Tx) (Response, error) {
		t.Fatal("work must not run while the key is in progress")
		return Response{}, nil
	}, nil); !errors.Is(err, ErrInProgress) {
		t.Fatalf("expected ErrInProgress, got %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first request: %v", err)
	}

	replay, err := service.Do(ctx, request, func(context.Context, pgx.Tx) (Response, error) {
		t.Fatal("work must not run for a completed key")
		return Response{}, nil
	}, nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatal("expected replay after completion")
	}
}
