package ledger

import (
	"context"
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
	if input.Amount <= 0 {
		return Transfer{}, ErrInvalidAmount
	}
	if !validCurrency(input.Currency) {
		return Transfer{}, fmt.Errorf("%w: invalid currency %q", ErrInvalidPosting, input.Currency)
	}

	bankID, err := l.systemBankAccount(ctx, tx, input.Currency)
	if err != nil {
		return Transfer{}, err
	}

	destination := input.DestinationAccountID
	source := bankID
	return l.Post(ctx, tx, PostingRequest{
		Type:                 TransferTypeTopup,
		SourceAccountID:      &source,
		DestinationAccountID: &destination,
		Amount:               input.Amount,
		Currency:             input.Currency,
		Reference:            input.Reference,
		Metadata:             input.Metadata,
		Entries: []PostingEntry{
			{AccountID: bankID, Currency: input.Currency, Direction: DirectionDebit, Amount: input.Amount},
			{AccountID: destination, Currency: input.Currency, Direction: DirectionCredit, Amount: input.Amount},
		},
	})
}

func (l *Ledger) Transfer(ctx context.Context, tx pgx.Tx, input TransferInput) (Transfer, error) {
	if input.Amount <= 0 {
		return Transfer{}, ErrInvalidAmount
	}
	if !validCurrency(input.Currency) {
		return Transfer{}, fmt.Errorf("%w: invalid currency %q", ErrInvalidPosting, input.Currency)
	}

	source := input.SourceAccountID
	destination := input.DestinationAccountID
	return l.Post(ctx, tx, PostingRequest{
		Type:                 TransferTypeTransfer,
		SourceAccountID:      &source,
		DestinationAccountID: &destination,
		Amount:               input.Amount,
		Currency:             input.Currency,
		Reference:            input.Reference,
		Metadata:             input.Metadata,
		Entries: []PostingEntry{
			{AccountID: source, Currency: input.Currency, Direction: DirectionDebit, Amount: input.Amount},
			{AccountID: destination, Currency: input.Currency, Direction: DirectionCredit, Amount: input.Amount},
		},
	})
}

func (l *Ledger) Withdrawal(ctx context.Context, tx pgx.Tx, input WithdrawalInput) (Transfer, error) {
	if tx == nil {
		return Transfer{}, ErrNoTransaction
	}
	if input.Amount <= 0 {
		return Transfer{}, ErrInvalidAmount
	}
	if !validCurrency(input.Currency) {
		return Transfer{}, fmt.Errorf("%w: invalid currency %q", ErrInvalidPosting, input.Currency)
	}

	bankID, err := l.systemBankAccount(ctx, tx, input.Currency)
	if err != nil {
		return Transfer{}, err
	}

	source := input.SourceAccountID
	destination := bankID
	return l.Post(ctx, tx, PostingRequest{
		Type:                 TransferTypeWithdrawal,
		SourceAccountID:      &source,
		DestinationAccountID: &destination,
		Amount:               input.Amount,
		Currency:             input.Currency,
		Reference:            input.Reference,
		Metadata:             input.Metadata,
		Entries: []PostingEntry{
			{AccountID: source, Currency: input.Currency, Direction: DirectionDebit, Amount: input.Amount},
			{AccountID: bankID, Currency: input.Currency, Direction: DirectionCredit, Amount: input.Amount},
		},
	})
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
	if status != "active" {
		return uuid.Nil, fmt.Errorf("%w: system bank account is %s", ErrAccountFrozen, status)
	}
	return uuidFromPg(id), nil
}
