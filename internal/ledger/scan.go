package ledger

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Column lists are kept next to their scanners so a SELECT and its Scan can
// never drift apart.
const (
	accountColumns = `id, kind::text, owner_ref, currency, normal_side::text, allow_negative, status, created_at`

	transferColumns = `id, type::text, status::text, amount, currency,
		       source_account_id, destination_account_id, reference,
		       reverses_transfer_id, metadata, created_at, posted_at`

	entryColumns = `id, transfer_id, account_id, currency, direction::text, amount, balance_after, created_at`

	holdColumns = `id, account_id, amount, currency, status::text, reference,
		       expires_at, transfer_id, created_at, resolved_at`
)

func scanAccount(row pgx.Row) (Account, error) {
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
	if err := row.Scan(&id, &kind, &ownerRef, &currency, &normalSide, &allowNegative, &status, &createdAt); err != nil {
		return Account{}, notFoundOnNoRows(err)
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

func scanTransfer(row pgx.Row) (Transfer, error) {
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
	if err := row.Scan(
		&id, &transferType, &status, &amount, &currency,
		&sourceAccountID, &destinationAccountID, &reference,
		&reversesTransferID, &metadata, &createdAt, &postedAt,
	); err != nil {
		return Transfer{}, notFoundOnNoRows(err)
	}
	return Transfer{
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
		PostedAt:             timePtrFromPg(postedAt),
	}, nil
}

func scanEntry(row pgx.Row) (Entry, error) {
	var (
		entry      Entry
		transferID pgtype.UUID
		accountID  pgtype.UUID
		direction  string
	)
	if err := row.Scan(&entry.ID, &transferID, &accountID, &entry.Currency, &direction,
		&entry.Amount, &entry.BalanceAfter, &entry.CreatedAt); err != nil {
		return Entry{}, err
	}
	entry.TransferID = uuidFromPg(transferID)
	entry.AccountID = uuidFromPg(accountID)
	entry.Direction = Direction(direction)
	return entry, nil
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
	if err := row.Scan(&id, &accountID, &amount, &currency, &status, &reference,
		&expiresAt, &transferID, &createdAt, &resolvedAt); err != nil {
		return Hold{}, notFoundOnNoRows(err)
	}
	return Hold{
		ID:         uuidFromPg(id),
		AccountID:  uuidFromPg(accountID),
		Amount:     amount,
		Currency:   currency,
		Status:     HoldStatus(status),
		Reference:  textPtrFromPg(reference),
		ExpiresAt:  expiresAt,
		TransferID: uuidPtrFromPg(transferID),
		CreatedAt:  createdAt,
		ResolvedAt: timePtrFromPg(resolvedAt),
	}, nil
}

// collect adapts a single-row scanner to pgx.CollectRows.
func collect[T any](scan func(pgx.Row) (T, error)) pgx.RowToFunc[T] {
	return func(row pgx.CollectableRow) (T, error) { return scan(row) }
}

func notFoundOnNoRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
