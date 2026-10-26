package ledger

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stevians/wallet-service/internal/testutil"
)

func TestHoldCapturePartialAndRelease(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	accountA := createAccount(t, pg.Pool, book, "hold-owner-a")
	accountB := createAccount(t, pg.Pool, book, "hold-owner-b")
	postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Topup(ctx, tx, TopupInput{
			DestinationAccountID: accountA.ID,
			Amount:               100_000,
			Currency:             "IDR",
		})
	})

	hold := createHoldInTx(t, pg.Pool, book, CreateHoldInput{
		AccountID:  accountA.ID,
		Amount:     30_000,
		Currency:   "IDR",
		TTLSeconds: 60,
		Reference:  ptr("order-1"),
	})
	assertBalance(t, book, ctx, accountA.ID, 100_000, 30_000)
	assertBalance(t, book, ctx, accountB.ID, 0, 0)

	capture := captureHoldInTx(t, pg.Pool, book, CaptureHoldInput{
		HoldID:               hold.ID,
		DestinationAccountID: accountB.ID,
		Amount:               20_000,
	})
	if capture.Type != TransferTypeHoldCapture || capture.Amount != 20_000 {
		t.Fatalf("unexpected capture transfer: %+v", capture)
	}
	assertBalance(t, book, ctx, accountA.ID, 80_000, 0)
	assertBalance(t, book, ctx, accountB.ID, 20_000, 0)

	stored, err := book.GetHold(ctx, hold.ID)
	if err != nil {
		t.Fatalf("get hold: %v", err)
	}
	if stored.Status != HoldStatusCaptured {
		t.Fatalf("hold status = %s, want captured", stored.Status)
	}
	if stored.TransferID == nil || *stored.TransferID != capture.ID {
		t.Fatalf("hold transfer_id = %v, want %s", stored.TransferID, capture.ID)
	}
	if stored.ResolvedAt == nil {
		t.Fatal("captured hold has no resolved_at")
	}

	if _, err := captureHoldInTxErr(pg.Pool, book, CaptureHoldInput{
		HoldID:               hold.ID,
		DestinationAccountID: accountB.ID,
	}); !errors.Is(err, ErrHoldNotActive) {
		t.Fatalf("second capture error = %v, want ErrHoldNotActive", err)
	}
	if _, err := releaseHoldInTxErr(pg.Pool, book, hold.ID); !errors.Is(err, ErrHoldNotActive) {
		t.Fatalf("release captured hold error = %v, want ErrHoldNotActive", err)
	}

	assertOutboxEvents(t, pg.Pool, ctx, []string{
		"account.created",
		"account.created",
		"transfer.posted",
		"hold.created",
		"transfer.posted",
		"hold.captured",
	})
	assertNoDrift(t, pg.Pool, ctx)
}

func TestHoldReleaseAndExpiry(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	account := createAccount(t, pg.Pool, book, "hold-owner-expiry")
	postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Topup(ctx, tx, TopupInput{
			DestinationAccountID: account.ID,
			Amount:               50_000,
			Currency:             "IDR",
		})
	})

	released := createHoldInTx(t, pg.Pool, book, CreateHoldInput{
		AccountID:  account.ID,
		Amount:     10_000,
		Currency:   "IDR",
		TTLSeconds: 60,
	})
	released, err := releaseHoldInTx(pg.Pool, book, released.ID)
	if err != nil {
		t.Fatalf("release hold: %v", err)
	}
	if released.Status != HoldStatusReleased || released.ResolvedAt == nil {
		t.Fatalf("unexpected released hold: %+v", released)
	}
	assertBalance(t, book, ctx, account.ID, 50_000, 0)

	expiring := createHoldInTx(t, pg.Pool, book, CreateHoldInput{
		AccountID:  account.ID,
		Amount:     10_000,
		Currency:   "IDR",
		TTLSeconds: 1,
	})
	assertBalance(t, book, ctx, account.ID, 50_000, 10_000)
	time.Sleep(1100 * time.Millisecond)

	expired, ok, err := book.ExpireOne(ctx)
	if err != nil {
		t.Fatalf("expire one: %v", err)
	}
	if !ok {
		t.Fatal("expected one expired hold")
	}
	if expired.ID != expiring.ID || expired.Status != HoldStatusExpired {
		t.Fatalf("unexpected expired hold: %+v", expired)
	}
	assertBalance(t, book, ctx, account.ID, 50_000, 0)

	if _, ok, err := book.ExpireOne(ctx); err != nil || ok {
		t.Fatalf("second expire one = ok:%v err:%v, want false nil", ok, err)
	}

	assertOutboxEvents(t, pg.Pool, ctx, []string{
		"account.created",
		"transfer.posted",
		"hold.created",
		"hold.released",
		"hold.created",
		"hold.expired",
	})
	assertNoDrift(t, pg.Pool, ctx)
}

