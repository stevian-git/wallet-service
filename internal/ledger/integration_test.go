package ledger

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stevians/wallet-service/internal/testutil"
)

func TestLedgerCoreHappyPathAndReversal(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	accountA := createAccount(t, pg.Pool, book, "owner-a")
	accountB := createAccount(t, pg.Pool, book, "owner-b")

	topup := postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Topup(ctx, tx, TopupInput{
			DestinationAccountID: accountA.ID,
			Amount:               100_000,
			Currency:             "IDR",
			Reference:            ptr("topup-1"),
		})
	})
	if topup.Type != TransferTypeTopup || topup.Amount != 100_000 {
		t.Fatalf("unexpected topup: %+v", topup)
	}

	assertBalance(t, book, ctx, accountA.ID, 100_000, 0)
	assertBalance(t, book, ctx, accountB.ID, 0, 0)

	transfer := postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Transfer(ctx, tx, TransferInput{
			SourceAccountID:      accountA.ID,
			DestinationAccountID: accountB.ID,
			Amount:               25_000,
			Currency:             "IDR",
			Reference:            ptr("transfer-1"),
		})
	})
	assertBalance(t, book, ctx, accountA.ID, 75_000, 0)
	assertBalance(t, book, ctx, accountB.ID, 25_000, 0)

	detail, err := book.GetTransfer(ctx, transfer.ID)
	if err != nil {
		t.Fatalf("get transfer: %v", err)
	}
	if len(detail.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(detail.Entries))
	}

	reversal := postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Reverse(ctx, tx, transfer.ID, "customer request")
	})
	if reversal.Type != TransferTypeReversal {
		t.Fatalf("expected reversal transfer, got %s", reversal.Type)
	}
	if reversal.ReversesTransferID == nil || *reversal.ReversesTransferID != transfer.ID {
		t.Fatalf("reversal does not point at original transfer: %+v", reversal)
	}
	assertBalance(t, book, ctx, accountA.ID, 100_000, 0)
	assertBalance(t, book, ctx, accountB.ID, 0, 0)

	withdrawal := postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Withdrawal(ctx, tx, WithdrawalInput{
			SourceAccountID: accountA.ID,
			Amount:          5_000,
			Currency:        "IDR",
		})
	})
	if withdrawal.Type != TransferTypeWithdrawal || withdrawal.Amount != 5_000 {
		t.Fatalf("unexpected withdrawal: %+v", withdrawal)
	}
	assertBalance(t, book, ctx, accountA.ID, 95_000, 0)

	assertOutboxEvents(t, pg.Pool, ctx, []string{
		"account.created",
		"account.created",
		"transfer.posted",
		"transfer.posted",
		"transfer.reversed",
		"transfer.posted",
	})
	assertNoDrift(t, pg.Pool, ctx)
}

func TestInsufficientFundsLeavesNoPartialWrites(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	accountA := createAccount(t, pg.Pool, book, "owner-a")
	accountB := createAccount(t, pg.Pool, book, "owner-b")

	beforeTransfers := countRows(t, pg.Pool, "transfers")
	beforeEntries := countRows(t, pg.Pool, "ledger_entries")
	beforeOutbox := countRows(t, pg.Pool, "outbox_events")

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	_, err = book.Transfer(ctx, tx, TransferInput{
		SourceAccountID:      accountA.ID,
		DestinationAccountID: accountB.ID,
		Amount:               100,
		Currency:             "IDR",
	})
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	assertBalance(t, book, ctx, accountA.ID, 0, 0)
	assertBalance(t, book, ctx, accountB.ID, 0, 0)
	if got := countRows(t, pg.Pool, "transfers"); got != beforeTransfers {
		t.Fatalf("expected no new transfers, got %d (before %d)", got, beforeTransfers)
	}
	if got := countRows(t, pg.Pool, "ledger_entries"); got != beforeEntries {
		t.Fatalf("expected no new entries, got %d (before %d)", got, beforeEntries)
	}
	if got := countRows(t, pg.Pool, "outbox_events"); got != beforeOutbox {
		t.Fatalf("expected no new outbox events, got %d (before %d)", got, beforeOutbox)
	}
	assertNoDrift(t, pg.Pool, ctx)
}

