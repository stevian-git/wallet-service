package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stevians/wallet-service/internal/app"
	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/reconciliation"
	"github.com/stevians/wallet-service/internal/testutil"
)

func TestCreateReconciliationRunIsIdempotent(t *testing.T) {
	pg := testutil.NewPostgres(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	recon := reconciliation.NewService(pg.Pool)
	service := app.New(ledger.New(pg.Pool), idempotency.New(pg.Pool, 5*time.Second), recon)
	server := New(service, logger, testAPIKey, pg.Pool.Ping)
	client := &apiClient{t: t, handler: server.Handler()}
	ctx := context.Background()

	file, err := recon.Import(ctx, reconciliation.ImportInput{
		Provider: "p", FileName: "f.csv",
		FileContent: []byte("provider_ref,our_reference,amount,currency,status,occurred_at\n" +
			"prov-1,ref-a,100,IDR,success,2026-10-26T10:00:00Z\n"),
		SettlementDate: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	body := map[string]any{"file_id": file.ID}

	first := client.request(http.MethodPost, "/v1/reconciliation/runs", body, "run-key-1", true)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first create status = %d, body = %s", first.Code, first.Body.String())
	}
	var firstRun app.ReconciliationRunView
	decode(t, first, &firstRun)

	// A client retry with the same key replays the original run.
	retry := client.request(http.MethodPost, "/v1/reconciliation/runs", body, "run-key-1", true)
	if retry.Code != http.StatusAccepted || retry.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry status = %d replayed = %q, body = %s",
			retry.Code, retry.Header().Get("Idempotent-Replayed"), retry.Body.String())
	}
	var retryRun app.ReconciliationRunView
	decode(t, retry, &retryRun)
	if retryRun.ID != firstRun.ID {
		t.Fatalf("retry created run %s, want replay of %s", retryRun.ID, firstRun.ID)
	}

	var runs int
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM reconciliation_runs WHERE file_id = $1`, file.ID).Scan(&runs); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	if runs != 1 {
		t.Fatalf("runs for file = %d, want 1", runs)
	}

	missingKey := client.request(http.MethodPost, "/v1/reconciliation/runs", body, "", true)
	assertProblem(t, missingKey, http.StatusUnprocessableEntity, "validation_failed")

	// A new key for a file whose run is still running is rejected, not queued twice.
	inProgress := client.request(http.MethodPost, "/v1/reconciliation/runs", body, "run-key-2", true)
	assertProblem(t, inProgress, http.StatusConflict, "run_in_progress")

	// After a corrected file is imported, the old one can no longer be reconciled.
	if _, err := recon.Import(ctx, reconciliation.ImportInput{
		Provider: "p", FileName: "f-corrected.csv",
		FileContent: []byte("provider_ref,our_reference,amount,currency,status,occurred_at\n" +
			"prov-1,ref-a,150,IDR,success,2026-10-26T10:00:00Z\n"),
		SettlementDate: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("import corrected file: %v", err)
	}
	superseded := client.request(http.MethodPost, "/v1/reconciliation/runs", body, "run-key-3", true)
	assertProblem(t, superseded, http.StatusConflict, "file_superseded")
}
