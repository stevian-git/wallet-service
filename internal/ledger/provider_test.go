package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stevians/wallet-service/internal/testutil"
)

// insertProvider registers a provider row the way the reconciliation package
// does; the ledger only owns the provider's bank accounts.
func insertProvider(t *testing.T, pool *pgxpool.Pool, provider string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO settlement_providers (provider, cutoff_tz, cutoff_time)
		VALUES ($1, 'UTC', '00:00')`, provider); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
}

func ensureProviderAccount(t *testing.T, pool *pgxpool.Pool, book *Ledger, provider, currency string) (Account, bool) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	account, created, err := book.EnsureProviderBankAccount(ctx, tx, provider, currency)
	if err != nil {
		t.Fatalf("ensure provider account: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return account, created
}

func TestProviderBankAccountIsCreatedOnce(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	insertProvider(t, pg.Pool, "bank_a")

	first, created := ensureProviderAccount(t, pg.Pool, book, "bank_a", "IDR")
	if !created {
		t.Fatal("first call did not create the account")
	}
	if first.Kind != AccountKindSystemBank || first.NormalSide != NormalSideDebit || !first.AllowNegative {
		t.Fatalf("provider account = %+v, want debit-normal system_bank that may go negative", first)
	}
	if first.OwnerRef == nil || *first.OwnerRef != "provider:bank_a" {
		t.Fatalf("owner_ref = %v, want provider:bank_a", first.OwnerRef)
	}

	again, created := ensureProviderAccount(t, pg.Pool, book, "bank_a", "IDR")
	if created || again.ID != first.ID {
		t.Fatalf("second call: created=%v id=%s, want the existing account %s", created, again.ID, first.ID)
	}
	assertOutboxEvents(t, pg.Pool, context.Background(), []string{"account.created"})
}

func TestTopupAndWithdrawalPostAgainstProviderAccount(t *testing.T) {
	pg := testutil.NewPostgres(t)
	ctx := context.Background()
	book := New(pg.Pool)
	insertProvider(t, pg.Pool, "bank_a")
	providerAccount, _ := ensureProviderAccount(t, pg.Pool, book, "bank_a", "IDR")
	wallet := createAccount(t, pg.Pool, book, "alice")

	topup := postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Topup(ctx, tx, TopupInput{
			DestinationAccountID: wallet.ID, Amount: 1000, Currency: "IDR",
			Reference: ptr("topup-a"), Provider: ptr("bank_a"),
		})
	})
	if topup.SourceAccountID == nil || *topup.SourceAccountID != providerAccount.ID {
		t.Fatalf("topup source = %v, want provider account %s", topup.SourceAccountID, providerAccount.ID)
	}

	withdrawal := postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Withdrawal(ctx, tx, WithdrawalInput{
			SourceAccountID: wallet.ID, Amount: 400, Currency: "IDR",
			Reference: ptr("withdraw-a"), Provider: ptr("bank_a"),
		})
	})
	if withdrawal.DestinationAccountID == nil || *withdrawal.DestinationAccountID != providerAccount.ID {
		t.Fatalf("withdrawal destination = %v, want provider account %s", withdrawal.DestinationAccountID, providerAccount.ID)
	}

	// Without a provider the shared system:bank account is used, as before.
	plain := postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Topup(ctx, tx, TopupInput{DestinationAccountID: wallet.ID, Amount: 10, Currency: "IDR"})
	})
	if plain.SourceAccountID == nil || *plain.SourceAccountID == providerAccount.ID {
		t.Fatalf("topup without provider used %v, want the system:bank account", plain.SourceAccountID)
	}

	assertBalance(t, book, ctx, wallet.ID, 610, 0)
	assertNoDrift(t, pg.Pool, ctx)
}

func TestTopupWithUnknownProviderIsRejected(t *testing.T) {
	pg := testutil.NewPostgres(t)
	ctx := context.Background()
	book := New(pg.Pool)
	insertProvider(t, pg.Pool, "bank_a") // registered, but without a USD account
	ensureProviderAccount(t, pg.Pool, book, "bank_a", "IDR")
	wallet := createAccount(t, pg.Pool, book, "alice")

	for _, tc := range []struct{ provider, currency string }{
		{"bank_x", "IDR"}, // not registered
		{"bank_a", "USD"}, // registered, currency not linked
	} {
		tx, err := pg.Pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		_, err = book.Topup(ctx, tx, TopupInput{
			DestinationAccountID: wallet.ID, Amount: 10, Currency: tc.currency, Provider: ptr(tc.provider),
		})
		_ = tx.Rollback(ctx)
		if !errors.Is(err, ErrUnknownProvider) {
			t.Fatalf("topup via %s/%s: err = %v, want ErrUnknownProvider", tc.provider, tc.currency, err)
		}
	}
}