func TestHoldCaptureAfterExpiryIsRejected(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	accountA := createAccount(t, pg.Pool, book, "hold-expired-a")
	accountB := createAccount(t, pg.Pool, book, "hold-expired-b")
	postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Topup(ctx, tx, TopupInput{
			DestinationAccountID: accountA.ID,
			Amount:               10_000,
			Currency:             "IDR",
		})
	})

	hold := createHoldInTx(t, pg.Pool, book, CreateHoldInput{
		AccountID:  accountA.ID,
		Amount:     5_000,
		Currency:   "IDR",
		TTLSeconds: 1,
	})
	time.Sleep(1100 * time.Millisecond)

	if _, err := captureHoldInTxErr(pg.Pool, book, CaptureHoldInput{
		HoldID:               hold.ID,
		DestinationAccountID: accountB.ID,
	}); !errors.Is(err, ErrHoldExpired) {
		t.Fatalf("capture expired hold error = %v, want ErrHoldExpired", err)
	}
	if _, err := releaseHoldInTx(pg.Pool, book, hold.ID); err != nil {
		t.Fatalf("release expired active hold: %v", err)
	}
	assertBalance(t, book, ctx, accountA.ID, 10_000, 0)
}

func TestHoldRejectsOverAvailableAndOverCapture(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	accountA := createAccount(t, pg.Pool, book, "hold-limit-a")
	accountB := createAccount(t, pg.Pool, book, "hold-limit-b")
	postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Topup(ctx, tx, TopupInput{
			DestinationAccountID: accountA.ID,
			Amount:               10_000,
			Currency:             "IDR",
		})
	})

	hold := createHoldInTx(t, pg.Pool, book, CreateHoldInput{
		AccountID:  accountA.ID,
		Amount:     8_000,
		Currency:   "IDR",
		TTLSeconds: 60,
	})
	if _, err := createHoldInTxErr(pg.Pool, book, CreateHoldInput{
		AccountID:  accountA.ID,
		Amount:     3_000,
		Currency:   "IDR",
		TTLSeconds: 60,
	}); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("over-available hold error = %v, want ErrInsufficientFunds", err)
	}
	if _, err := captureHoldInTxErr(pg.Pool, book, CaptureHoldInput{
		HoldID:               hold.ID,
		DestinationAccountID: accountB.ID,
		Amount:               8_001,
	}); !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("over-capture error = %v, want ErrInvalidPosting", err)
	}
	assertBalance(t, book, ctx, accountA.ID, 10_000, 8_000)
	assertNoDrift(t, pg.Pool, ctx)
}

