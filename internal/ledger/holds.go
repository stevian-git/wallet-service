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
)

const (
	minHoldTTLSeconds = 1
	maxHoldTTLSeconds = 7 * 24 * 60 * 60
)

func (l *Ledger) CreateHold(ctx context.Context, tx pgx.Tx, input CreateHoldInput) (Hold, error) {
	if tx == nil {
		return Hold{}, ErrNoTransaction
	}
	if input.AccountID == uuid.Nil {
		return Hold{}, fmt.Errorf("%w: account_id is required", ErrInvalidPosting)
	}
	if input.Amount <= 0 {
		return Hold{}, ErrInvalidAmount
	}
	if !validCurrency(input.Currency) {
		return Hold{}, fmt.Errorf("%w: invalid currency %q", ErrInvalidPosting, input.Currency)
	}
	if input.TTLSeconds < minHoldTTLSeconds || input.TTLSeconds > maxHoldTTLSeconds {
		return Hold{}, fmt.Errorf("%w: ttl_seconds must be between %d and %d", ErrInvalidPosting, minHoldTTLSeconds, maxHoldTTLSeconds)
	}
	if input.Reference != nil && len(*input.Reference) > 255 {
		return Hold{}, fmt.Errorf("%w: reference must be at most 255 characters", ErrInvalidPosting)
	}

	accounts, err := lockAccounts(ctx, tx, []uuid.UUID{input.AccountID})
	if err != nil {
		return Hold{}, err
	}
	account := accounts[input.AccountID]
	if account.Currency != input.Currency {
		return Hold{}, ErrCurrencyMismatch
	}
	if account.Status != "active" {
		return Hold{}, fmt.Errorf("%w: account %s has status %s", ErrAccountFrozen, input.AccountID, account.Status)
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
		id        pgtype.UUID
		status    string
		expiresAt time.Time
		createdAt time.Time
	)
	err = tx.QueryRow(ctx, `
		INSERT INTO holds (account_id, amount, currency, status, reference, expires_at)
		VALUES ($1, $2, $3, 'active', $4, now() + ($5 * interval '1 second'))
		RETURNING id, status::text, expires_at, created_at`,
		toPgUUID(input.AccountID), input.Amount, input.Currency, textPtrToPg(input.Reference), input.TTLSeconds,
	).Scan(&id, &status, &expiresAt, &createdAt)
	if err != nil {
		return Hold{}, mapWriteError(err)
	}

	hold := Hold{
		ID:        uuidFromPg(id),
		AccountID: input.AccountID,
		Amount:    input.Amount,
		Currency:  input.Currency,
		Status:    HoldStatus(status),
		Reference: input.Reference,
		ExpiresAt: expiresAt,
		CreatedAt: createdAt,
	}

	payload := map[string]any{
		"hold_id":    hold.ID,
		"account_id": hold.AccountID,
		"amount":     hold.Amount,
		"currency":   hold.Currency,
		"expires_at": hold.ExpiresAt,
	}
	if err := insertOutboxEvent(ctx, tx, "hold", hold.ID, "hold.created", payload); err != nil {
		return Hold{}, mapWriteError(err)
	}
	return hold, nil
}

