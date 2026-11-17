package reconciliation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stevians/wallet-service/internal/ledger"
	"github.com/stevians/wallet-service/internal/testutil"
)

func TestWindowForCutoff(t *testing.T) {
	jakarta, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		loc        *time.Location
		cutoff     time.Duration
		start, end string
	}{
		{"WIB 23:00", jakarta, 23 * time.Hour, "2026-10-25T16:00:00Z", "2026-10-26T16:00:00Z"},
		{"WIB midnight", jakarta, 0, "2026-10-25T17:00:00Z", "2026-10-26T17:00:00Z"},
		{"UTC midnight", time.UTC, 0, "2026-10-26T00:00:00Z", "2026-10-27T00:00:00Z"},
		{"UTC 21:30", time.UTC, 21*time.Hour + 30*time.Minute, "2026-10-25T21:30:00Z", "2026-10-26T21:30:00Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := windowFor(date, tc.loc, tc.cutoff)
			if got := start.UTC().Format(time.RFC3339); got != tc.start {
				t.Fatalf("start = %s, want %s", got, tc.start)
			}
			if got := end.UTC().Format(time.RFC3339); got != tc.end {
				t.Fatalf("end = %s, want %s", got, tc.end)
			}
		})
	}
}

func TestValidateProviderInput(t *testing.T) {
	valid := PutProviderInput{Provider: "bank_a", CutoffTZ: "Asia/Jakarta", CutoffTime: "23:00", Currencies: []string{"IDR"}}
	if err := ValidateProviderInput(valid); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	for name, mutate := range map[string]func(*PutProviderInput){
		"bad name":      func(in *PutProviderInput) { in.Provider = "Bank A" },
		"empty name":    func(in *PutProviderInput) { in.Provider = "" },
		"unknown tz":    func(in *PutProviderInput) { in.CutoffTZ = "Mars/Olympus" },
		"empty tz":      func(in *PutProviderInput) { in.CutoffTZ = "" },
		"bad time":      func(in *PutProviderInput) { in.CutoffTime = "24:00" },
		"seconds":       func(in *PutProviderInput) { in.CutoffTime = "23:00:00" },
		"bad currency":  func(in *PutProviderInput) { in.Currencies = []string{"idr"} },
		"no currencies": func(in *PutProviderInput) { in.Currencies = nil },
	} {
		t.Run(name, func(t *testing.T) {
			in := valid
			in.Currencies = append([]string(nil), valid.Currencies...)
			mutate(&in)
			if err := ValidateProviderInput(in); !errors.Is(err, ErrInvalidProvider) {
				t.Fatalf("err = %v, want ErrInvalidProvider", err)
			}
		})
	}
}

