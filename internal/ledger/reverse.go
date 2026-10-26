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

func (l *Ledger) Reverse(ctx context.Context, tx pgx.Tx, transferID uuid.UUID, reason string) (Transfer, error) {
	if tx == nil {
		return Transfer{}, ErrNoTransaction
	}
	if len(reason) > 255 {
		return Transfer{}, fmt.Errorf("%w: reason must be at most 255 characters", ErrInvalidPosting)
	}

	var (
		originalID           pgtype.UUID
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
	err := tx.QueryRow(ctx, `
		SELECT id, type::text, status::text, amount, currency,
		       source_account_id, destination_account_id, reference,
		       reverses_transfer_id, metadata, created_at, posted_at
		  FROM transfers
		 WHERE id = $1
		   FOR UPDATE`, toPgUUID(transferID),
	).Scan(
		&originalID, &transferType, &status, &amount, &currency,
		&sourceAccountID, &destinationAccountID, &reference,
		&reversesTransferID, &metadata, &createdAt, &postedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, ErrNotFound
	}
	if err != nil {
		return Transfer{}, err
	}
	if status != string(TransferStatusPosted) {
		return Transfer{}, fmt.Errorf("%w: transfer status is %s", ErrInvalidPosting, status)
	}

	var alreadyReversed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM transfers WHERE reverses_transfer_id = $1)`, toPgUUID(transferID)).Scan(&alreadyReversed); err != nil {
		return Transfer{}, err
	}
	if alreadyReversed {
		return Transfer{}, ErrAlreadyReversed
	}

	rows, err := tx.Query(ctx, `
		SELECT account_id, currency, direction::text, amount
		  FROM ledger_entries
		 WHERE transfer_id = $1
		 ORDER BY id`, toPgUUID(transferID))
	if err != nil {
		return Transfer{}, err
	}
	defer rows.Close()

	entries := make([]PostingEntry, 0, 2)
	for rows.Next() {
		var (
			accountID pgtype.UUID
			entryCur  string
			direction string
			entryAmt  int64
		)
		if err := rows.Scan(&accountID, &entryCur, &direction, &entryAmt); err != nil {
			return Transfer{}, err
		}
		inverted := DirectionCredit
		if Direction(direction) == DirectionCredit {
			inverted = DirectionDebit
		}
		entries = append(entries, PostingEntry{
			AccountID: uuidFromPg(accountID),
			Currency:  entryCur,
			Direction: inverted,
			Amount:    entryAmt,
		})
	}
	if err := rows.Err(); err != nil {
		return Transfer{}, err
	}

	reversalMetadata := json.RawMessage(`{}`)
	if reason != "" {
		payload, err := json.Marshal(map[string]string{"reason": reason})
		if err != nil {
			return Transfer{}, err
		}
		reversalMetadata = payload
	}

	originalUUID := uuidFromPg(originalID)
	source := sourceAccountID
	destination := destinationAccountID
	return l.Post(ctx, tx, PostingRequest{
		Type:                 TransferTypeReversal,
		SourceAccountID:      uuidPtrFromPg(destination),
		DestinationAccountID: uuidPtrFromPg(source),
		Amount:               amount,
		Currency:             currency,
		Reference:            textPtrFromPg(reference),
		ReversesTransferID:   &originalUUID,
		Metadata:             reversalMetadata,
		Entries:              entries,
	})
}
