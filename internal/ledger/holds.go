package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stevians/wallet-service/internal/platform/telemetry"
)

const (
	minHoldTTLSeconds = 1
	maxHoldTTLSeconds = 7 * 24 * 60 * 60
)

func (h Hold) requireActive() error {
	if h.Status != HoldStatusActive {
		return fmt.Errorf("%w: hold status is %s", ErrHoldNotActive, h.Status)
	}
	return nil
}

func (l *Ledger) CreateHold(ctx context.Context, tx pgx.Tx, input CreateHoldInput) (Hold, error) {
	if tx == nil {
		return Hold{}, ErrNoTransaction
	}
	if input.AccountID == uuid.Nil {
		return Hold{}, fmt.Errorf("%w: account_id is required", ErrInvalidPosting)
	}
	if err := validateAmountAndCurrency(input.Amount, input.Currency); err != nil {
		return Hold{}, err
	}
	if input.TTLSeconds < minHoldTTLSeconds || input.TTLSeconds > maxHoldTTLSeconds {
		return Hold{}, fmt.Errorf("%w: ttl_seconds must be between %d and %d", ErrInvalidPosting, minHoldTTLSeconds, maxHoldTTLSeconds)
	}
	if input.Reference != nil && len(*input.Reference) > 255 {
		return Hold{}, fmt.Errorf("%w: reference must be at most 255 characters", ErrInvalidPosting)
	}

	lockStart := time.Now()
	accounts, err := lockAccounts(ctx, tx, []uuid.UUID{input.AccountID})
	telemetry.ObserveLockWait("hold_create", time.Since(lockStart))
	if err != nil {
		return Hold{}, err
	}
	account := accounts[input.AccountID]
	if account.Currency != input.Currency {
		return Hold{}, ErrCurrencyMismatch
	}
	if err := account.requireActive(input.AccountID); err != nil {
		return Hold{}, err
	}
	available, ok := addInt64(account.Posted, -account.Held)
	if !ok {
		return Hold{}, ErrInvalidAmount
	}
	if !account.AllowNegative && available < input.Amount {
		return Hold{}, ErrInsufficientFunds
	}
	if _, ok := addInt64(account.Held, input.Amount); !ok {
		return Hold{}, ErrInvalidAmount
	}

	if err := changeHeld(ctx, tx, input.AccountID, input.Amount); err != nil {
		return Hold{}, err
	}

	var (
		id     pgtype.UUID
		status string
	)
	hold := Hold{
		AccountID: input.AccountID,
		Amount:    input.Amount,
		Currency:  input.Currency,
		Reference: input.Reference,
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO holds (account_id, amount, currency, status, reference, expires_at)
		VALUES ($1, $2, $3, 'active', $4, now() + ($5 * interval '1 second'))
		RETURNING id, status::text, expires_at, created_at`,
		toPgUUID(input.AccountID), input.Amount, input.Currency, textPtrToPg(input.Reference), input.TTLSeconds,
	).Scan(&id, &status, &hold.ExpiresAt, &hold.CreatedAt)
	if err != nil {
		return Hold{}, mapWriteError(err)
	}
	hold.ID = uuidFromPg(id)
	hold.Status = HoldStatus(status)

	if err := insertOutboxEvent(ctx, tx, "hold", hold.ID, "hold.created", map[string]any{
		"hold_id":    hold.ID,
		"account_id": hold.AccountID,
		"amount":     hold.Amount,
		"currency":   hold.Currency,
		"expires_at": hold.ExpiresAt,
	}); err != nil {
		return Hold{}, mapWriteError(err)
	}
	return hold, nil
}

func (l *Ledger) GetHold(ctx context.Context, holdID uuid.UUID) (Hold, error) {
	return scanHold(l.pool.QueryRow(ctx,
		`SELECT `+holdColumns+` FROM holds WHERE id = $1`, toPgUUID(holdID)))
}

func (l *Ledger) CaptureHold(ctx context.Context, tx pgx.Tx, input CaptureHoldInput) (Transfer, error) {
	if tx == nil {
		return Transfer{}, ErrNoTransaction
	}
	if input.HoldID == uuid.Nil {
		return Transfer{}, fmt.Errorf("%w: hold_id is required", ErrInvalidPosting)
	}
	if input.DestinationAccountID == uuid.Nil {
		return Transfer{}, fmt.Errorf("%w: destination_account_id is required", ErrInvalidPosting)
	}
	if input.Amount < 0 {
		return Transfer{}, ErrInvalidAmount
	}

	hold, err := lockHold(ctx, tx, input.HoldID)
	if err != nil {
		return Transfer{}, err
	}
	if err := hold.requireActive(); err != nil {
		return Transfer{}, err
	}
	if !hold.ExpiresAt.After(time.Now().UTC()) {
		return Transfer{}, ErrHoldExpired
	}
	if hold.AccountID == input.DestinationAccountID {
		return Transfer{}, fmt.Errorf("%w: source and destination are the same account", ErrInvalidPosting)
	}

	// Amount 0 means "capture the full hold"; any remainder is released.
	captureAmount := hold.Amount
	if input.Amount != 0 {
		captureAmount = input.Amount
	}
	if captureAmount <= 0 || captureAmount > hold.Amount {
		return Transfer{}, fmt.Errorf("%w: capture amount must be between 1 and hold amount", ErrInvalidPosting)
	}

	lockStart := time.Now()
	accounts, err := lockAccounts(ctx, tx, []uuid.UUID{hold.AccountID, input.DestinationAccountID})
	telemetry.ObserveLockWait("hold_capture", time.Since(lockStart))
	if err != nil {
		return Transfer{}, err
	}
	source := accounts[hold.AccountID]
	if source.Held < hold.Amount {
		return Transfer{}, heldInconsistency(hold)
	}
	if err := source.requireActive(hold.AccountID); err != nil {
		return Transfer{}, err
	}
	if err := changeHeld(ctx, tx, hold.AccountID, -hold.Amount); err != nil {
		return Transfer{}, err
	}

	metadata, err := json.Marshal(map[string]string{"hold_id": hold.ID.String()})
	if err != nil {
		return Transfer{}, err
	}
	transfer, err := l.Post(ctx, tx, simpleMovement(TransferTypeHoldCapture, hold.AccountID, input.DestinationAccountID,
		captureAmount, hold.Currency, hold.Reference, metadata))
	if err != nil {
		return Transfer{}, err
	}

	if _, err := markHoldResolved(ctx, tx, hold.ID, HoldStatusCaptured, &transfer.ID); err != nil {
		return Transfer{}, err
	}
	if err := insertOutboxEvent(ctx, tx, "hold", hold.ID, "hold.captured", map[string]any{
		"hold_id":     hold.ID,
		"transfer_id": transfer.ID,
		"amount":      captureAmount,
	}); err != nil {
		return Transfer{}, mapWriteError(err)
	}
	return transfer, nil
}

func (l *Ledger) ReleaseHold(ctx context.Context, tx pgx.Tx, holdID uuid.UUID) (Hold, error) {
	if tx == nil {
		return Hold{}, ErrNoTransaction
	}
	hold, err := lockHold(ctx, tx, holdID)
	if err != nil {
		return Hold{}, err
	}
	if err := hold.requireActive(); err != nil {
		return Hold{}, err
	}
	return finishHold(ctx, tx, hold, HoldStatusReleased, "hold.released")
}

// ExpireOne finds and expires at most one due hold. It is safe to run from
// multiple workers: the row is claimed with FOR UPDATE SKIP LOCKED.
func (l *Ledger) ExpireOne(ctx context.Context) (Hold, bool, error) {
	tx, err := l.pool.Begin(ctx)
	if err != nil {
		return Hold{}, false, err
	}
	defer tx.Rollback(ctx)

	var holdID pgtype.UUID
	err = tx.QueryRow(ctx, `
		SELECT id
		  FROM holds
		 WHERE status = 'active'
		   AND expires_at <= now()
		 ORDER BY expires_at, id
		 LIMIT 1
		   FOR UPDATE SKIP LOCKED`,
	).Scan(&holdID)

	var (
		hold    Hold
		expired bool
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Nothing due.
	case err != nil:
		return Hold{}, false, err
	default:
		hold, err = expireHoldLocked(ctx, tx, uuidFromPg(holdID))
		switch {
		case errors.Is(err, ErrHoldNotActive):
			// Resolved concurrently between the claim and the lock; not an error.
		case err != nil:
			return Hold{}, false, err
		default:
			expired = true
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Hold{}, false, err
	}
	if !expired {
		return Hold{}, false, nil
	}
	return hold, true, nil
}

func (l *Ledger) ExpireHold(ctx context.Context, tx pgx.Tx, holdID uuid.UUID) (Hold, error) {
	if tx == nil {
		return Hold{}, ErrNoTransaction
	}
	return expireHoldLocked(ctx, tx, holdID)
}

func expireHoldLocked(ctx context.Context, tx pgx.Tx, holdID uuid.UUID) (Hold, error) {
	hold, err := lockHold(ctx, tx, holdID)
	if err != nil {
		return Hold{}, err
	}
	if err := hold.requireActive(); err != nil {
		return Hold{}, err
	}
	if hold.ExpiresAt.After(time.Now().UTC()) {
		return Hold{}, ErrHoldNotActive
	}
	return finishHold(ctx, tx, hold, HoldStatusExpired, "hold.expired")
}

// finishHold returns the held amount to the account, records the terminal
// status, and emits the matching event. Used by release and expiry; capture
// moves money instead and has its own path.
func finishHold(ctx context.Context, tx pgx.Tx, hold Hold, status HoldStatus, eventType string) (Hold, error) {
	if err := releaseHeld(ctx, tx, hold); err != nil {
		return Hold{}, err
	}
	resolvedAt, err := markHoldResolved(ctx, tx, hold.ID, status, nil)
	if err != nil {
		return Hold{}, err
	}
	hold.Status = status
	hold.ResolvedAt = &resolvedAt

	if err := insertOutboxEvent(ctx, tx, "hold", hold.ID, eventType, map[string]any{"hold_id": hold.ID}); err != nil {
		return Hold{}, mapWriteError(err)
	}
	return hold, nil
}

func markHoldResolved(ctx context.Context, tx pgx.Tx, holdID uuid.UUID, status HoldStatus, transferID *uuid.UUID) (time.Time, error) {
	var resolvedAt time.Time
	err := tx.QueryRow(ctx, `
		UPDATE holds
		   SET status = $2::hold_status,
		       transfer_id = COALESCE($3, transfer_id),
		       resolved_at = now()
		 WHERE id = $1
		 RETURNING resolved_at`,
		toPgUUID(holdID), string(status), uuidPtrToPg(transferID),
	).Scan(&resolvedAt)
	if err != nil {
		return time.Time{}, mapWriteError(err)
	}
	return resolvedAt, nil
}

func releaseHeld(ctx context.Context, tx pgx.Tx, hold Hold) error {
	var currentlyHeld int64
	err := tx.QueryRow(ctx, `
		SELECT held
		  FROM account_balances
		 WHERE account_id = $1
		   FOR UPDATE`, toPgUUID(hold.AccountID),
	).Scan(&currentlyHeld)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return mapWriteError(err)
	}
	if currentlyHeld < hold.Amount {
		return heldInconsistency(hold)
	}
	return changeHeld(ctx, tx, hold.AccountID, -hold.Amount)
}

func heldInconsistency(hold Hold) error {
	return fmt.Errorf("ledger inconsistency: hold %s exceeds held balance for account %s", hold.ID, hold.AccountID)
}

func changeHeld(ctx context.Context, tx pgx.Tx, accountID uuid.UUID, delta int64) error {
	if delta == 0 {
		return nil
	}
	var held int64
	err := tx.QueryRow(ctx, `
		UPDATE account_balances
		   SET held = held + $2, version = version + 1, updated_at = now()
		 WHERE account_id = $1
		   AND held + $2 >= 0
		 RETURNING held`,
		toPgUUID(accountID), delta,
	).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("ledger inconsistency: could not adjust held balance for account %s", accountID)
	}
	if err != nil {
		return mapWriteError(err)
	}
	return nil
}

func lockHold(ctx context.Context, tx pgx.Tx, holdID uuid.UUID) (Hold, error) {
	return scanHold(tx.QueryRow(ctx,
		`SELECT `+holdColumns+` FROM holds WHERE id = $1 FOR UPDATE`, toPgUUID(holdID)))
}