// registerProvider does what the admin API does: upsert the provider and make
// sure it has a bank account per currency, in one transaction.
func registerProvider(t *testing.T, pool *pgxpool.Pool, svc *Service, book *ledger.Ledger, in PutProviderInput) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := svc.UpsertProviderTx(ctx, tx, in); err != nil {
		t.Fatalf("upsert provider: %v", err)
	}
	for _, currency := range in.Currencies {
		if _, _, err := book.EnsureProviderBankAccount(ctx, tx, in.Provider, currency); err != nil {
			t.Fatalf("provider account: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestUpsertProviderUpdatesCutoffAndKeepsAccounts(t *testing.T) {
	pg := testutil.NewPostgres(t)
	svc := NewService(pg.Pool)
	book := ledger.New(pg.Pool)
	ctx := context.Background()

	registerProvider(t, pg.Pool, svc, book, PutProviderInput{Provider: "bank_a", CutoffTZ: "UTC", CutoffTime: "00:00", Currencies: []string{"IDR"}})
	registerProvider(t, pg.Pool, svc, book, PutProviderInput{Provider: "bank_a", CutoffTZ: "Asia/Jakarta", CutoffTime: "23:00", Currencies: []string{"USD"}})

	got, err := svc.GetProvider(ctx, "bank_a")
	if err != nil {
		t.Fatalf("get provider: %v", err)
	}
	if got.CutoffTZ != "Asia/Jakarta" || got.CutoffTime != "23:00" {
		t.Fatalf("cutoff = %s %s, want Asia/Jakarta 23:00", got.CutoffTZ, got.CutoffTime)
	}
	// Currencies are only ever added: IDR stays although the second PUT omitted it.
	if len(got.Accounts) != 2 || got.Accounts[0].Currency != "IDR" || got.Accounts[1].Currency != "USD" {
		t.Fatalf("accounts = %+v, want IDR and USD", got.Accounts)
	}

	if _, err := svc.GetProvider(ctx, "bank_x"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("unknown provider: err = %v, want ErrProviderNotFound", err)
	}
}

func topupVia(t *testing.T, pool *pgxpool.Pool, book *ledger.Ledger, wallet ledger.Account, provider *string, ref string, amount int64) ledger.Transfer {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	transfer, err := book.Topup(ctx, tx, ledger.TopupInput{
		DestinationAccountID: wallet.ID, Amount: amount, Currency: "IDR", Reference: &ref, Provider: provider,
	})
	if err != nil {
		t.Fatalf("topup %s: %v", ref, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return transfer
}

func newWallet(t *testing.T, pool *pgxpool.Pool, book *ledger.Ledger, owner string) ledger.Account {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	account, err := book.CreateAccount(ctx, tx, ledger.CreateAccountInput{OwnerRef: owner, Kind: ledger.AccountKindUserWallet, Currency: "IDR"})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return account
}

func executeRun(t *testing.T, svc *Service, file SettlementFile) ReconciliationRun {
	t.Helper()
	ctx := context.Background()
	run, err := svc.CreateRun(ctx, CreateRunInput{FileID: file.ID})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := svc.Reconciler().Execute(ctx, run); err != nil {
		t.Fatalf("execute: %v", err)
	}
	done, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	return done
}

func discrepancyNotes(t *testing.T, svc *Service, runID ReconciliationRun) []string {
	t.Helper()
	page, err := svc.ListDiscrepancies(context.Background(), ListDiscrepanciesInput{RunID: runID.ID, Limit: 200})
	if err != nil {
		t.Fatalf("list discrepancies: %v", err)
	}
	var out []string
	for _, d := range page.Data {
		out = append(out, string(d.Kind))
	}
	return out
}

// TestRunScopesToProviderAccounts: with two providers settling the same day,
// provider A's run sees only the transfers posted against A's bank account.
// Peer transfers and B's top-ups are not A's business.
func TestRunScopesToProviderAccounts(t *testing.T) {
	pg := testutil.NewPostgres(t)
	svc := NewService(pg.Pool)
	book := ledger.New(pg.Pool)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	date := today.Format(time.DateOnly)

	for _, p := range []string{"bank_a", "bank_b"} {
		registerProvider(t, pg.Pool, svc, book, PutProviderInput{Provider: p, CutoffTZ: "UTC", CutoffTime: "00:00", Currencies: []string{"IDR"}})
	}
	alice := newWallet(t, pg.Pool, book, "alice")
	bob := newWallet(t, pg.Pool, book, "bob")
	bankA, bankB := "bank_a", "bank_b"
	topupVia(t, pg.Pool, book, alice, &bankA, "a-1", 100)
	topupVia(t, pg.Pool, book, alice, &bankB, "b-1", 200)
	topupVia(t, pg.Pool, book, alice, nil, "n-1", 300) // shared system:bank
	peerRef := "p-1"
	tx, err := pg.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := book.Transfer(context.Background(), tx, ledger.TransferInput{
		SourceAccountID: alice.ID, DestinationAccountID: bob.ID, Amount: 50, Currency: "IDR", Reference: &peerRef,
	}); err != nil {
		t.Fatalf("peer transfer: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	fileA := importCSV(t, svc, "bank_a", date, "pa-1,a-1,100,IDR,success,"+today.Add(time.Hour).Format(time.RFC3339))
	run := executeRun(t, svc, fileA)
	if run.MatchedCount != 1 || run.DiscrepancyCount != 0 {
		t.Fatalf("bank_a run matched=%d discrepancies=%d (%v), want 1 and 0",
			run.MatchedCount, run.DiscrepancyCount, discrepancyNotes(t, svc, run))
	}
	if run.ScopeProvider == nil || *run.ScopeProvider != "bank_a" {
		t.Fatalf("scope_provider = %v, want bank_a", run.ScopeProvider)
	}
	if run.ScopeStart == nil || !run.ScopeStart.Equal(today) || run.ScopeEnd == nil || !run.ScopeEnd.Equal(today.Add(24*time.Hour)) {
		t.Fatalf("scope = [%v, %v), want the UTC day %s", run.ScopeStart, run.ScopeEnd, date)
	}

	// An unregistered provider keeps the legacy scope: every referenced
	// transfer of the UTC day, so the other three show up as missing.
	fileC := importCSV(t, svc, "bank_c", date, "pc-1,a-1,100,IDR,success,"+today.Add(time.Hour).Format(time.RFC3339))
	legacy := executeRun(t, svc, fileC)
	if legacy.MatchedCount != 1 || legacy.DiscrepancyCount != 3 {
		t.Fatalf("legacy run matched=%d discrepancies=%d (%v), want 1 and 3",
			legacy.MatchedCount, legacy.DiscrepancyCount, discrepancyNotes(t, svc, legacy))
	}
	if legacy.ScopeProvider != nil {
		t.Fatalf("legacy scope_provider = %q, want nil", *legacy.ScopeProvider)
	}
	if legacy.ScopeStart == nil || !legacy.ScopeStart.Equal(today) {
		t.Fatalf("legacy scope_start = %v, want %v", legacy.ScopeStart, today)
	}
}

// TestRunScopeFollowsProviderCutoff: a provider closing its day at 23:00 WIB
// owns the transfers from 23:00 WIB the day before up to 23:00 WIB on the
// settlement date, not the UTC calendar day.
func TestRunScopeFollowsProviderCutoff(t *testing.T) {
	pg := testutil.NewPostgres(t)
	svc := NewService(pg.Pool)
	book := ledger.New(pg.Pool)
	ctx := context.Background()

	registerProvider(t, pg.Pool, svc, book, PutProviderInput{Provider: "bank_wib", CutoffTZ: "Asia/Jakarta", CutoffTime: "23:00", Currencies: []string{"IDR"}})
	wallet := newWallet(t, pg.Pool, book, "alice")
	provider := "bank_wib"

	// created_at is set by the database; the app role cannot change it, so the
	// test backdates the rows through the schema owner.
	owner, err := pgxpool.New(ctx, strings.Replace(pg.MigratorURL, "pgx5://", "postgres://", 1))
	if err != nil {
		t.Fatalf("owner pool: %v", err)
	}
	defer owner.Close()
	at := map[string]string{
		"before-window": "2026-10-25T15:30:00Z", // 25 Oct 22:30 WIB: previous business day
		"window-start":  "2026-10-25T16:30:00Z", // 25 Oct 23:30 WIB: after the previous cut-off
		"window-late":   "2026-10-26T15:30:00Z", // 26 Oct 22:30 WIB
		"after-cutoff":  "2026-10-26T16:30:00Z", // 26 Oct 23:30 WIB: next business day
	}
	for ref, ts := range at {
		transfer := topupVia(t, pg.Pool, book, wallet, &provider, ref, 100)
		if _, err := owner.Exec(ctx, `UPDATE transfers SET created_at = $2 WHERE id = $1`, transfer.ID, ts); err != nil {
			t.Fatalf("backdate %s: %v", ref, err)
		}
	}

	file := importCSV(t, svc, "bank_wib", "2026-10-26",
		"p-1,window-start,100,IDR,success,2026-10-25T16:30:00Z",
		"p-2,window-late,100,IDR,success,2026-10-26T15:30:00Z",
	)
	run := executeRun(t, svc, file)
	if run.MatchedCount != 2 || run.DiscrepancyCount != 0 {
		t.Fatalf("matched=%d discrepancies=%d (%v), want 2 and 0",
			run.MatchedCount, run.DiscrepancyCount, discrepancyNotes(t, svc, run))
	}
	wantStart := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 10, 26, 16, 0, 0, 0, time.UTC)
	if run.ScopeStart == nil || !run.ScopeStart.Equal(wantStart) || run.ScopeEnd == nil || !run.ScopeEnd.Equal(wantEnd) {
		t.Fatalf("scope = [%v, %v), want [%v, %v)", run.ScopeStart, run.ScopeEnd, wantStart, wantEnd)
	}
}
