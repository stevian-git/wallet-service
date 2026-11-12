package ledger

import (
	"context"
	"encoding/json"
	"fmt"

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

	original, err := scanTransfer(tx.QueryRow(ctx,
		`SELECT `+transferColumns+` FROM transfers WHERE id = $1 FOR UPDATE`, toPgUUID(transferID)))
	if err != nil {
		return Transfer{}, err
	}
	if original.Status != TransferStatusPosted {
		return Transfer{}, fmt.Errorf("%w: transfer status is %s", ErrInvalidPosting, original.Status)
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
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (PostingEntry, error) {
		var (
			accountID pgtype.UUID
			entry     PostingEntry
			direction string
		)
		if err := row.Scan(&accountID, &entry.Currency, &direction, &entry.Amount); err != nil {
			return PostingEntry{}, err
		}
		entry.AccountID = uuidFromPg(accountID)
		entry.Direction = Direction(direction).opposite()
		return entry, nil
	})
	if err != nil {
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

	return l.Post(ctx, tx, PostingRequest{
		Type:                 TransferTypeReversal,
		SourceAccountID:      original.DestinationAccountID,
		DestinationAccountID: original.SourceAccountID,
		Amount:               original.Amount,
		Currency:             original.Currency,
		Reference:            original.Reference,
		ReversesTransferID:   &original.ID,
		Metadata:             reversalMetadata,
		Entries:              entries,
	})
}

func (d Direction) opposite() Direction {
	if d == DirectionCredit {
		return DirectionDebit
	}
	return DirectionCredit
}
