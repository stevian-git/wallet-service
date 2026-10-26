package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/stevians/wallet-service/internal/app"
	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/testutil"
)

func TestHoldsHTTPAPI(t *testing.T) {
	pg := testutil.NewPostgres(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := app.New(ledger.New(pg.Pool), idempotency.New(pg.Pool, 5*time.Second))
	server := New(service, logger, testAPIKey, pg.Pool.Ping)
	client := &apiClient{t: t, handler: server.Handler()}

	accountA := client.createAccount("hold-http-a", "hold-create-a", http.StatusCreated)
	accountB := client.createAccount("hold-http-b", "hold-create-b", http.StatusCreated)
	client.postForTransfer("/v1/topups", map[string]any{
		"destination_account_id": accountA.ID,
		"amount":                 100_000,
		"currency":               "IDR",
	}, "hold-topup-a", http.StatusCreated)

	invalidTTL := client.request(http.MethodPost, "/v1/holds", map[string]any{
		"account_id":  accountA.ID,
		"amount":      10_000,
		"currency":    "IDR",
		"ttl_seconds": 0,
	}, "hold-invalid-ttl", true)
	assertProblem(t, invalidTTL, http.StatusUnprocessableEntity, "validation_failed")

	missingKey := client.request(http.MethodPost, "/v1/holds", map[string]any{
		"account_id":  accountA.ID,
		"amount":      10_000,
		"currency":    "IDR",
		"ttl_seconds": 60,
	}, "", true)
	assertProblem(t, missingKey, http.StatusUnprocessableEntity, "validation_failed")

	hold := createHoldHTTP(t, client, accountA.ID, 30_000, "hold-1", "hold-create-1", http.StatusCreated)
	if hold.Status != ledger.HoldStatusActive || hold.Reference == nil || *hold.Reference != "hold-1" {
		t.Fatalf("unexpected hold: %+v", hold)
	}
	assertBalanceFull(t, client, accountA.ID, 100_000, 30_000)
	assertBalanceFull(t, client, accountB.ID, 0, 0)

	replayed := createHoldHTTP(t, client, accountA.ID, 30_000, "hold-1", "hold-create-1", http.StatusCreated)
	if replayed.ID != hold.ID {
		t.Fatalf("hold replay id = %s, want %s", replayed.ID, hold.ID)
	}
	replayRecorder := client.request(http.MethodPost, "/v1/holds", holdBody(accountA.ID, 30_000, "hold-1"), "hold-create-1", true)
	if replayRecorder.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("expected hold replay header")
	}

	keyReused := client.request(http.MethodPost, "/v1/holds", holdBody(accountA.ID, 31_000, "hold-1"), "hold-create-1", true)
	assertProblem(t, keyReused, http.StatusUnprocessableEntity, "idempotency_key_reused")

	getHold := client.request(http.MethodGet, "/v1/holds/"+hold.ID.String(), nil, "", true)
	if getHold.Code != http.StatusOK {
		t.Fatalf("get hold status = %d, body = %s", getHold.Code, getHold.Body.String())
	}
	var gotHold app.HoldView
	decode(t, getHold, &gotHold)
	if gotHold.ID != hold.ID || gotHold.Status != ledger.HoldStatusActive {
		t.Fatalf("unexpected get hold: %+v", gotHold)
	}

	capture := client.postForTransfer("/v1/holds/"+hold.ID.String()+"/capture", map[string]any{
		"destination_account_id": accountB.ID,
		"amount":                 20_000,
	}, "hold-capture-1", http.StatusCreated)
	if capture.Type != ledger.TransferTypeHoldCapture || capture.Amount != 20_000 {
		t.Fatalf("unexpected capture: %+v", capture)
	}
	replayedCapture := client.postForTransfer("/v1/holds/"+hold.ID.String()+"/capture", map[string]any{
		"destination_account_id": accountB.ID,
		"amount":                 20_000,
	}, "hold-capture-1", http.StatusCreated)
	if replayedCapture.ID != capture.ID {
		t.Fatalf("capture replay id = %s, want %s", replayedCapture.ID, capture.ID)
	}
	assertBalanceFull(t, client, accountA.ID, 80_000, 0)
	assertBalanceFull(t, client, accountB.ID, 20_000, 0)

	captured := client.request(http.MethodGet, "/v1/holds/"+hold.ID.String(), nil, "", true)
	var capturedHold app.HoldView
	decode(t, captured, &capturedHold)
	if capturedHold.Status != ledger.HoldStatusCaptured || capturedHold.TransferID == nil || *capturedHold.TransferID != capture.ID {
		t.Fatalf("unexpected captured hold: %+v", capturedHold)
	}

	captureAgain := client.request(http.MethodPost, "/v1/holds/"+hold.ID.String()+"/capture", map[string]any{
		"destination_account_id": accountB.ID,
	}, "hold-capture-2", true)
	assertProblem(t, captureAgain, http.StatusConflict, "hold_not_active")

	releaseCaptured := client.request(http.MethodPost, "/v1/holds/"+hold.ID.String()+"/release", nil, "hold-release-captured", true)
	assertProblem(t, releaseCaptured, http.StatusConflict, "hold_not_active")

	releasedHold := createHoldHTTP(t, client, accountA.ID, 10_000, "hold-2", "hold-create-2", http.StatusCreated)
	assertBalanceFull(t, client, accountA.ID, 80_000, 10_000)
	releasedRecorder := client.request(http.MethodPost, "/v1/holds/"+releasedHold.ID.String()+"/release", nil, "hold-release-2", true)
	if releasedRecorder.Code != http.StatusOK {
		t.Fatalf("release hold status = %d, body = %s", releasedRecorder.Code, releasedRecorder.Body.String())
	}
	var released app.HoldView
	decode(t, releasedRecorder, &released)
	if released.Status != ledger.HoldStatusReleased || released.ResolvedAt == nil {
		t.Fatalf("unexpected released hold: %+v", released)
	}
	assertBalanceFull(t, client, accountA.ID, 80_000, 0)

	expiring := createHoldHTTPWithTTL(t, client, accountA.ID, 5_000, "hold-3", "hold-create-3", 1, http.StatusCreated)
	time.Sleep(1100 * time.Millisecond)
	expiredCapture := client.request(http.MethodPost, "/v1/holds/"+expiring.ID.String()+"/capture", map[string]any{
		"destination_account_id": accountB.ID,
	}, "hold-capture-expired", true)
	assertProblem(t, expiredCapture, http.StatusConflict, "hold_expired")
	assertBalanceFull(t, client, accountA.ID, 80_000, 5_000)

	tooLarge := client.request(http.MethodPost, "/v1/holds", map[string]any{
		"account_id":  accountA.ID,
		"amount":      80_000,
		"currency":    "IDR",
		"ttl_seconds": 60,
	}, "hold-too-large", true)
	assertProblem(t, tooLarge, http.StatusUnprocessableEntity, "insufficient_funds")

	var outboxCount int
	if err := pg.Pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events`).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	// 2 accounts, topup, hold.created, transfer.posted + hold.captured,
	// hold.created, hold.released, hold.created. Replays and failures add none.
	if outboxCount != 9 {
		t.Fatalf("outbox events = %d, want 9", outboxCount)
	}

	integrity := client.request(http.MethodGet, "/v1/admin/integrity", nil, "", true)
	if integrity.Code != http.StatusOK {
		t.Fatalf("integrity status = %d", integrity.Code)
	}
	var report app.IntegrityView
	decode(t, integrity, &report)
	if !report.OK {
		t.Fatalf("integrity not ok: %+v", report)
	}
}

func createHoldHTTP(t *testing.T, client *apiClient, accountID uuid.UUID, amount int64, reference, key string, wantStatus int) app.HoldView {
	t.Helper()
	return createHoldHTTPWithTTL(t, client, accountID, amount, reference, key, 60, wantStatus)
}

func createHoldHTTPWithTTL(t *testing.T, client *apiClient, accountID uuid.UUID, amount int64, reference, key string, ttlSeconds int64, wantStatus int) app.HoldView {
	t.Helper()
	recorder := client.request(http.MethodPost, "/v1/holds", map[string]any{
		"account_id":  accountID,
		"amount":      amount,
		"currency":    "IDR",
		"ttl_seconds": ttlSeconds,
		"reference":   reference,
	}, key, true)
	if recorder.Code != wantStatus {
		t.Fatalf("create hold status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var hold app.HoldView
	decode(t, recorder, &hold)
	return hold
}

func holdBody(accountID uuid.UUID, amount int64, reference string) map[string]any {
	return map[string]any{
		"account_id":  accountID,
		"amount":      amount,
		"currency":    "IDR",
		"ttl_seconds": 60,
		"reference":   reference,
	}
}

func assertBalanceFull(t *testing.T, client *apiClient, accountID uuid.UUID, wantPosted, wantHeld int64) {
	t.Helper()
	recorder := client.request(http.MethodGet, "/v1/accounts/"+accountID.String()+"/balance", nil, "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("balance status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var balance app.BalanceView
	decode(t, recorder, &balance)
	if balance.Posted != wantPosted || balance.Held != wantHeld || balance.Available != wantPosted-wantHeld {
		t.Fatalf("balance for %s = %+v, want posted=%d held=%d", accountID, balance, wantPosted, wantHeld)
	}
}
