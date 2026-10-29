package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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
		if isUniqueViolation(err) {
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
		Status:        "active",
		CreatedAt:     createdAt,
	}, nil
}

func (l *Ledger) GetAccount(ctx context.Context, accountID uuid.UUID) (Account, error) {
	var (
		id            pgtype.UUID
		kind          string
		ownerRef      pgtype.Text
		currency      string
		normalSide    string
		allowNegative bool
		status        string
		createdAt     time.Time
	)
	err := l.pool.QueryRow(ctx, `
		SELECT id, kind::text, owner_ref, currency, normal_side::text, allow_negative, status, created_at
		  FROM accounts
		 WHERE id = $1`, toPgUUID(accountID),
	).Scan(&id, &kind, &ownerRef, &currency, &normalSide, &allowNegative, &status, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}

	return Account{
		ID:            uuidFromPg(id),
		Kind:          AccountKind(kind),
		OwnerRef:      textPtrFromPg(ownerRef),
		Currency:      currency,
		NormalSide:    NormalSide(normalSide),
		AllowNegative: allowNegative,
		Status:        status,
		CreatedAt:     createdAt,
	}, nil
}

func (l *Ledger) ListAccounts(ctx context.Context, afterID *uuid.UUID, limit int) ([]Account, *uuid.UUID, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	var cursor pgtype.UUID
	if afterID != nil {
		cursor = toPgUUID(*afterID)
	}

	rows, err := l.pool.Query(ctx, `
		SELECT id, kind::text, owner_ref, currency, normal_side::text, allow_negative, status, created_at
		  FROM accounts
		 WHERE ($1::uuid = '00000000-0000-0000-0000-000000000000' OR id < $1)
		 ORDER BY id DESC
		 LIMIT $2`, cursor, limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	accounts := make([]Account, 0, limit+1)
	for rows.Next() {
		var (
			id            pgtype.UUID
			kind          string
			ownerRef      pgtype.Text
			currency      string
			normalSide    string
			allowNegative bool
			status        string
			createdAt     time.Time
		)
		if err := rows.Scan(&id, &kind, &ownerRef, &currency, &normalSide, &allowNegative, &status, &createdAt); err != nil {
			return nil, nil, err
		}
		accounts = append(accounts, Account{
			ID:            uuidFromPg(id),
			Kind:          AccountKind(kind),
			OwnerRef:      textPtrFromPg(ownerRef),
			Currency:      currency,
			NormalSide:    NormalSide(normalSide),
			AllowNegative: allowNegative,
			Status:        status,
			CreatedAt:     createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var nextCursor *uuid.UUID
	if len(accounts) > limit {
		accounts = accounts[:limit]
		value := accounts[len(accounts)-1].ID
		nextCursor = &value
	}
	return accounts, nextCursor, nil
}

func (l *Ledger) GetBalance(ctx context.Context, accountID uuid.UUID) (Balance, error) {
	var (
		id        pgtype.UUID
		currency  string
		posted    int64
		held      int64
		available int64
		asOf      time.Time
	)
	err := l.pool.QueryRow(ctx, `
		SELECT b.account_id, a.currency, b.posted, b.held, b.posted - b.held, now()
		  FROM account_balances b
		  JOIN accounts a ON a.id = b.account_id
		 WHERE b.account_id = $1`, toPgUUID(accountID),
	).Scan(&id, &currency, &posted, &held, &available, &asOf)
	if errors.Is(err, pgx.ErrNoRows) {
		return Balance{}, ErrNotFound
	}
	if err != nil {
		return Balance{}, err
	}

	return Balance{
		AccountID: uuidFromPg(id),
		Currency:  currency,
		Posted:    posted,
		Held:      held,
		Available: available,
		AsOf:      asOf,
	}, nil
}

func (l *Ledger) ListEntries(ctx context.Context, accountID uuid.UUID, afterID *int64, limit int) ([]Entry, *int64, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

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
		SELECT id, transfer_id, account_id, currency, direction::text, amount, balance_after, created_at
		  FROM ledger_entries
		 WHERE account_id = $1
		   AND ($2::bigint = 0 OR id < $2)
		 ORDER BY id DESC
		 LIMIT $3`, toPgUUID(accountID), cursor, limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	entries := make([]Entry, 0, limit+1)
	for rows.Next() {
		var (
			id           int64
			transferID   pgtype.UUID
			accountID    pgtype.UUID
			currency     string
			direction    string
			amount       int64
			balanceAfter int64
			createdAt    time.Time
		)
		if err := rows.Scan(&id, &transferID, &accountID, &currency, &direction, &amount, &balanceAfter, &createdAt); err != nil {
			return nil, nil, err
		}
		entries = append(entries, Entry{
			ID:           id,
			TransferID:   uuidFromPg(transferID),
			AccountID:    uuidFromPg(accountID),
			Currency:     currency,
			Direction:    Direction(direction),
			Amount:       amount,
			BalanceAfter: balanceAfter,
			CreatedAt:    createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var nextCursor *int64
	if len(entries) > limit {
		entries = entries[:limit]
		value := entries[len(entries)-1].ID
		nextCursor = &value
	}
	return entries, nextCursor, nil
}

func (l *Ledger) GetTransfer(ctx context.Context, transferID uuid.UUID) (TransferDetail, error) {
	var (
		id                   pgtype.UUID
		transferType         string
		status               string
		amount               int64
		currency             string
		sourceAccountID      pgtype.UUID
		destinationAccountID pgtype.UUID
		reference            pgtype.Text
		reversesTransferID   pgtype.UUID
		metadata             []byte
		createdAt            time.Time
		postedAt             pgtype.Timestamptz
	)
	err := l.pool.QueryRow(ctx, `
		SELECT id, type::text, status::text, amount, currency,
		       source_account_id, destination_account_id, reference,
		       reverses_transfer_id, metadata, created_at, posted_at
		  FROM transfers
		 WHERE id = $1`, toPgUUID(transferID),
	).Scan(
		&id, &transferType, &status, &amount, &currency,
		&sourceAccountID, &destinationAccountID, &reference,
		&reversesTransferID, &metadata, &createdAt, &postedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return TransferDetail{}, ErrNotFound
	}
	if err != nil {
		return TransferDetail{}, err
	}

	var postedAtPtr *time.Time
	if postedAt.Valid {
		value := postedAt.Time
		postedAtPtr = &value
	}

	transfer := Transfer{
		ID:                   uuidFromPg(id),
		Type:                 TransferType(transferType),
		Status:               TransferStatus(status),
		Amount:               amount,
		Currency:             currency,
		SourceAccountID:      uuidPtrFromPg(sourceAccountID),
		DestinationAccountID: uuidPtrFromPg(destinationAccountID),
		Reference:            textPtrFromPg(reference),
		ReversesTransferID:   uuidPtrFromPg(reversesTransferID),
		Metadata:             json.RawMessage(metadata),
		CreatedAt:            createdAt,
		PostedAt:             postedAtPtr,
	}

	entries, err := l.listTransferEntries(ctx, transfer.ID)
	if err != nil {
		return TransferDetail{}, err
	}
	return TransferDetail{Transfer: transfer, Entries: entries}, nil
}

func (l *Ledger) listTransferEntries(ctx context.Context, transferID uuid.UUID) ([]Entry, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT id, transfer_id, account_id, currency, direction::text, amount, balance_after, created_at
		  FROM ledger_entries
		 WHERE transfer_id = $1
		 ORDER BY id`, toPgUUID(transferID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]Entry, 0, 2)
	for rows.Next() {
		var (
			id           int64
			transferID   pgtype.UUID
			accountID    pgtype.UUID
			currency     string
			direction    string
			amount       int64
			balanceAfter int64
			createdAt    time.Time
		)
		if err := rows.Scan(&id, &transferID, &accountID, &currency, &direction, &amount, &balanceAfter, &createdAt); err != nil {
			return nil, err
		}
		entries = append(entries, Entry{
			ID:           id,
			TransferID:   uuidFromPg(transferID),
			AccountID:    uuidFromPg(accountID),
			Currency:     currency,
			Direction:    Direction(direction),
			Amount:       amount,
			BalanceAfter: balanceAfter,
			CreatedAt:    createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (l *Ledger) ListTransfers(ctx context.Context, afterID *uuid.UUID, limit int) ([]Transfer, *uuid.UUID, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	var cursor pgtype.UUID
	if afterID != nil {
		cursor = toPgUUID(*afterID)
	}

	rows, err := l.pool.Query(ctx, `
		SELECT id, type::text, status::text, amount, currency,
		       source_account_id, destination_account_id, reference,
		       reverses_transfer_id, metadata, created_at, posted_at
		  FROM transfers
		 WHERE ($1::uuid = '00000000-0000-0000-0000-000000000000' OR id < $1)
		 ORDER BY id DESC
		 LIMIT $2`, cursor, limit+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	transfers := make([]Transfer, 0, limit+1)
	for rows.Next() {
		var (
			id                   pgtype.UUID
			transferType         string
			status               string
			amount               int64
			currency             string
			sourceAccountID      pgtype.UUID
			destinationAccountID pgtype.UUID
			reference            pgtype.Text
			reversesTransferID   pgtype.UUID
			metadata             []byte
			createdAt            time.Time
			postedAt             pgtype.Timestamptz
		)
		if err := rows.Scan(
			&id, &transferType, &status, &amount, &currency,
			&sourceAccountID, &destinationAccountID, &reference,
			&reversesTransferID, &metadata, &createdAt, &postedAt,
		); err != nil {
			return nil, nil, err
		}
		var postedAtPtr *time.Time
		if postedAt.Valid {
			value := postedAt.Time
			postedAtPtr = &value
		}
		transfers = append(transfers, Transfer{
			ID:                   uuidFromPg(id),
			Type:                 TransferType(transferType),
			Status:               TransferStatus(status),
			Amount:               amount,
			Currency:             currency,
			SourceAccountID:      uuidPtrFromPg(sourceAccountID),
			DestinationAccountID: uuidPtrFromPg(destinationAccountID),
			Reference:            textPtrFromPg(reference),
			ReversesTransferID:   uuidPtrFromPg(reversesTransferID),
			Metadata:             json.RawMessage(metadata),
			CreatedAt:            createdAt,
			PostedAt:             postedAtPtr,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var nextCursor *uuid.UUID
	if len(transfers) > limit {
		transfers = transfers[:limit]
		value := transfers[len(transfers)-1].ID
		nextCursor = &value
	}
	return transfers, nextCursor, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		strings.Contains(pgErr.ConstraintName, "owner_currency")
}
