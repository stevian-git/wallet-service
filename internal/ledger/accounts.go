package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stevians/wallet-service/internal/pagination"
)

func (l *Ledger) CreateAccount(ctx context.Context, tx pgx.Tx, input CreateAccountInput) (Account, error) {
	if tx == nil {
		return Account{}, ErrNoTransaction
	}
	if input.OwnerRef == "" || len(input.OwnerRef) > 128 {
		return Account{}, fmt.Errorf("%w: owner_ref must be between 1 and 128 characters", ErrInvalidPosting)
	}
	if input.Kind != AccountKindUserWallet && input.Kind != AccountKindMerchant {
		return Account{}, fmt.Errorf("%w: kind must be user_wallet or merchant", ErrInvalidPosting)
	}
	if !validCurrency(input.Currency) {
		return Account{}, fmt.Errorf("%w: invalid currency %q", ErrInvalidPosting, input.Currency)
	}

	var (
		id        pgtype.UUID
		createdAt time.Time
	)
	err := tx.QueryRow(ctx, `
		INSERT INTO accounts (kind, owner_ref, currency, normal_side, allow_negative)
		VALUES ($1, $2, $3, 'credit', false)
		RETURNING id, created_at`,
		string(input.Kind), input.OwnerRef, input.Currency,
	).Scan(&id, &createdAt)
	if err != nil {
		if isOwnerCurrencyViolation(err) {
			return Account{}, ErrAccountExists
		}
		return Account{}, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO account_balances (account_id, allow_negative)
		VALUES ($1, false)`, id); err != nil {
		return Account{}, err
	}

	accountID := uuidFromPg(id)
	if err := insertOutboxEvent(ctx, tx, "account", accountID, "account.created", map[string]any{
		"account_id": accountID,
		"owner_ref":  input.OwnerRef,
		"currency":   input.Currency,
	}); err != nil {
		return Account{}, err
	}

	ownerRef := input.OwnerRef
	return Account{
		ID:            accountID,
		Kind:          input.Kind,
		OwnerRef:      &ownerRef,
		Currency:      input.Currency,
		NormalSide:    NormalSideCredit,
		AllowNegative: false,
		Status:        accountStatusActive,
		CreatedAt:     createdAt,
	}, nil
}

func (l *Ledger) GetAccount(ctx context.Context, accountID uuid.UUID) (Account, error) {
	return scanAccount(l.pool.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE id = $1`, toPgUUID(accountID)))
}

func (l *Ledger) ListAccounts(ctx context.Context, afterID *uuid.UUID, limit int) ([]Account, *uuid.UUID, error) {
	limit = pagination.ClampLimit(limit)
	rows, err := l.pool.Query(ctx, `
		SELECT `+accountColumns+`
		  FROM accounts
		 WHERE ($1::uuid = '00000000-0000-0000-0000-000000000000' OR id < $1)
		 ORDER BY id DESC
		 LIMIT $2`, uuidCursor(afterID), limit+1)
	if err != nil {
		return nil, nil, err
	}
	accounts, err := pgx.CollectRows(rows, collect(scanAccount))
	if err != nil {
		return nil, nil, err
	}

	accounts, more := pagination.Trim(accounts, limit)
	if !more {
		return accounts, nil, nil
	}
	next := accounts[len(accounts)-1].ID
	return accounts, &next, nil
}

func (l *Ledger) GetBalance(ctx context.Context, accountID uuid.UUID) (Balance, error) {
	var (
		id      pgtype.UUID
		balance Balance
	)
	err := l.pool.QueryRow(ctx, `
		SELECT b.account_id, a.currency, b.posted, b.held, b.posted - b.held, now()
		  FROM account_balances b
		  JOIN accounts a ON a.id = b.account_id
		 WHERE b.account_id = $1`, toPgUUID(accountID),
	).Scan(&id, &balance.Currency, &balance.Posted, &balance.Held, &balance.Available, &balance.AsOf)
	if err != nil {
		return Balance{}, notFoundOnNoRows(err)
	}
	balance.AccountID = uuidFromPg(id)
	return balance, nil
}

func (l *Ledger) ListEntries(ctx context.Context, accountID uuid.UUID, afterID *int64, limit int) ([]Entry, *int64, error) {
	limit = pagination.ClampLimit(limit)

	var exists bool
	if err := l.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $1)`, toPgUUID(accountID)).Scan(&exists); err != nil {
		return nil, nil, err
	}
	if !exists {
		return nil, nil, ErrNotFound
	}

	var cursor int64
	if afterID != nil {
		cursor = *afterID
	}

	rows, err := l.pool.Query(ctx, `
		SELECT `+entryColumns+`
		  FROM ledger_entries
		 WHERE account_id = $1
		   AND ($2::bigint = 0 OR id < $2)
		 ORDER BY id DESC
		 LIMIT $3`, toPgUUID(accountID), cursor, limit+1)
	if err != nil {
		return nil, nil, err
	}
	entries, err := pgx.CollectRows(rows, collect(scanEntry))
	if err != nil {
		return nil, nil, err
	}

	entries, more := pagination.Trim(entries, limit)
	if !more {
		return entries, nil, nil
	}
	next := entries[len(entries)-1].ID
	return entries, &next, nil
}

func (l *Ledger) GetTransfer(ctx context.Context, transferID uuid.UUID) (TransferDetail, error) {
	transfer, err := scanTransfer(l.pool.QueryRow(ctx,
		`SELECT `+transferColumns+` FROM transfers WHERE id = $1`, toPgUUID(transferID)))
	if err != nil {
		return TransferDetail{}, err
	}

	rows, err := l.pool.Query(ctx, `
		SELECT `+entryColumns+`
		  FROM ledger_entries
		 WHERE transfer_id = $1
		 ORDER BY id`, toPgUUID(transfer.ID))
	if err != nil {
		return TransferDetail{}, err
	}
	entries, err := pgx.CollectRows(rows, collect(scanEntry))
	if err != nil {
		return TransferDetail{}, err
	}
	return TransferDetail{Transfer: transfer, Entries: entries}, nil
}

func (l *Ledger) ListTransfers(ctx context.Context, afterID *uuid.UUID, limit int) ([]Transfer, *uuid.UUID, error) {
	limit = pagination.ClampLimit(limit)
	rows, err := l.pool.Query(ctx, `
		SELECT `+transferColumns+`
		  FROM transfers
		 WHERE ($1::uuid = '00000000-0000-0000-0000-000000000000' OR id < $1)
		 ORDER BY id DESC
		 LIMIT $2`, uuidCursor(afterID), limit+1)
	if err != nil {
		return nil, nil, err
	}
	transfers, err := pgx.CollectRows(rows, collect(scanTransfer))
	if err != nil {
		return nil, nil, err
	}

	transfers, more := pagination.Trim(transfers, limit)
	if !more {
		return transfers, nil, nil
	}
	next := transfers[len(transfers)-1].ID
	return transfers, &next, nil
}

// uuidCursor maps "no cursor" to the nil UUID sentinel the list queries test for.
func uuidCursor(afterID *uuid.UUID) pgtype.UUID {
	if afterID == nil {
		return toPgUUID(uuid.Nil)
	}
	return toPgUUID(*afterID)
}

func isOwnerCurrencyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		strings.Contains(pgErr.ConstraintName, "owner_currency")
}
