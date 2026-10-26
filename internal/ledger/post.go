package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type lockedAccount struct {
	Currency      string
	Status        string
	NormalSide    NormalSide
	Posted        int64
	Held          int64
	AllowNegative bool
}

func (l *Ledger) Post(ctx context.Context, tx pgx.Tx, req PostingRequest) (Transfer, error) {
	if err := validatePosting(req); err != nil {
		return Transfer{}, err
	}
	if tx == nil {
		return Transfer{}, ErrNoTransaction
	}

	accountIDs := make([]uuid.UUID, 0, len(req.Entries))
	for _, entry := range req.Entries {
		accountIDs = append(accountIDs, entry.AccountID)
	}
	sort.Slice(accountIDs, func(i, j int) bool {
		return accountIDs[i].String() < accountIDs[j].String()
	})

	accounts, err := lockAccounts(ctx, tx, accountIDs)
	if err != nil {
		return Transfer{}, err
	}

	postedAfter := make(map[uuid.UUID]int64, len(req.Entries))
	for _, entry := range req.Entries {
		account, ok := accounts[entry.AccountID]
		if !ok {
			return Transfer{}, ErrNotFound
		}
		if account.Currency != req.Currency {
			return Transfer{}, ErrCurrencyMismatch
		}
		if account.Status != "active" {
			return Transfer{}, fmt.Errorf("%w: account %s has status %s", ErrAccountFrozen, entry.AccountID, account.Status)
		}

		delta := entry.Amount
		if entry.Direction != Direction(account.NormalSide) {
			delta = -entry.Amount
		}
		newPosted, ok := addInt64(account.Posted, delta)
		if !ok {
			return Transfer{}, ErrInvalidAmount
		}
		if !account.AllowNegative && newPosted < account.Held {
			return Transfer{}, ErrInsufficientFunds
		}
		postedAfter[entry.AccountID] = newPosted
	}

	transferID := uuid.New()
	createdAt := time.Now().UTC()
	postedAt := createdAt

	var insertedID pgtype.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO transfers (
			id, type, status, amount, currency,
			source_account_id, destination_account_id, reference,
			reverses_transfer_id, metadata, posted_at
		) VALUES (
			$1, $2, 'posted', $3, $4,
			$5, $6, $7,
			$8, $9::jsonb, $10
		)
		RETURNING id, created_at, posted_at`,
		toPgUUID(transferID),
		string(req.Type),
		req.Amount,
		req.Currency,
		uuidPtrToPg(req.SourceAccountID),
		uuidPtrToPg(req.DestinationAccountID),
		textPtrToPg(req.Reference),
		uuidPtrToPg(req.ReversesTransferID),
		metadataOrEmpty(req.Metadata),
		postedAt,
	).Scan(&insertedID, &createdAt, &postedAt)
	if err != nil {
		return Transfer{}, mapWriteError(err)
	}

	for _, entry := range req.Entries {
		_, err = tx.Exec(ctx, `
			INSERT INTO ledger_entries (
				transfer_id, account_id, currency, direction, amount, balance_after
			) VALUES ($1, $2, $3, $4, $5, $6)`,
			toPgUUID(transferID),
			toPgUUID(entry.AccountID),
			req.Currency,
			string(entry.Direction),
			entry.Amount,
			postedAfter[entry.AccountID],
		)
		if err != nil {
			return Transfer{}, mapWriteError(err)
		}

		tag, err := tx.Exec(ctx, `
			UPDATE account_balances
			   SET posted = $2, version = version + 1, updated_at = now()
			 WHERE account_id = $1`,
			toPgUUID(entry.AccountID),
			postedAfter[entry.AccountID],
		)
		if err != nil {
			return Transfer{}, mapWriteError(err)
		}
		if tag.RowsAffected() != 1 {
			return Transfer{}, ErrNotFound
		}
	}

	if err := insertTransferEvent(ctx, tx, transferID, req); err != nil {
		return Transfer{}, mapWriteError(err)
	}

	metadata := json.RawMessage(metadataOrEmpty(req.Metadata))
	return Transfer{
		ID:                   uuidFromPg(insertedID),
		Type:                 req.Type,
		Status:               TransferStatusPosted,
		Amount:               req.Amount,
		Currency:             req.Currency,
		SourceAccountID:      req.SourceAccountID,
		DestinationAccountID: req.DestinationAccountID,
		Reference:            req.Reference,
		ReversesTransferID:   req.ReversesTransferID,
		Metadata:             metadata,
		CreatedAt:            createdAt,
		PostedAt:             &postedAt,
	}, nil
}

func insertTransferEvent(ctx context.Context, tx pgx.Tx, transferID uuid.UUID, req PostingRequest) error {
	eventType := "transfer.posted"
	payload := map[string]any{
		"transfer_id":            transferID,
		"type":                   req.Type,
		"amount":                 req.Amount,
		"currency":               req.Currency,
		"source_account_id":      req.SourceAccountID,
		"destination_account_id": req.DestinationAccountID,
		"reference":              req.Reference,
	}
	if req.Type == TransferTypeReversal {
		eventType = "transfer.reversed"
		payload = map[string]any{
			"transfer_id":          transferID,
			"reverses_transfer_id": req.ReversesTransferID,
			"amount":               req.Amount,
			"currency":             req.Currency,
		}
	}
	return insertOutboxEvent(ctx, tx, "transfer", transferID, eventType, payload)
}

func validatePosting(req PostingRequest) error {
	if !validCurrency(req.Currency) {
		return fmt.Errorf("%w: invalid currency %q", ErrInvalidPosting, req.Currency)
	}
	if req.Amount <= 0 {
		return ErrInvalidAmount
	}
	if len(req.Entries) < 2 {
		return fmt.Errorf("%w: at least two entries are required", ErrInvalidPosting)
	}

	switch req.Type {
	case TransferTypeTopup, TransferTypeTransfer, TransferTypeWithdrawal, TransferTypeHoldCapture, TransferTypeReversal:
	default:
		return fmt.Errorf("%w: invalid transfer type %q", ErrInvalidPosting, req.Type)
	}

	seen := make(map[uuid.UUID]struct{}, len(req.Entries))
	var totalDebit, totalCredit int64

	for _, entry := range req.Entries {
		if entry.Amount <= 0 {
			return ErrInvalidAmount
		}
		if !validCurrency(entry.Currency) || entry.Currency != req.Currency {
			return ErrCurrencyMismatch
		}
		if entry.AccountID == uuid.Nil {
			return fmt.Errorf("%w: account id is required", ErrInvalidPosting)
		}
		if _, exists := seen[entry.AccountID]; exists {
			return fmt.Errorf("%w: duplicate account %s", ErrInvalidPosting, entry.AccountID)
		}
		seen[entry.AccountID] = struct{}{}

		switch entry.Direction {
		case DirectionDebit:
			if totalDebit > math.MaxInt64-entry.Amount {
				return ErrInvalidAmount
			}
			totalDebit += entry.Amount
		case DirectionCredit:
			if totalCredit > math.MaxInt64-entry.Amount {
				return ErrInvalidAmount
			}
			totalCredit += entry.Amount
		default:
			return fmt.Errorf("%w: invalid direction %q", ErrInvalidPosting, entry.Direction)
		}
	}

	if totalDebit != totalCredit {
		return ErrUnbalancedEntries
	}
	if req.Amount != totalDebit {
		return fmt.Errorf("%w: transfer amount does not match entries", ErrInvalidPosting)
	}
	if req.SourceAccountID != nil && req.DestinationAccountID != nil && *req.SourceAccountID == *req.DestinationAccountID {
		return fmt.Errorf("%w: source and destination are the same account", ErrInvalidPosting)
	}

	return nil
}

func lockAccounts(ctx context.Context, tx pgx.Tx, accountIDs []uuid.UUID) (map[uuid.UUID]lockedAccount, error) {
	ids := make([]string, len(accountIDs))
	for i, id := range accountIDs {
		ids[i] = id.String()
	}

	rows, err := tx.Query(ctx, `
		SELECT b.account_id,
		       a.currency,
		       a.status,
		       a.normal_side::text,
		       b.posted,
		       b.held,
		       b.allow_negative
		  FROM account_balances b
		  JOIN accounts a ON a.id = b.account_id
		 WHERE b.account_id = ANY($1::uuid[])
		 ORDER BY b.account_id
		   FOR UPDATE OF b`, ids)
	if err != nil {
		return nil, mapWriteError(err)
	}
	defer rows.Close()

	result := make(map[uuid.UUID]lockedAccount, len(accountIDs))
	for rows.Next() {
		var (
			id            pgtype.UUID
			currency      string
			status        string
			normalSide    string
			posted        int64
			held          int64
			allowNegative bool
		)
		if err := rows.Scan(&id, &currency, &status, &normalSide, &posted, &held, &allowNegative); err != nil {
			return nil, err
		}
		result[uuidFromPg(id)] = lockedAccount{
			Currency:      currency,
			Status:        status,
			NormalSide:    NormalSide(normalSide),
			Posted:        posted,
			Held:          held,
			AllowNegative: allowNegative,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(accountIDs) {
		return nil, ErrNotFound
	}
	return result, nil
}

func validCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for i := 0; i < len(currency); i++ {
		if currency[i] < 'A' || currency[i] > 'Z' {
			return false
		}
	}
	return true
}

func addInt64(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}

func metadataOrEmpty(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return "{}"
	}
	return string(metadata)
}

func textPtrToPg(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23514":
			if strings.Contains(pgErr.ConstraintName, "account_balances") {
				slog.Error("account_balances check constraint fired; treating as a ledger race",
					"constraint", pgErr.ConstraintName,
					"detail", pgErr.Detail,
				)
				return ErrInsufficientFunds
			}
			if strings.Contains(pgErr.ConstraintName, "amount") {
				return ErrInvalidAmount
			}
			return fmt.Errorf("%w: %s", ErrInvalidPosting, pgErr.ConstraintName)
		case "23505":
			if strings.Contains(pgErr.ConstraintName, "reverses_transfer_id") {
				return ErrAlreadyReversed
			}
			return err
		case "23503":
			return ErrNotFound
		}
	}
	return err
}