func createAccount(t *testing.T, pool *pgxpool.Pool, book *Ledger, owner string) Account {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(context.Background())

	account, err := book.CreateAccount(context.Background(), tx, CreateAccountInput{
		OwnerRef: owner,
		Kind:     AccountKindUserWallet,
		Currency: "IDR",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return account
}

func postInTx(t *testing.T, pool *pgxpool.Pool, fn func(tx pgx.Tx) (Transfer, error)) Transfer {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	transfer, err := fn(tx)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return transfer
}

func assertBalance(t *testing.T, book *Ledger, ctx context.Context, accountID uuid.UUID, posted, held int64) {
	t.Helper()
	balance, err := book.GetBalance(ctx, accountID)
	if err != nil {
		t.Fatalf("get balance: %v", err)
	}
	if balance.Posted != posted || balance.Held != held || balance.Available != posted-held {
		t.Fatalf("unexpected balance for %s: %+v", accountID, balance)
	}
}

func assertNoDrift(t *testing.T, pool *pgxpool.Pool, ctx context.Context) {
	t.Helper()
	counts := []string{
		"SELECT count(*) FROM v_balance_drift",
		"SELECT count(*) FROM v_unbalanced_transfers",
		"SELECT count(*) FROM v_posted_transfers_without_entries",
	}
	for _, query := range counts {
		var count int
		if err := pool.QueryRow(ctx, query).Scan(&count); err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		if count != 0 {
			t.Fatalf("integrity view %q returned %d rows", query, count)
		}
	}
}

func assertOutboxEvents(t *testing.T, pool *pgxpool.Pool, ctx context.Context, want []string) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT event_type FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatalf("query outbox events: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatalf("scan outbox event: %v", err)
		}
		got = append(got, eventType)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate outbox events: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("outbox event types = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("outbox event types = %v, want %v", got, want)
		}
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func ptr[T any](value T) *T {
	return &value
}

func TestConcurrentTransfersDoNotOverdraw(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	accountA := createAccount(t, pg.Pool, book, "owner-a")
	accountB := createAccount(t, pg.Pool, book, "owner-b")

	postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Topup(ctx, tx, TopupInput{
			DestinationAccountID: accountA.ID,
			Amount:               1_000,
			Currency:             "IDR",
		})
	})

	const attempts = 20
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		rejected  int
		failures  []error
	)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			tx, err := pg.Pool.Begin(ctx)
			if err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
				return
			}
			defer tx.Rollback(ctx)

			_, err = book.Transfer(ctx, tx, TransferInput{
				SourceAccountID:      accountA.ID,
				DestinationAccountID: accountB.ID,
				Amount:               100,
				Currency:             "IDR",
			})
			if err != nil {
				if errors.Is(err, ErrInsufficientFunds) {
					mu.Lock()
					rejected++
					mu.Unlock()
					return
				}
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
				return
			}
			if err := tx.Commit(ctx); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
				return
			}

			mu.Lock()
			succeeded++
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("unexpected concurrent transfer failures: %v", failures)
	}
	if succeeded != 10 || rejected != 10 {
		t.Fatalf("succeeded = %d, rejected = %d; want 10 and 10", succeeded, rejected)
	}
	assertBalance(t, book, ctx, accountA.ID, 0, 0)
	assertBalance(t, book, ctx, accountB.ID, 1_000, 0)
	assertNoDrift(t, pg.Pool, ctx)

	if got := countRows(t, pg.Pool, "outbox_events"); got != 13 {
		t.Fatalf("expected 13 outbox events (2 accounts + 1 topup + 10 transfers), got %d", got)
	}
}

func TestReversalCannotBeReversedTwiceAndLedgerIsAppendOnly(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	accountA := createAccount(t, pg.Pool, book, "reversal-owner-a")
	accountB := createAccount(t, pg.Pool, book, "reversal-owner-b")
	postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Topup(ctx, tx, TopupInput{
			DestinationAccountID: accountA.ID,
			Amount:               10_000,
			Currency:             "IDR",
		})
	})
	transfer := postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Transfer(ctx, tx, TransferInput{
			SourceAccountID:      accountA.ID,
			DestinationAccountID: accountB.ID,
			Amount:               4_000,
			Currency:             "IDR",
		})
	})
	reversal := postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Reverse(ctx, tx, transfer.ID, "test")
	})

	entriesBefore := countRows(t, pg.Pool, "ledger_entries")
	transfersBefore := countRows(t, pg.Pool, "transfers")
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	_, err = book.Reverse(ctx, tx, transfer.ID, "again")
	_ = tx.Rollback(ctx)
	if !errors.Is(err, ErrAlreadyReversed) {
		t.Fatalf("second reversal error = %v, want ErrAlreadyReversed", err)
	}
	if got := countRows(t, pg.Pool, "ledger_entries"); got != entriesBefore {
		t.Fatalf("entries after rejected reversal = %d, want %d", got, entriesBefore)
	}
	if got := countRows(t, pg.Pool, "transfers"); got != transfersBefore {
		t.Fatalf("transfers after rejected reversal = %d, want %d", got, transfersBefore)
	}

	if _, err := pg.Pool.Exec(ctx, `UPDATE ledger_entries SET amount = amount + 1 WHERE transfer_id = $1`, transfer.ID.String()); err == nil {
		t.Fatal("app role was able to update append-only ledger_entries")
	}
	if _, err := pg.Pool.Exec(ctx, `DELETE FROM ledger_entries WHERE transfer_id = $1`, transfer.ID.String()); err == nil {
		t.Fatal("app role was able to delete append-only ledger_entries")
	}

	detail, err := book.GetTransfer(ctx, reversal.ID)
	if err != nil {
		t.Fatalf("get reversal: %v", err)
	}
	if len(detail.Entries) != 2 || detail.ReversesTransferID == nil || *detail.ReversesTransferID != transfer.ID {
		t.Fatalf("unexpected reversal detail: %+v", detail)
	}
	assertBalance(t, book, ctx, accountA.ID, 10_000, 0)
	assertBalance(t, book, ctx, accountB.ID, 0, 0)
	assertOutboxEvents(t, pg.Pool, ctx, []string{
		"account.created",
		"account.created",
		"transfer.posted",
		"transfer.posted",
		"transfer.reversed",
	})
	assertNoDrift(t, pg.Pool, ctx)
}