func TestConcurrentHoldsDoNotOverdraw(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	account := createAccount(t, pg.Pool, book, "hold-concurrent")
	postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
		return book.Topup(ctx, tx, TopupInput{
			DestinationAccountID: account.ID,
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

			_, err = book.CreateHold(ctx, tx, CreateHoldInput{
				AccountID:  account.ID,
				Amount:     100,
				Currency:   "IDR",
				TTLSeconds: 60,
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
		t.Fatalf("unexpected concurrent hold failures: %v", failures)
	}
	if succeeded != 10 || rejected != 10 {
		t.Fatalf("succeeded = %d, rejected = %d; want 10 and 10", succeeded, rejected)
	}
	assertBalance(t, book, ctx, account.ID, 1_000, 1_000)
	assertNoDrift(t, pg.Pool, ctx)
}

func createHoldInTx(t *testing.T, pool *pgxpool.Pool, book *Ledger, input CreateHoldInput) Hold {
	t.Helper()
	hold, err := createHoldInTxErr(pool, book, input)
	if err != nil {
		t.Fatalf("create hold: %v", err)
	}
	return hold
}

func createHoldInTxErr(pool *pgxpool.Pool, book *Ledger, input CreateHoldInput) (Hold, error) {
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Hold{}, err
	}
	defer tx.Rollback(ctx)

	hold, err := book.CreateHold(ctx, tx, input)
	if err != nil {
		return Hold{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Hold{}, err
	}
	return hold, nil
}

func captureHoldInTx(t *testing.T, pool *pgxpool.Pool, book *Ledger, input CaptureHoldInput) Transfer {
	t.Helper()
	transfer, err := captureHoldInTxErr(pool, book, input)
	if err != nil {
		t.Fatalf("capture hold: %v", err)
	}
	return transfer
}

func captureHoldInTxErr(pool *pgxpool.Pool, book *Ledger, input CaptureHoldInput) (Transfer, error) {
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Transfer{}, err
	}
	defer tx.Rollback(ctx)

	transfer, err := book.CaptureHold(ctx, tx, input)
	if err != nil {
		return Transfer{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Transfer{}, err
	}
	return transfer, nil
}

func releaseHoldInTx(pool *pgxpool.Pool, book *Ledger, holdID uuid.UUID) (Hold, error) {
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Hold{}, err
	}
	defer tx.Rollback(ctx)

	hold, err := book.ReleaseHold(ctx, tx, holdID)
	if err != nil {
		return Hold{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Hold{}, err
	}
	return hold, nil
}

func releaseHoldInTxErr(pool *pgxpool.Pool, book *Ledger, holdID uuid.UUID) (Hold, error) {
	return releaseHoldInTx(pool, book, holdID)
}

func TestConcurrentCaptureAndReleaseResolveHoldOnce(t *testing.T) {
	pg := testutil.NewPostgres(t)
	book := New(pg.Pool)
	ctx := context.Background()

	accountA := createAccount(t, pg.Pool, book, "hold-race-a")
	accountB := createAccount(t, pg.Pool, book, "hold-race-b")

	for i := 0; i < 5; i++ {
		postInTx(t, pg.Pool, func(tx pgx.Tx) (Transfer, error) {
			return book.Topup(ctx, tx, TopupInput{
				DestinationAccountID: accountA.ID,
				Amount:               10_000,
				Currency:             "IDR",
			})
		})
		hold := createHoldInTx(t, pg.Pool, book, CreateHoldInput{
			AccountID:  accountA.ID,
			Amount:     1_000,
			Currency:   "IDR",
			TTLSeconds: 60,
		})

		start := make(chan struct{})
		var (
			wg         sync.WaitGroup
			captureErr error
			releaseErr error
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, captureErr = captureHoldInTxErr(pg.Pool, book, CaptureHoldInput{
				HoldID:               hold.ID,
				DestinationAccountID: accountB.ID,
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, releaseErr = releaseHoldInTx(pg.Pool, book, hold.ID)
		}()
		close(start)
		wg.Wait()

		if (captureErr == nil) == (releaseErr == nil) {
			t.Fatalf("iteration %d captureErr=%v releaseErr=%v; want exactly one success", i, captureErr, releaseErr)
		}
		if captureErr != nil && !errors.Is(captureErr, ErrHoldNotActive) {
			t.Fatalf("iteration %d capture error = %v", i, captureErr)
		}
		if releaseErr != nil && !errors.Is(releaseErr, ErrHoldNotActive) {
			t.Fatalf("iteration %d release error = %v", i, releaseErr)
		}

		var resolvedEvents int
		err := pg.Pool.QueryRow(ctx, `
			SELECT count(*)
			  FROM outbox_events
			 WHERE aggregate_type = 'hold'
			   AND aggregate_id = $1
			   AND event_type IN ('hold.captured', 'hold.released')`,
			hold.ID.String(),
		).Scan(&resolvedEvents)
		if err != nil {
			t.Fatalf("count resolved hold events: %v", err)
		}
		if resolvedEvents != 1 {
			t.Fatalf("iteration %d resolved events = %d, want 1", i, resolvedEvents)
		}
	}

	balance, err := book.GetBalance(ctx, accountA.ID)
	if err != nil {
		t.Fatalf("get final balance: %v", err)
	}
	if balance.Held != 0 || balance.Available != balance.Posted {
		t.Fatalf("final balance = %+v, want no held funds", balance)
	}
	if balance.Posted < 45_000 || balance.Posted > 50_000 {
		t.Fatalf("final posted = %d, want between 45000 and 50000", balance.Posted)
	}
	assertNoDrift(t, pg.Pool, ctx)
}
