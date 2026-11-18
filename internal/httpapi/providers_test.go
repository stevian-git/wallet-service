package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/stevians/wallet-service/internal/app"
	"github.com/stevians/wallet-service/internal/idempotency"
	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/reconciliation"
	"github.com/stevians/wallet-service/internal/testutil"
)

func TestSettlementProviderRegistrationAndTopup(t *testing.T) {
	pg := testutil.NewPostgres(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := app.New(ledger.New(pg.Pool), idempotency.New(pg.Pool, 5*time.Second), reconciliation.NewService(pg.Pool))
	client := &apiClient{t: t, handler: New(service, logger, testAPIKey, nil, pg.Pool.Ping).Handler()}

	put := func(provider string, body map[string]any) app.SettlementProviderView {
		t.Helper()
		res := client.request(http.MethodPut, "/v1/admin/settlement-providers/"+provider, body, "", true)
		if res.Code != http.StatusOK {
			t.Fatalf("PUT %s: status = %d, body = %s", provider, res.Code, res.Body.String())
		}
		var view app.SettlementProviderView
		decode(t, res, &view)
		return view
	}

	first := put("bank_a", map[string]any{"cutoff_tz": "Asia/Jakarta", "cutoff_time": "23:00", "currencies": []string{"IDR"}})
	if first.Provider != "bank_a" || first.CutoffTZ != "Asia/Jakarta" || first.CutoffTime != "23:00" || len(first.Accounts) != 1 {
		t.Fatalf("first PUT = %+v", first)
	}
	// Repeating the PUT changes nothing; a new currency is added next to IDR.
	again := put("bank_a", map[string]any{"cutoff_tz": "Asia/Jakarta", "cutoff_time": "23:00", "currencies": []string{"IDR"}})
	if len(again.Accounts) != 1 || again.Accounts[0].AccountID != first.Accounts[0].AccountID {
		t.Fatalf("repeated PUT accounts = %+v, want %+v", again.Accounts, first.Accounts)
	}
	more := put("bank_a", map[string]any{"cutoff_tz": "UTC", "cutoff_time": "00:00", "currencies": []string{"USD"}})
	if len(more.Accounts) != 2 || more.CutoffTZ != "UTC" {
		t.Fatalf("PUT with USD = %+v, want IDR+USD and UTC", more)
	}

	list := client.request(http.MethodGet, "/v1/admin/settlement-providers", nil, "", true)
	var page app.SettlementProvidersView
	decode(t, list, &page)
	if len(page.Data) != 1 || page.Data[0].Provider != "bank_a" {
		t.Fatalf("list = %+v", page)
	}

	for name, body := range map[string]map[string]any{
		"bad tz":      {"cutoff_tz": "Mars/Olympus", "cutoff_time": "23:00", "currencies": []string{"IDR"}},
		"bad time":    {"cutoff_tz": "UTC", "cutoff_time": "7pm", "currencies": []string{"IDR"}},
		"no currency": {"cutoff_tz": "UTC", "cutoff_time": "23:00"},
		"unknown key": {"cutoff_tz": "UTC", "cutoff_time": "23:00", "currencies": []string{"IDR"}, "x": 1},
	} {
		res := client.request(http.MethodPut, "/v1/admin/settlement-providers/bank_a", body, "", true)
		t.Run(name, func(t *testing.T) { assertProblem(t, res, http.StatusUnprocessableEntity, "validation_failed") })
	}
	badName := client.request(http.MethodPut, "/v1/admin/settlement-providers/Bank-A", map[string]any{"cutoff_tz": "UTC", "cutoff_time": "23:00", "currencies": []string{"IDR"}}, "", true)
	assertProblem(t, badName, http.StatusUnprocessableEntity, "validation_failed")

	// A top-up through the provider posts against its IDR account.
	account := client.request(http.MethodPost, "/v1/accounts", map[string]any{"owner_ref": "alice", "currency": "IDR"}, "acct-1", true)
	var wallet struct {
		ID uuid.UUID `json:"id"`
	}
	decode(t, account, &wallet)
	topup := client.request(http.MethodPost, "/v1/topups", map[string]any{
		"destination_account_id": wallet.ID, "amount": 1000, "currency": "IDR", "reference": "t-1", "provider": "bank_a",
	}, "topup-1", true)
	if topup.Code != http.StatusCreated {
		t.Fatalf("topup status = %d, body = %s", topup.Code, topup.Body.String())
	}
	var transfer struct {
		SourceAccountID uuid.UUID `json:"source_account_id"`
	}
	decode(t, topup, &transfer)
	if transfer.SourceAccountID != first.Accounts[0].AccountID {
		t.Fatalf("topup source = %s, want provider account %s", transfer.SourceAccountID, first.Accounts[0].AccountID)
	}

	unknown := client.request(http.MethodPost, "/v1/topups", map[string]any{
		"destination_account_id": wallet.ID, "amount": 1000, "currency": "IDR", "provider": "bank_x",
	}, "topup-2", true)
	assertProblem(t, unknown, http.StatusUnprocessableEntity, "unknown_provider")
	withdrawal := client.request(http.MethodPost, "/v1/withdrawals", map[string]any{
		"source_account_id": wallet.ID, "amount": 10, "currency": "IDR", "provider": "bank_x",
	}, "withdraw-1", true)
	assertProblem(t, withdrawal, http.StatusUnprocessableEntity, "unknown_provider")
}
