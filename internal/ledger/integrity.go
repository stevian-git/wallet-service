package ledger

import (
	"context"
	"time"

	"github.com/google/uuid"
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
	checkedAt := time.Now().UTC()

	driftRows, err := l.pool.Query(ctx, `
		SELECT account_id, cached_posted, derived_posted
		  FROM v_balance_drift
		 ORDER BY account_id`)
	if err != nil {
		return IntegrityReport{}, err
	}
	defer driftRows.Close()

	drift := make([]BalanceDrift, 0)
	for driftRows.Next() {
		var (
			accountID pgtype.UUID
			cached    int64
			derived   int64
		)
		if err := driftRows.Scan(&accountID, &cached, &derived); err != nil {
			return IntegrityReport{}, err
		}
		drift = append(drift, BalanceDrift{
			AccountID:     uuidFromPg(accountID),
			CachedPosted:  cached,
			DerivedPosted: derived,
		})
	}
	if err := driftRows.Err(); err != nil {
		return IntegrityReport{}, err
	}

	unbalancedRows, err := l.pool.Query(ctx, `
		SELECT transfer_id, total_debit, total_credit
		  FROM v_unbalanced_transfers
		 ORDER BY transfer_id`)
	if err != nil {
		return IntegrityReport{}, err
	}
	defer unbalancedRows.Close()

	unbalanced := make([]UnbalancedTransfer, 0)
	for unbalancedRows.Next() {
		var (
			transferID  pgtype.UUID
			totalDebit  int64
			totalCredit int64
		)
		if err := unbalancedRows.Scan(&transferID, &totalDebit, &totalCredit); err != nil {
			return IntegrityReport{}, err
		}
		unbalanced = append(unbalanced, UnbalancedTransfer{
			TransferID:  uuidFromPg(transferID),
			TotalDebit:  totalDebit,
			TotalCredit: totalCredit,
		})
	}
	if err := unbalancedRows.Err(); err != nil {
		return IntegrityReport{}, err
	}

	missingRows, err := l.pool.Query(ctx, `
		SELECT transfer_id
		  FROM v_posted_transfers_without_entries
		 ORDER BY transfer_id`)
	if err != nil {
		return IntegrityReport{}, err
	}
	defer missingRows.Close()

	missing := make([]uuid.UUID, 0)
	for missingRows.Next() {
		var transferID pgtype.UUID
		if err := missingRows.Scan(&transferID); err != nil {
			return IntegrityReport{}, err
		}
		missing = append(missing, uuidFromPg(transferID))
	}
	if err := missingRows.Err(); err != nil {
		return IntegrityReport{}, err
	}

	return IntegrityReport{
		CheckedAt:            checkedAt,
		BalanceDrift:         drift,
		UnbalancedTransfers:  unbalanced,
		PostedWithoutEntries: missing,
	}, nil
}