func (l *Ledger) GetHold(ctx context.Context, holdID uuid.UUID) (Hold, error) {
	return scanHold(l.pool.QueryRow(ctx, `
		SELECT id, account_id, amount, currency, status::text, reference,
		       expires_at, transfer_id, created_at, resolved_at
		  FROM holds
		 WHERE id = $1`, toPgUUID(holdID)))
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
	if hold.Status != HoldStatusActive {
		return Transfer{}, fmt.Errorf("%w: hold status is %s", ErrHoldNotActive, hold.Status)
	}
	if !hold.ExpiresAt.After(time.Now().UTC()) {
		return Transfer{}, ErrHoldExpired
	}
	if hold.AccountID == input.DestinationAccountID {
		return Transfer{}, fmt.Errorf("%w: source and destination are the same account", ErrInvalidPosting)
	}

	captureAmount := hold.Amount
	if input.Amount != 0 {
		captureAmount = input.Amount
	}
	if captureAmount <= 0 || captureAmount > hold.Amount {
		return Transfer{}, fmt.Errorf("%w: capture amount must be between 1 and hold amount", ErrInvalidPosting)
	}

	accounts, err := lockAccounts(ctx, tx, []uuid.UUID{hold.AccountID, input.DestinationAccountID})
	if err != nil {
		return Transfer{}, err
	}
	source := accounts[hold.AccountID]
	if source.Held < hold.Amount {
		return Transfer{}, fmt.Errorf("ledger inconsistency: hold %s exceeds held balance for account %s", hold.ID, hold.AccountID)
	}
	if source.Status != "active" {
		return Transfer{}, fmt.Errorf("%w: account %s has status %s", ErrAccountFrozen, hold.AccountID, source.Status)
	}
	if err := changeHeld(ctx, tx, hold.AccountID, -hold.Amount); err != nil {
		return Transfer{}, err
	}

	metadata, err := json.Marshal(map[string]string{"hold_id": hold.ID.String()})
	if err != nil {
		return Transfer{}, err
	}

	sourceAccountID := hold.AccountID
	transfer, err := l.Post(ctx, tx, PostingRequest{
		Type:                 TransferTypeHoldCapture,
		SourceAccountID:      &sourceAccountID,
		DestinationAccountID: &input.DestinationAccountID,
		Amount:               captureAmount,
		Currency:             hold.Currency,
		Reference:            hold.Reference,
		Metadata:             metadata,
		Entries: []PostingEntry{
			{AccountID: hold.AccountID, Currency: hold.Currency, Direction: DirectionDebit, Amount: captureAmount},
			{AccountID: input.DestinationAccountID, Currency: hold.Currency, Direction: DirectionCredit, Amount: captureAmount},
		},
	})
	if err != nil {
		return Transfer{}, err
	}

	var resolvedAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE holds
		   SET status = 'captured', transfer_id = $2, resolved_at = now()
		 WHERE id = $1
		 RETURNING resolved_at`,
		toPgUUID(hold.ID), toPgUUID(transfer.ID),
	).Scan(&resolvedAt)
	if err != nil {
		return Transfer{}, mapWriteError(err)
	}

	payload := map[string]any{
		"hold_id":     hold.ID,
		"transfer_id": transfer.ID,
		"amount":      captureAmount,
	}
	if err := insertOutboxEvent(ctx, tx, "hold", hold.ID, "hold.captured", payload); err != nil {
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
	if hold.Status != HoldStatusActive {
		return Hold{}, fmt.Errorf("%w: hold status is %s", ErrHoldNotActive, hold.Status)
	}
	if err := releaseHeld(ctx, tx, hold); err != nil {
		return Hold{}, err
	}

	var resolvedAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE holds
		   SET status = 'released', resolved_at = now()
		 WHERE id = $1
		 RETURNING resolved_at`, toPgUUID(hold.ID),
	).Scan(&resolvedAt)
	if err != nil {
		return Hold{}, mapWriteError(err)
	}
	hold.Status = HoldStatusReleased
	hold.ResolvedAt = &resolvedAt

	if err := insertOutboxEvent(ctx, tx, "hold", hold.ID, "hold.released", map[string]any{"hold_id": hold.ID}); err != nil {
		return Hold{}, mapWriteError(err)
	}
	return hold, nil
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
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return Hold{}, false, err
		}
		return Hold{}, false, nil
	}
	if err != nil {
		return Hold{}, false, err
	}

	hold, err := expireHoldLocked(ctx, tx, uuidFromPg(holdID))
	if errors.Is(err, ErrHoldNotActive) {
		if err := tx.Commit(ctx); err != nil {
			return Hold{}, false, err
		}
		return Hold{}, false, nil
	}
	if err != nil {
		return Hold{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Hold{}, false, err
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
	if hold.Status != HoldStatusActive {
		return Hold{}, fmt.Errorf("%w: hold status is %s", ErrHoldNotActive, hold.Status)
	}
	if hold.ExpiresAt.After(time.Now().UTC()) {
		return Hold{}, ErrHoldNotActive
	}
	if err := releaseHeld(ctx, tx, hold); err != nil {
		return Hold{}, err
	}

	var resolvedAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE holds
		   SET status = 'expired', resolved_at = now()
		 WHERE id = $1
		 RETURNING resolved_at`, toPgUUID(hold.ID),
	).Scan(&resolvedAt)
	if err != nil {
		return Hold{}, mapWriteError(err)
	}
	hold.Status = HoldStatusExpired
	hold.ResolvedAt = &resolvedAt

	if err := insertOutboxEvent(ctx, tx, "hold", hold.ID, "hold.expired", map[string]any{"hold_id": hold.ID}); err != nil {
		return Hold{}, mapWriteError(err)
	}
	return hold, nil
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
		return fmt.Errorf("ledger inconsistency: hold %s exceeds held balance for account %s", hold.ID, hold.AccountID)
	}
	return changeHeld(ctx, tx, hold.AccountID, -hold.Amount)
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
	return scanHold(tx.QueryRow(ctx, `
		SELECT id, account_id, amount, currency, status::text, reference,
		       expires_at, transfer_id, created_at, resolved_at
		  FROM holds
		 WHERE id = $1
		   FOR UPDATE`, toPgUUID(holdID)))
}

func scanHold(row pgx.Row) (Hold, error) {
	var (
		id         pgtype.UUID
		accountID  pgtype.UUID
		amount     int64
		currency   string
		status     string
		reference  pgtype.Text
		expiresAt  time.Time
		transferID pgtype.UUID
		createdAt  time.Time
		resolvedAt pgtype.Timestamptz
	)
	err := row.Scan(&id, &accountID, &amount, &currency, &status, &reference,
		&expiresAt, &transferID, &createdAt, &resolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Hold{}, ErrNotFound
	}
	if err != nil {
		return Hold{}, err
	}

	hold := Hold{
		ID:         uuidFromPg(id),
		AccountID:  uuidFromPg(accountID),
		Amount:     amount,
		Currency:   currency,
		Status:     HoldStatus(status),
		Reference:  textPtrFromPg(reference),
		ExpiresAt:  expiresAt,
		TransferID: uuidPtrFromPg(transferID),
		CreatedAt:  createdAt,
	}
	if resolvedAt.Valid {
		value := resolvedAt.Time
		hold.ResolvedAt = &value
	}
	return hold, nil
}
