package ledger

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type BalanceDrift struct {
	AccountID     uuid.UUID
	CachedPosted  int64
	DerivedPosted int64
}

type UnbalancedTransfer struct {
	TransferID  uuid.UUID
	TotalDebit  int64
	TotalCredit int64
}

type IntegrityReport struct {
	CheckedAt            time.Time
	BalanceDrift         []BalanceDrift
	UnbalancedTransfers  []UnbalancedTransfer
	PostedWithoutEntries []uuid.UUID
}

func (l *Ledger) Integrity(ctx context.Context) (IntegrityReport, error) {
	report := IntegrityReport{CheckedAt: time.Now().UTC()}

	var err error
	report.BalanceDrift, err = queryAll(ctx, l, `
		SELECT account_id, cached_posted, derived_posted
		  FROM v_balance_drift
		 ORDER BY account_id`, func(row pgx.CollectableRow) (BalanceDrift, error) {
		var (
			item      BalanceDrift
			accountID pgtype.UUID
		)
		err := row.Scan(&accountID, &item.CachedPosted, &item.DerivedPosted)
		item.AccountID = uuidFromPg(accountID)
		return item, err
	})
	if err != nil {
		return IntegrityReport{}, err
	}

	report.UnbalancedTransfers, err = queryAll(ctx, l, `
		SELECT transfer_id, total_debit, total_credit
		  FROM v_unbalanced_transfers
		 ORDER BY transfer_id`, func(row pgx.CollectableRow) (UnbalancedTransfer, error) {
		var (
			item       UnbalancedTransfer
			transferID pgtype.UUID
		)
		err := row.Scan(&transferID, &item.TotalDebit, &item.TotalCredit)
		item.TransferID = uuidFromPg(transferID)
		return item, err
	})
	if err != nil {
		return IntegrityReport{}, err
	}

	report.PostedWithoutEntries, err = queryAll(ctx, l, `
		SELECT transfer_id
		  FROM v_posted_transfers_without_entries
		 ORDER BY transfer_id`, func(row pgx.CollectableRow) (uuid.UUID, error) {
		var transferID pgtype.UUID
		err := row.Scan(&transferID)
		return uuidFromPg(transferID), err
	})
	if err != nil {
		return IntegrityReport{}, err
	}

	return report, nil
}

func queryAll[T any](ctx context.Context, l *Ledger, sql string, scan pgx.RowToFunc[T]) ([]T, error) {
	rows, err := l.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scan)
}
