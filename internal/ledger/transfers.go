package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (l *Ledger) Topup(ctx context.Context, tx pgx.Tx, input TopupInput) (Transfer, error) {
	if tx == nil {
		return Transfer{}, ErrNoTransaction
	}
	if err := validateAmountAndCurrency(input.Amount, input.Currency); err != nil {
		return Transfer{}, err
	}
	bankID, err := l.systemBankAccount(ctx, tx, input.Currency)
	if err != nil {
		return Transfer{}, err
	}
	return l.Post(ctx, tx, simpleMovement(TransferTypeTopup, bankID, input.DestinationAccountID,
		input.Amount, input.Currency, input.Reference, input.Metadata))
}

func (l *Ledger) Transfer(ctx context.Context, tx pgx.Tx, input TransferInput) (Transfer, error) {
	if err := validateAmountAndCurrency(input.Amount, input.Currency); err != nil {
		return Transfer{}, err
	}
	return l.Post(ctx, tx, simpleMovement(TransferTypeTransfer, input.SourceAccountID, input.DestinationAccountID,
		input.Amount, input.Currency, input.Reference, input.Metadata))
}

func (l *Ledger) Withdrawal(ctx context.Context, tx pgx.Tx, input WithdrawalInput) (Transfer, error) {
	if tx == nil {
		return Transfer{}, ErrNoTransaction
	}
	if err := validateAmountAndCurrency(input.Amount, input.Currency); err != nil {
		return Transfer{}, err
	}
	bankID, err := l.systemBankAccount(ctx, tx, input.Currency)
	if err != nil {
		return Transfer{}, err
	}
	return l.Post(ctx, tx, simpleMovement(TransferTypeWithdrawal, input.SourceAccountID, bankID,
		input.Amount, input.Currency, input.Reference, input.Metadata))
}

// simpleMovement builds the two-leg posting every non-reversal transfer uses:
// debit the source, credit the destination, same amount and currency.
func simpleMovement(
	kind TransferType,
	source, destination uuid.UUID,
	amount int64,
	currency string,
	reference *string,
	metadata json.RawMessage,
) PostingRequest {
	return PostingRequest{
		Type:                 kind,
		SourceAccountID:      &source,
		DestinationAccountID: &destination,
		Amount:               amount,
		Currency:             currency,
		Reference:            reference,
		Metadata:             metadata,
		Entries: []PostingEntry{
			{AccountID: source, Currency: currency, Direction: DirectionDebit, Amount: amount},
			{AccountID: destination, Currency: currency, Direction: DirectionCredit, Amount: amount},
		},
	}
}

// validateAmountAndCurrency is the fast-fail check run before any DB access.
// Post re-validates the full posting; this only avoids a wasted round trip.
func validateAmountAndCurrency(amount int64, currency string) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	if !validCurrency(currency) {
		return fmt.Errorf("%w: invalid currency %q", ErrInvalidPosting, currency)
	}
	return nil
}

func (l *Ledger) systemBankAccount(ctx context.Context, tx pgx.Tx, currency string) (uuid.UUID, error) {
	var (
		id     pgtype.UUID
		status string
	)
	err := tx.QueryRow(ctx, `
		SELECT id, status
		  FROM accounts
		 WHERE kind = 'system_bank'
		   AND owner_ref = 'system:bank'
		   AND currency = $1
		 ORDER BY id
		 LIMIT 1`, currency,
	).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	if status != accountStatusActive {
		return uuid.Nil, fmt.Errorf("%w: system bank account is %s", ErrAccountFrozen, status)
	}
	return uuidFromPg(id), nil
}
