package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/stevians/wallet-service/internal/app"
	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/reconciliation"
	"github.com/stevians/wallet-service/internal/testutil"
)

const testAPIKey = "test-api-key"

type apiClient struct {
	t       *testing.T
	handler http.Handler
}

type problemResponse struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Code      string `json:"code"`
	RequestID string `json:"request_id"`
}

func TestHTTPAPIEndToEnd(t *testing.T) {
	pg := testutil.NewPostgres(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := app.New(ledger.New(pg.Pool), idempotency.New(pg.Pool, 5*time.Second), reconciliation.NewService(pg.Pool))
	server := New(service, logger, testAPIKey, pg.Pool.Ping)
	client := &apiClient{t: t, handler: server.Handler()}

	health := client.request(http.MethodGet, "/healthz", nil, "", false)
	if health.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", health.Code)
	}
	ready := client.request(http.MethodGet, "/readyz", nil, "", false)
	if ready.Code != http.StatusOK {
		t.Fatalf("readyz status = %d", ready.Code)
	}

	metrics := client.request(http.MethodGet, "/metrics", nil, "", false)
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", metrics.Code)
	}
	if body := metrics.Body.String(); !strings.Contains(body, "wallet_http_requests_total") {
		t.Fatalf("metrics body missing wallet_http_requests_total: %s", body[:min(len(body), 200)])
	}

	unauthorized := client.request(http.MethodGet, "/v1/accounts/"+uuid.NewString(), nil, "", false)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	if got := unauthorized.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("unauthorized content type = %q", got)
	}

	accountA := client.createAccount("owner-a", "create-a", http.StatusCreated)
	replayedA := client.createAccount("owner-a", "create-a", http.StatusCreated)
	if replayedA.ID != accountA.ID {
		t.Fatalf("account replay returned different id: %s vs %s", replayedA.ID, accountA.ID)
	}

	keyReused := client.request(http.MethodPost, "/v1/accounts", map[string]any{
		"owner_ref": "owner-a-other",
		"currency":  "IDR",
	}, "create-a", true)
	assertProblem(t, keyReused, http.StatusUnprocessableEntity, "idempotency_key_reused")

	missingKey := client.request(http.MethodPost, "/v1/accounts", map[string]any{
		"owner_ref": "owner-no-key",
		"currency":  "IDR",
	}, "", true)
	assertProblem(t, missingKey, http.StatusUnprocessableEntity, "validation_failed")

	duplicate := client.request(http.MethodPost, "/v1/accounts", map[string]any{
		"owner_ref": "owner-a",
		"currency":  "IDR",
	}, "create-a-duplicate", true)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate account status = %d, body = %s", duplicate.Code, duplicate.Body.String())
	}
	var duplicateProblem problemResponse
	decode(t, duplicate, &duplicateProblem)
	if duplicateProblem.Title != "Account already exists" {
		t.Fatalf("duplicate account problem = %+v", duplicateProblem)
	}

	unknownField := client.request(http.MethodPost, "/v1/accounts", map[string]any{
		"owner_ref":  "owner-unknown",
		"currency":   "IDR",
		"unexpected": true,
	}, "create-unknown-field", true)
	assertProblem(t, unknownField, http.StatusUnprocessableEntity, "validation_failed")

	accountB := client.createAccount("owner-b", "create-b", http.StatusCreated)

	topup := client.postForTransfer("/v1/topups", map[string]any{
		"destination_account_id": accountA.ID,
		"amount":                 100_000,
		"currency":               "IDR",
		"reference":              "topup-1",
		"metadata":               map[string]any{"channel": "test"},
	}, "topup-a", http.StatusCreated)
	if topup.Type != ledger.TransferTypeTopup || topup.Amount != 100_000 {
		t.Fatalf("unexpected topup: %+v", topup)
	}

	replayedTopup := client.postForTransfer("/v1/topups", map[string]any{
		"destination_account_id": accountA.ID,
		"amount":                 100_000,
		"currency":               "IDR",
		"reference":              "topup-1",
		"metadata":               map[string]any{"channel": "test"},
	}, "topup-a", http.StatusCreated)
	if replayedTopup.ID != topup.ID {
		t.Fatalf("topup replay returned different id: %s vs %s", replayedTopup.ID, topup.ID)
	}
	assertBalance(t, client, accountA.ID, 100_000)
	assertBalance(t, client, accountB.ID, 0)

	zeroTopup := client.request(http.MethodPost, "/v1/topups", map[string]any{
		"destination_account_id": accountA.ID,
		"amount":                 0,
		"currency":               "IDR",
	}, "topup-zero", true)
	assertProblem(t, zeroTopup, http.StatusUnprocessableEntity, "validation_failed")

	transfer := client.postForTransfer("/v1/transfers", map[string]any{
		"source_account_id":      accountA.ID,
		"destination_account_id": accountB.ID,
		"amount":                 25_000,
		"currency":               "IDR",
		"reference":              "transfer-1",
	}, "transfer-ab", http.StatusCreated)
	if transfer.Type != ledger.TransferTypeTransfer {
		t.Fatalf("unexpected transfer type: %s", transfer.Type)
	}

	replayedTransfer := client.postForTransfer("/v1/transfers", map[string]any{
		"source_account_id":      accountA.ID,
		"destination_account_id": accountB.ID,
		"amount":                 25_000,
		"currency":               "IDR",
		"reference":              "transfer-1",
	}, "transfer-ab", http.StatusCreated)
	if replayedTransfer.ID != transfer.ID {
		t.Fatalf("transfer replay returned different id: %s vs %s", replayedTransfer.ID, transfer.ID)
	}
	assertBalance(t, client, accountA.ID, 75_000)
	assertBalance(t, client, accountB.ID, 25_000)

	insufficient := client.request(http.MethodPost, "/v1/transfers", map[string]any{
		"source_account_id":      accountA.ID,
		"destination_account_id": accountB.ID,
		"amount":                 1_000_000,
		"currency":               "IDR",
	}, "transfer-insufficient", true)
	assertProblem(t, insufficient, http.StatusUnprocessableEntity, "insufficient_funds")

	replayedInsufficient := client.request(http.MethodPost, "/v1/transfers", map[string]any{
		"source_account_id":      accountA.ID,
		"destination_account_id": accountB.ID,
		"amount":                 1_000_000,
		"currency":               "IDR",
	}, "transfer-insufficient", true)
	assertProblem(t, replayedInsufficient, http.StatusUnprocessableEntity, "insufficient_funds")
	if replayedInsufficient.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("expected insufficient funds error to be replayed")
	}
	assertBalance(t, client, accountA.ID, 75_000)
	assertBalance(t, client, accountB.ID, 25_000)

	detail := client.getTransfer(transfer.ID)
	if detail.Status != ledger.TransferStatusPosted || len(detail.Entries) != 2 {
		t.Fatalf("unexpected transfer detail: %+v", detail)
	}

	firstPage := client.getEntries(accountA.ID, "", 1)
	if len(firstPage.Data) != 1 || firstPage.NextCursor == nil {
		t.Fatalf("unexpected first page: %+v", firstPage)
	}
	if firstPage.Data[0].Direction != ledger.DirectionDebit || firstPage.Data[0].Amount != 25_000 {
		t.Fatalf("unexpected first entry: %+v", firstPage.Data[0])
	}

	secondPage := client.getEntries(accountA.ID, *firstPage.NextCursor, 1)
	if len(secondPage.Data) != 1 || secondPage.NextCursor != nil {
		t.Fatalf("unexpected second page: %+v", secondPage)
	}
	if secondPage.Data[0].Direction != ledger.DirectionCredit || secondPage.Data[0].Amount != 100_000 {
		t.Fatalf("unexpected second entry: %+v", secondPage.Data[0])
	}

	invalidCursor := client.request(http.MethodGet, "/v1/accounts/"+accountA.ID.String()+"/entries?cursor=not-a-cursor", nil, "", true)
	assertProblem(t, invalidCursor, http.StatusUnprocessableEntity, "validation_failed")

	invalidLimit := client.request(http.MethodGet, "/v1/accounts/"+accountA.ID.String()+"/entries?limit=300", nil, "", true)
	assertProblem(t, invalidLimit, http.StatusUnprocessableEntity, "validation_failed")

	invalidUUID := client.request(http.MethodGet, "/v1/accounts/not-a-uuid", nil, "", true)
	assertProblem(t, invalidUUID, http.StatusUnprocessableEntity, "validation_failed")

	missingAccount := client.request(http.MethodGet, "/v1/accounts/"+uuid.NewString(), nil, "", true)
	assertProblem(t, missingAccount, http.StatusNotFound, "not_found")

	reversal := client.postForTransfer("/v1/transfers/"+transfer.ID.String()+"/reversal", map[string]any{
		"reason": "customer request",
	}, "reverse-ab", http.StatusCreated)
	if reversal.Type != ledger.TransferTypeReversal || reversal.ReversesTransferID == nil || *reversal.ReversesTransferID != transfer.ID {
		t.Fatalf("unexpected reversal: %+v", reversal)
	}

	replayedReversal := client.postForTransfer("/v1/transfers/"+transfer.ID.String()+"/reversal", map[string]any{
		"reason": "customer request",
	}, "reverse-ab", http.StatusCreated)
	if replayedReversal.ID != reversal.ID {
		t.Fatalf("reversal replay returned different id: %s vs %s", replayedReversal.ID, reversal.ID)
	}
	assertBalance(t, client, accountA.ID, 100_000)
	assertBalance(t, client, accountB.ID, 0)

	alreadyReversed := client.request(http.MethodPost, "/v1/transfers/"+transfer.ID.String()+"/reversal", map[string]any{
		"reason": "again",
	}, "reverse-ab-again", true)
	assertProblem(t, alreadyReversed, http.StatusConflict, "already_reversed")

	withdrawal := client.postForTransfer("/v1/withdrawals", map[string]any{
		"source_account_id": accountA.ID,
		"amount":            5_000,
		"currency":          "IDR",
	}, "withdraw-a", http.StatusCreated)
	if withdrawal.Type != ledger.TransferTypeWithdrawal {
		t.Fatalf("unexpected withdrawal: %+v", withdrawal)
	}
	assertBalance(t, client, accountA.ID, 95_000)

	var outboxCount int
	if err := pg.Pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox_events`).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox events: %v", err)
	}
	if outboxCount != 6 {
		t.Fatalf("outbox events = %d, want 6 (2 accounts, topup, transfer, reversal, withdrawal); replays and failures must not add events", outboxCount)
	}

	integrity := client.request(http.MethodGet, "/v1/admin/integrity", nil, "", true)
	if integrity.Code != http.StatusOK {
		t.Fatalf("integrity status = %d, body = %s", integrity.Code, integrity.Body.String())
	}
	var report app.IntegrityView
	decode(t, integrity, &report)
	if !report.OK || len(report.BalanceDrift) != 0 || len(report.UnbalancedTransfers) != 0 || len(report.PostedWithoutEntries) != 0 {
		t.Fatalf("unexpected integrity report: %+v", report)
	}

	notFound := client.request(http.MethodGet, "/v1/does-not-exist", nil, "", true)
	assertProblem(t, notFound, http.StatusNotFound, "not_found")
}

func (c *apiClient) createAccount(owner, key string, wantStatus int) app.AccountView {
	c.t.Helper()
	recorder := c.request(http.MethodPost, "/v1/accounts", map[string]any{
		"owner_ref": owner,
		"currency":  "IDR",
	}, key, true)
	if recorder.Code != wantStatus {
		c.t.Fatalf("create account %s status = %d, body = %s", owner, recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Idempotent-Replayed") == "true" {
		c.t.Logf("account %s response was replayed", owner)
	}
	var account app.AccountView
	decode(c.t, recorder, &account)
	return account
}

func (c *apiClient) postForTransfer(path string, body map[string]any, key string, wantStatus int) app.TransferView {
	c.t.Helper()
	recorder := c.request(http.MethodPost, path, body, key, true)
	if recorder.Code != wantStatus {
		c.t.Fatalf("%s status = %d, body = %s", path, recorder.Code, recorder.Body.String())
	}
	var transfer app.TransferView
	decode(c.t, recorder, &transfer)
	return transfer
}

func (c *apiClient) getTransfer(id uuid.UUID) app.TransferDetailView {
	c.t.Helper()
	recorder := c.request(http.MethodGet, "/v1/transfers/"+id.String(), nil, "", true)
	if recorder.Code != http.StatusOK {
		c.t.Fatalf("get transfer status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var detail app.TransferDetailView
	decode(c.t, recorder, &detail)
	return detail
}

func (c *apiClient) getEntries(accountID uuid.UUID, cursor string, limit int) app.EntriesPage {
	c.t.Helper()
	path := "/v1/accounts/" + accountID.String() + "/entries?limit=" + strconv.Itoa(limit)
	if cursor != "" {
		path += "&cursor=" + cursor
	}
	recorder := c.request(http.MethodGet, path, nil, "", true)
	if recorder.Code != http.StatusOK {
		c.t.Fatalf("get entries status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var page app.EntriesPage
	decode(c.t, recorder, &page)
	return page
}

func (c *apiClient) request(method, path string, body any, idempotencyKey string, auth bool) *httptest.ResponseRecorder {
	c.t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			c.t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	request := httptest.NewRequest(method, path, reader)
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if auth {
		request.Header.Set("X-API-Key", testAPIKey)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}

	recorder := httptest.NewRecorder()
	c.handler.ServeHTTP(recorder, request)
	return recorder
}

func assertBalance(t *testing.T, client *apiClient, accountID uuid.UUID, want int64) {
	t.Helper()
	recorder := client.request(http.MethodGet, "/v1/accounts/"+accountID.String()+"/balance", nil, "", true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("balance status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var balance app.BalanceView
	decode(t, recorder, &balance)
	if balance.Posted != want || balance.Available != want {
		t.Fatalf("unexpected balance for %s: %+v (want posted %d)", accountID, balance, want)
	}
}

func assertProblem(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type = %q", got)
	}
	var payload problemResponse
	decode(t, recorder, &payload)
	if payload.Status != wantStatus {
		t.Fatalf("problem status = %d, body = %s", payload.Status, recorder.Body.String())
	}
	if payload.Code != wantCode {
		t.Fatalf("problem code = %q, want %q, body = %s", payload.Code, wantCode, recorder.Body.String())
	}
	if payload.Type == "" || payload.Title == "" {
		t.Fatalf("problem missing type/title: %+v", payload)
	}
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
}
